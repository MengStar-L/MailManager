package mailruntime

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
	"mailmanager/internal/cryptox"
	"mailmanager/internal/events"
	"mailmanager/internal/ingeststore"
	"mailmanager/internal/oauthflow"
	"mailmanager/internal/repository"
	"mailmanager/internal/store"
	mailSync "mailmanager/internal/sync"
)

const (
	defaultAttachmentLimit = int64(25 << 20)
	defaultCacheQuota      = int64(5 << 30)
	defaultQueuePoll       = time.Second
)

// OAuthRefresher is the subset of oauthflow.Service used by the runtime.
type OAuthRefresher interface {
	RefreshToken(context.Context, accounts.Provider, *oauth2.Token) (*oauth2.Token, error)
}

// MailSender is implemented by connectors.SMTPSender and permits deterministic
// delivery classification in tests.
type MailSender interface {
	Send(context.Context, connectors.SMTPConfig, connectors.Envelope) (connectors.DeliveryResult, error)
}

type Options struct {
	Logger                    *slog.Logger
	Store                     *store.Store
	Repository                *repository.Repository
	IngestStore               *ingeststore.Store
	Cipher                    *cryptox.Cipher
	OAuth                     OAuthRefresher
	Events                    *events.Hub
	IMAPDialer                connectors.IMAPDialer
	SMTPSender                MailSender
	AttachmentCacheDir        string
	DraftBlobDir              string
	MaxAttachmentBytes        int64
	AttachmentCacheQuotaBytes int64
}

// Runtime coordinates durable mail work. Protocol implementations remain in
// connectors; this package only owns scheduling and state transitions.
type Runtime struct {
	logger       *slog.Logger
	store        *store.Store
	repository   *repository.Repository
	ingest       *ingeststore.Store
	cipher       *cryptox.Cipher
	oauth        OAuthRefresher
	events       *events.Hub
	imapDialer   connectors.IMAPDialer
	smtpSender   MailSender
	synchronizer mailSync.FolderSynchronizer

	cacheDir           string
	draftBlobDir       string
	maxAttachmentBytes int64
	cacheQuotaBytes    int64
	cacheMu            sync.Mutex

	startMu sync.Mutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	syncHigh chan syncJob
	syncLow  chan syncJob
	syncMu   sync.Mutex
	pending  map[string]*syncJobState
	failures map[string]int
	// fgWaiting/bgWaiting park jobs whose per-account slot is taken; the
	// slot holder re-dispatches the next one on release, so contended jobs
	// chain instead of blocking workers or colliding on retry timers.
	fgWaiting map[string][]syncJob
	bgWaiting map[string][]syncJob

	operationWake chan struct{}
	outboxWake    chan struct{}

	idleMu       sync.Mutex
	idleWatchers map[string]idleWatcher
	idleSequence uint64

	accountLocks sync.Map
	// syncFgLocks serialize quick foreground jobs (discover, inbox and folder
	// incremental syncs) per account; syncBgLocks serialize long background
	// jobs (backfill, reconcile) so they never delay new-mail syncs.
	syncFgLocks sync.Map
	syncBgLocks sync.Map

	poolMu   sync.Mutex
	sessions map[string]*pooledSession

	pollInterval      time.Duration
	reconcileInterval time.Duration
	queuePollInterval time.Duration
}

type syncJob struct {
	accountID string
	folderID  string
	mailbox   string
	kind      syncJobKind
}

type syncJobKind uint8

const (
	syncDiscover syncJobKind = iota + 1
	syncFolder
	syncBackfill
	syncReconcile
)

type syncJobState struct {
	job   syncJob
	rerun bool
}

type idleWatcher struct {
	folderID string
	mailbox  string
	cancel   context.CancelFunc
	token    uint64
}

func New(options Options) (*Runtime, error) {
	if options.Store == nil || options.Repository == nil || options.Cipher == nil || options.OAuth == nil || options.Events == nil {
		return nil, errors.New("mail runtime requires store, repository, cipher, OAuth, and events")
	}
	if options.AttachmentCacheDir == "" || options.DraftBlobDir == "" {
		return nil, errors.New("attachment cache and draft blob directories are required")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.IngestStore == nil {
		options.IngestStore = ingeststore.New(options.Store)
	}
	if options.IMAPDialer == nil {
		options.IMAPDialer = connectors.EmersionIMAPDialer{}
	}
	if options.MaxAttachmentBytes <= 0 {
		options.MaxAttachmentBytes = defaultAttachmentLimit
	}
	if options.AttachmentCacheQuotaBytes <= 0 {
		options.AttachmentCacheQuotaBytes = defaultCacheQuota
	}
	if options.AttachmentCacheQuotaBytes < options.MaxAttachmentBytes {
		return nil, errors.New("attachment cache quota must be at least the per-attachment limit")
	}
	if options.SMTPSender == nil {
		options.SMTPSender = connectors.SMTPSender{
			Connector:       connectors.StandardSMTPConnector{},
			MaxMessageBytes: options.MaxAttachmentBytes,
		}
	}
	return &Runtime{
		logger: options.Logger, store: options.Store, repository: options.Repository,
		ingest: options.IngestStore, cipher: options.Cipher, oauth: options.OAuth,
		events: options.Events, imapDialer: options.IMAPDialer, smtpSender: options.SMTPSender,
		synchronizer: mailSync.FolderSynchronizer{MaxBodyPartBytes: 10 << 20},
		cacheDir:     options.AttachmentCacheDir, draftBlobDir: options.DraftBlobDir,
		maxAttachmentBytes: options.MaxAttachmentBytes,
		cacheQuotaBytes:    options.AttachmentCacheQuotaBytes,
		syncHigh:           make(chan syncJob, 256), syncLow: make(chan syncJob, 256),
		pending: make(map[string]*syncJobState), failures: make(map[string]int),
		fgWaiting: make(map[string][]syncJob), bgWaiting: make(map[string][]syncJob),
		operationWake: make(chan struct{}, 1), outboxWake: make(chan struct{}, 1),
		idleWatchers: make(map[string]idleWatcher), sessions: make(map[string]*pooledSession),
		pollInterval: mailSync.InboxPollInterval, reconcileInterval: mailSync.FolderReconcileInterval,
		queuePollInterval: defaultQueuePoll,
	}, nil
}

// Start launches background workers and returns after durable recovery has
// completed. Cancellation of ctx stops all workers.
func (r *Runtime) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("runtime context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if r.started {
		return errors.New("mail runtime is already started")
	}
	for _, directory := range []string{r.cacheDir, r.draftBlobDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	if err := r.recoverInFlight(ctx); err != nil {
		return err
	}

	r.ctx, r.cancel = context.WithCancel(ctx)
	r.started = true

	for range mailSync.DefaultWorkerLimit {
		r.goWorker(r.syncWorker)
	}
	r.goWorker(r.syncScheduler)
	r.goWorker(r.operationWorker)
	r.goWorker(r.outboxWorker)
	return nil
}

func (r *Runtime) goWorker(worker func(context.Context)) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		worker(r.ctx)
	}()
}

func (r *Runtime) goDynamic(worker func(context.Context), ctx context.Context) bool {
	r.startMu.Lock()
	if !r.started || r.ctx == nil || r.ctx.Err() != nil {
		r.startMu.Unlock()
		return false
	}
	r.wg.Add(1)
	r.startMu.Unlock()
	go func() {
		defer r.wg.Done()
		worker(ctx)
	}()
	return true
}

// Close requests shutdown and waits until protocol calls observe cancellation.
func (r *Runtime) Close() {
	r.startMu.Lock()
	cancel := r.cancel
	r.startMu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.stopAllIdleWatchers()
	r.wg.Wait()
	r.closeAllSessions()
}

func (r *Runtime) Wait() { r.wg.Wait() }

func (r *Runtime) WakeOperations() { signal(r.operationWake) }

func (r *Runtime) WakeOutbox() { signal(r.outboxWake) }

func signal(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (r *Runtime) runtimeContext() (context.Context, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if !r.started || r.ctx == nil || r.ctx.Err() != nil {
		return nil, errors.New("mail runtime is not running")
	}
	return r.ctx, nil
}

func (r *Runtime) accountLock(accountID string) *sync.Mutex {
	value, _ := r.accountLocks.LoadOrStore(accountID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (r *Runtime) syncForegroundLock(accountID string) *sync.Mutex {
	value, _ := r.syncFgLocks.LoadOrStore(accountID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (r *Runtime) syncBackgroundLock(accountID string) *sync.Mutex {
	value, _ := r.syncBgLocks.LoadOrStore(accountID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// Compile-time guard without importing the HTTP API package.
var _ OAuthRefresher = (*oauthflow.Service)(nil)

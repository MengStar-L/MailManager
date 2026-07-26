package mailruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"mailmanager/internal/events"
	"mailmanager/internal/repository"
	mailSync "mailmanager/internal/sync"
)

// errSyncBusy marks a job that found its per-account slot taken and parked
// itself in the tier's waiting queue; it is not a failure, and the slot
// holder re-dispatches it on release.
var errSyncBusy = errors.New("account sync slot is busy")

func (job syncJob) key() string {
	switch job.kind {
	case syncDiscover:
		return "discover:" + job.accountID
	case syncFolder:
		return "folder:" + job.accountID + ":" + job.folderID
	case syncBackfill:
		return "backfill:" + job.accountID + ":" + job.folderID
	case syncReconcile:
		return "reconcile:" + job.accountID + ":" + job.folderID
	default:
		return "invalid"
	}
}

// timeoutForSyncJob is a backstop against a stalled provider wedging a worker
// and, through job deduplication, silencing an account's syncs forever. The
// bounds are far above healthy durations.
func timeoutForSyncJob(kind syncJobKind) time.Duration {
	switch kind {
	case syncDiscover:
		return 5 * time.Minute
	case syncReconcile:
		return time.Hour
	case syncBackfill:
		return 2 * time.Hour
	default:
		return 30 * time.Minute
	}
}

func (r *Runtime) enqueueSync(ctx context.Context, job syncJob) error {
	if job.accountID == "" || job.kind == 0 || (job.kind != syncDiscover && (job.folderID == "" || job.mailbox == "")) {
		return errors.New("invalid sync job")
	}
	key := job.key()
	r.syncMu.Lock()
	if current, ok := r.pending[key]; ok {
		current.job = job
		current.rerun = true
		r.syncMu.Unlock()
		return nil
	}
	r.pending[key] = &syncJobState{job: job}
	r.syncMu.Unlock()

	if err := r.dispatchSync(ctx, job); err == nil {
		return nil
	} else {
		r.dropPendingSync(job)
		return err
	}
}

// dispatchSync never blocks a sync worker on a full execution queue. The
// pending map remains the durable in-memory ownership record while a tracked
// dispatcher waits for capacity, so deduplication and rerun requests continue
// to target the same job state.
func (r *Runtime) dispatchSync(ctx context.Context, job syncJob) error {
	queue := r.syncHigh
	if job.kind == syncBackfill || job.kind == syncReconcile {
		queue = r.syncLow
	}
	select {
	case queue <- job:
		return nil
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.startMu.Lock()
	runtimeCtx := r.ctx
	running := r.started && runtimeCtx != nil && runtimeCtx.Err() == nil
	r.startMu.Unlock()
	if !running {
		return errors.New("sync queue is full while runtime is not running")
	}
	if !r.goDynamic(func(dispatchCtx context.Context) {
		select {
		case queue <- job:
		case <-dispatchCtx.Done():
			r.dropPendingSync(job)
		}
	}, runtimeCtx) {
		if err := runtimeCtx.Err(); err != nil {
			return err
		}
		return errors.New("sync queue is full while runtime is not running")
	}
	return nil
}

func (r *Runtime) dropPendingSync(job syncJob) {
	r.syncMu.Lock()
	delete(r.pending, job.key())
	r.syncMu.Unlock()
}

// jobFailures reports how many consecutive attempts of this job have failed;
// retries use it to permit degraded body storage so one transient failure
// gets a clean retry before a poison message is stored bodyless.
func (r *Runtime) jobFailures(job syncJob) int {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	return r.failures[job.key()]
}

func (r *Runtime) syncWorker(ctx context.Context) {
	for {
		job, ok := r.nextSyncJob(ctx)
		if !ok {
			return
		}
		err := r.runSyncJob(ctx, job)
		r.finishSyncJob(ctx, job, err)
	}
}

func (r *Runtime) nextSyncJob(ctx context.Context) (syncJob, bool) {
	select {
	case job := <-r.syncHigh:
		return job, true
	default:
	}
	select {
	case <-ctx.Done():
		return syncJob{}, false
	case job := <-r.syncHigh:
		return job, true
	case job := <-r.syncLow:
		return job, true
	}
}

func (r *Runtime) finishSyncJob(ctx context.Context, job syncJob, runErr error) {
	if errors.Is(runErr, errSyncBusy) {
		// The job stays pending while parked so deduplication keeps working;
		// releaseSyncSlot dispatches it again.
		return
	}
	key := job.key()
	r.syncMu.Lock()
	state := r.pending[key]
	if runErr == nil {
		delete(r.failures, key)
	} else {
		r.failures[key]++
	}
	failures := r.failures[key]
	if state != nil && state.rerun && ctx.Err() == nil {
		next := state.job
		state.rerun = false
		r.syncMu.Unlock()
		if err := r.dispatchSync(ctx, next); err != nil {
			r.dropPendingSync(next)
		}
		return
	}
	delete(r.pending, key)
	r.syncMu.Unlock()

	if runErr == nil || ctx.Err() != nil || errors.Is(runErr, repository.ErrNotFound) {
		return
	}
	delay := mailSync.DefaultBackoff().Delay(failures, 0.5)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			_ = r.enqueueSync(ctx, job)
		}
	}()
}

// acquireSyncSlot claims the account's slot in the given tier or parks the
// job until the current holder releases it. Parking under syncMu makes the
// TryLock-or-park decision atomic with respect to releaseSyncSlot. Workers
// never block on account locks: a long job (e.g. a large folder heal) must
// not be able to starve the pool through its queued siblings.
func (r *Runtime) acquireSyncSlot(lock *sync.Mutex, waiting map[string][]syncJob, job syncJob) bool {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	if lock.TryLock() {
		return true
	}
	waiting[job.accountID] = append(waiting[job.accountID], job)
	return false
}

func (r *Runtime) releaseSyncSlot(ctx context.Context, lock *sync.Mutex, waiting map[string][]syncJob, accountID string) {
	lock.Unlock()
	r.syncMu.Lock()
	queue := waiting[accountID]
	var next *syncJob
	if len(queue) > 0 {
		job := queue[0]
		next = &job
		if len(queue) == 1 {
			delete(waiting, accountID)
		} else {
			waiting[accountID] = queue[1:]
		}
	}
	r.syncMu.Unlock()
	if next == nil {
		return
	}
	if ctx.Err() != nil {
		r.dropPendingSync(*next)
		return
	}
	if err := r.dispatchSync(ctx, *next); err != nil {
		r.dropPendingSync(*next)
	}
}

func (r *Runtime) runSyncJob(ctx context.Context, job syncJob) error {
	switch job.kind {
	case syncBackfill, syncReconcile:
		lock := r.syncBackgroundLock(job.accountID)
		if !r.acquireSyncSlot(lock, r.bgWaiting, job) {
			return errSyncBusy
		}
		defer r.releaseSyncSlot(ctx, lock, r.bgWaiting, job.accountID)
	default:
		lock := r.syncForegroundLock(job.accountID)
		if !r.acquireSyncSlot(lock, r.fgWaiting, job) {
			return errSyncBusy
		}
		defer r.releaseSyncSlot(ctx, lock, r.fgWaiting, job.accountID)
	}

	jobCtx, cancel := context.WithTimeout(ctx, timeoutForSyncJob(job.kind))
	defer cancel()

	var err error
	switch job.kind {
	case syncDiscover:
		err = r.discoverAccount(jobCtx, job.accountID)
	case syncFolder, syncReconcile:
		err = r.syncFolder(jobCtx, job)
	case syncBackfill:
		err = r.backfillFolder(jobCtx, job)
	default:
		err = errors.New("unsupported sync job")
	}
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		status, code := "error", "sync_failed"
		if errors.Is(err, errOAuthTokenPersist) {
			status, code = "reauth_required", "oauth_token_persist_failed"
		} else if errors.Is(err, errOAuthRefresh) {
			status, code = "reauth_required", "oauth_refresh_failed"
		}
		// A failing background reconcile or backfill must not flip the whole
		// account into an error state while new mail keeps arriving; the
		// folder-level error marker and log carry the detail. Credential
		// problems always surface.
		background := job.kind == syncBackfill || job.kind == syncReconcile
		if !background || status != "error" {
			_ = r.repository.UpdateAccountStatus(ctx, job.accountID, status, code)
			r.events.Publish(events.Event{Type: "account.status", ResourceID: job.accountID, State: status})
		}
		r.logger.Warn("mail sync job failed", "account_id", job.accountID, "folder_id", job.folderID, "kind", job.kind, "error", err)
		return err
	}
	return nil
}

func (r *Runtime) discoverAccount(ctx context.Context, accountID string) (err error) {
	_ = r.repository.UpdateAccountStatus(ctx, accountID, "syncing", "")
	r.events.Publish(events.Event{Type: "sync.progress", ResourceID: accountID, State: "discovering"})
	config, err := r.accountConfig(ctx, accountID)
	if err != nil {
		return err
	}
	pooled, err := r.acquireSession(ctx, accountID, config)
	if err != nil {
		return fmt.Errorf("dial IMAP for discovery: %w", err)
	}
	defer func() { r.releaseSession(accountID, pooled, err == nil) }()
	session := pooled.session
	capabilities, err := session.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("read IMAP capabilities: %w", err)
	}
	mailboxes, err := session.ListMailboxes(ctx)
	if err != nil {
		return fmt.Errorf("list IMAP mailboxes: %w", err)
	}
	folders, err := r.ingest.UpsertFolders(ctx, accountID, mailboxes)
	if err != nil {
		return fmt.Errorf("store IMAP mailboxes: %w", err)
	}
	var inbox *struct{ id, mailbox string }
	for _, folder := range folders {
		if !folder.Selectable {
			continue
		}
		if folder.Role == "inbox" {
			inbox = &struct{ id, mailbox string }{folder.ID, folder.RemoteName}
		}
		if err := r.enqueueSync(ctx, syncJob{accountID: accountID, folderID: folder.ID, mailbox: folder.RemoteName, kind: syncFolder}); err != nil {
			return err
		}
	}
	if capabilities.Idle && inbox != nil {
		r.ensureIdleWatcher(accountID, inbox.id, inbox.mailbox)
	} else {
		r.stopIdleWatcher(accountID)
	}
	if err := r.repository.UpdateAccountStatus(ctx, accountID, "ready", ""); err != nil {
		return err
	}
	r.events.Publish(events.Event{Type: "account.status", ResourceID: accountID, State: "ready"})
	return nil
}

func (r *Runtime) syncFolder(ctx context.Context, job syncJob) (err error) {
	config, err := r.accountConfig(ctx, job.accountID)
	if err != nil {
		return err
	}
	// Foreground and background jobs draw from separate per-account pool
	// slots: reconciles must not hold the new-mail session, but a reconcile
	// burst over many folders should still cost one login, not one each —
	// 163/QQ throttle exactly that pattern.
	poolKey := job.accountID
	if job.kind == syncReconcile {
		poolKey = backgroundPoolKey(job.accountID)
	}
	pooled, acquireErr := r.acquireSession(ctx, poolKey, config)
	if acquireErr != nil {
		return fmt.Errorf("dial IMAP for folder sync: %w", acquireErr)
	}
	defer func() { r.releaseSession(poolKey, pooled, err == nil) }()
	session := pooled.session
	checkpoint, err := r.ingest.Checkpoint(ctx, job.accountID, job.folderID)
	if err != nil {
		return err
	}
	pending, err := r.ingest.PendingOperationIDs(ctx, job.accountID)
	if err != nil {
		return err
	}
	request := mailSync.FolderSyncRequest{
		AccountID: job.accountID, FolderID: job.folderID, Mailbox: job.mailbox, Provider: config.Provider,
		Checkpoint: checkpoint, PendingOperationIDs: pending, Now: time.Now().UTC(),
		AllowDegradedBodies: r.jobFailures(job) > 0,
	}
	if job.kind == syncReconcile {
		_, err = r.synchronizer.Sync(ctx, session, r.ingest, request)
	} else {
		_, err = r.synchronizer.SyncIncremental(ctx, session, r.ingest, request)
	}
	if err != nil {
		_ = r.ingest.MarkFolderError(ctx, job.accountID, job.folderID, "sync_failed")
		return err
	}
	needsBackfill, err := r.ingest.NeedsBackfill(ctx, job.accountID, job.folderID)
	if err != nil {
		return err
	}
	if needsBackfill {
		_ = r.enqueueSync(ctx, syncJob{accountID: job.accountID, folderID: job.folderID, mailbox: job.mailbox, kind: syncBackfill})
	}
	_ = r.repository.UpdateAccountStatus(ctx, job.accountID, "ready", "")
	r.events.Publish(events.Event{Type: "sync.progress", ResourceID: job.accountID, State: "complete", Version: time.Now().UTC().UnixMilli()})
	return nil
}

func (r *Runtime) backfillFolder(ctx context.Context, job syncJob) (err error) {
	config, err := r.accountConfig(ctx, job.accountID)
	if err != nil {
		return err
	}
	poolKey := backgroundPoolKey(job.accountID)
	pooled, acquireErr := r.acquireSession(ctx, poolKey, config)
	if acquireErr != nil {
		return fmt.Errorf("dial IMAP for history backfill: %w", acquireErr)
	}
	defer func() { r.releaseSession(poolKey, pooled, err == nil) }()
	session := pooled.session
	checkpoint, err := r.ingest.Checkpoint(ctx, job.accountID, job.folderID)
	if err != nil {
		return err
	}
	if checkpoint.UIDValidity == 0 {
		return errors.New("history backfill requires an initialized checkpoint")
	}
	pending, err := r.ingest.PendingOperationIDs(ctx, job.accountID)
	if err != nil {
		return err
	}
	request := mailSync.FolderSyncRequest{
		AccountID: job.accountID, FolderID: job.folderID, Mailbox: job.mailbox, Provider: config.Provider,
		Checkpoint: checkpoint, PendingOperationIDs: pending, Now: time.Now().UTC(),
		AllowDegradedBodies: r.jobFailures(job) > 0,
	}
	if err := r.synchronizer.BackfillHistory(ctx, session, r.ingest, request); err != nil {
		_ = r.ingest.MarkFolderError(ctx, job.accountID, job.folderID, "backfill_failed")
		return err
	}
	if err := r.ingest.CompleteBackfill(ctx, job.accountID, job.folderID); err != nil {
		return err
	}
	r.events.Publish(events.Event{Type: "sync.progress", ResourceID: job.accountID, State: "history_complete", Version: time.Now().UTC().UnixMilli()})
	return nil
}

func (r *Runtime) syncScheduler(ctx context.Context) {
	r.discoverAll(ctx)
	poll := time.NewTicker(r.pollInterval)
	reconcile := time.NewTicker(r.reconcileInterval)
	defer poll.Stop()
	defer reconcile.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			r.reapSessions()
			r.scheduleInboxPolling(ctx)
		case <-reconcile.C:
			r.scheduleFolderReconciliation(ctx)
		}
	}
}

func (r *Runtime) discoverAll(ctx context.Context) {
	accounts, err := r.repository.ListAccounts(ctx)
	if err != nil {
		r.logger.Warn("list accounts for initial sync", "error", err)
		return
	}
	for _, account := range accounts {
		if account.Status != "disabled" {
			_ = r.enqueueSync(ctx, syncJob{accountID: account.ID, kind: syncDiscover})
		}
	}
}

func (r *Runtime) scheduleInboxPolling(ctx context.Context) {
	accounts, err := r.repository.ListAccounts(ctx)
	if err != nil {
		r.logger.Warn("list accounts for inbox polling", "error", err)
		return
	}
	present := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		present[account.ID] = struct{}{}
		if account.Status == "disabled" {
			continue
		}
		folders, err := r.ingest.Folders(ctx, account.ID)
		if err != nil || len(folders) == 0 {
			discover := syncJob{accountID: account.ID, kind: syncDiscover}
			if r.jobFailures(discover) == 0 {
				_ = r.enqueueSync(ctx, discover)
			}
			continue
		}
		for _, folder := range folders {
			if folder.Role == "inbox" {
				job := syncJob{accountID: account.ID, folderID: folder.ID, mailbox: folder.RemoteName, kind: syncFolder}
				// A failing job belongs to its backoff timer: re-enqueueing
				// it every poll tick would hammer a throttling provider with
				// fresh login attempts at the poll cadence.
				if r.jobFailures(job) == 0 {
					_ = r.enqueueSync(ctx, job)
				}
				break
			}
		}
	}
	r.removeIdleWatchersNotIn(present)
}

func (r *Runtime) scheduleFolderReconciliation(ctx context.Context) {
	accounts, err := r.repository.ListAccounts(ctx)
	if err != nil {
		r.logger.Warn("list accounts for folder reconciliation", "error", err)
		return
	}
	for _, account := range accounts {
		if account.Status == "disabled" {
			continue
		}
		folders, err := r.ingest.Folders(ctx, account.ID)
		if err != nil {
			continue
		}
		for _, folder := range folders {
			_ = r.enqueueSync(ctx, syncJob{
				accountID: account.ID, folderID: folder.ID, mailbox: folder.RemoteName,
				kind: syncReconcile,
			})
		}
	}
}

func (r *Runtime) ensureIdleWatcher(accountID, folderID, mailbox string) {
	r.idleMu.Lock()
	if existing, ok := r.idleWatchers[accountID]; ok {
		if existing.folderID == folderID && existing.mailbox == mailbox {
			r.idleMu.Unlock()
			return
		}
		existing.cancel()
	}
	ctx, cancel := context.WithCancel(r.ctx)
	r.idleSequence++
	token := r.idleSequence
	r.idleWatchers[accountID] = idleWatcher{folderID: folderID, mailbox: mailbox, cancel: cancel, token: token}
	r.idleMu.Unlock()
	if !r.goDynamic(func(ctx context.Context) { r.idleLoop(ctx, accountID, folderID, mailbox, token) }, ctx) {
		cancel()
	}
}

func (r *Runtime) idleLoop(ctx context.Context, accountID, folderID, mailbox string, token uint64) {
	defer r.finishIdleWatcher(accountID, token)
	failures := 0
	for ctx.Err() == nil {
		config, err := r.accountConfig(ctx, accountID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return
			}
			r.waitIdleRetry(ctx, &failures)
			continue
		}
		session, err := r.imapDialer.Dial(ctx, config)
		if err != nil {
			r.waitIdleRetry(ctx, &failures)
			continue
		}
		// The watcher context has no deadline, so bound the setup exchanges
		// separately: a stalled server must not wedge the watcher forever.
		setupCtx, cancelSetup := context.WithTimeout(ctx, time.Minute)
		caps, err := session.Capabilities(setupCtx)
		if err != nil || !caps.Idle {
			cancelSetup()
			_ = session.Close()
			if err == nil {
				return
			}
			r.waitIdleRetry(ctx, &failures)
			continue
		}
		_, err = session.Select(setupCtx, mailbox, true)
		cancelSetup()
		if err != nil {
			_ = session.Close()
			r.waitIdleRetry(ctx, &failures)
			continue
		}
		failures = 0
		for ctx.Err() == nil {
			idleCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
			_, err := session.Idle(idleCtx)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}
			if err != nil {
				break
			}
			_ = r.enqueueSync(ctx, syncJob{accountID: accountID, folderID: folderID, mailbox: mailbox, kind: syncFolder})
		}
		_ = session.Close()
		if ctx.Err() == nil {
			r.waitIdleRetry(ctx, &failures)
		}
	}
}

func (r *Runtime) waitIdleRetry(ctx context.Context, failures *int) {
	(*failures)++
	timer := time.NewTimer(mailSync.DefaultBackoff().Delay(*failures, 0.5))
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (r *Runtime) finishIdleWatcher(accountID string, token uint64) {
	r.idleMu.Lock()
	defer r.idleMu.Unlock()
	if watcher, ok := r.idleWatchers[accountID]; ok && watcher.token == token {
		delete(r.idleWatchers, accountID)
	}
}

func (r *Runtime) stopIdleWatcher(accountID string) {
	r.idleMu.Lock()
	watcher, ok := r.idleWatchers[accountID]
	if ok {
		delete(r.idleWatchers, accountID)
	}
	r.idleMu.Unlock()
	if ok {
		watcher.cancel()
	}
}

func (r *Runtime) stopAllIdleWatchers() {
	r.idleMu.Lock()
	watchers := make([]idleWatcher, 0, len(r.idleWatchers))
	for _, watcher := range r.idleWatchers {
		watchers = append(watchers, watcher)
	}
	clear(r.idleWatchers)
	r.idleMu.Unlock()
	for _, watcher := range watchers {
		watcher.cancel()
	}
}

func (r *Runtime) removeIdleWatchersNotIn(accounts map[string]struct{}) {
	r.idleMu.Lock()
	var stale []idleWatcher
	for accountID, watcher := range r.idleWatchers {
		if _, ok := accounts[accountID]; !ok {
			stale = append(stale, watcher)
			delete(r.idleWatchers, accountID)
		}
	}
	r.idleMu.Unlock()
	for _, watcher := range stale {
		watcher.cancel()
	}
}

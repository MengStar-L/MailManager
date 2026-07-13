package mailruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mailmanager/internal/events"
	"mailmanager/internal/repository"
	mailSync "mailmanager/internal/sync"
)

func (job syncJob) key() string {
	switch job.kind {
	case syncDiscover:
		return "discover:" + job.accountID
	case syncFolder:
		return "folder:" + job.accountID + ":" + job.folderID
	case syncBackfill:
		return "backfill:" + job.accountID + ":" + job.folderID
	default:
		return "invalid"
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
	if job.kind == syncBackfill {
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

func (r *Runtime) runSyncJob(ctx context.Context, job syncJob) error {
	var err error
	switch job.kind {
	case syncDiscover:
		err = r.discoverAccount(ctx, job.accountID)
	case syncFolder:
		err = r.syncFolder(ctx, job)
	case syncBackfill:
		err = r.backfillFolder(ctx, job)
	default:
		err = errors.New("unsupported sync job")
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		status, code := "error", "sync_failed"
		if errors.Is(err, errOAuthTokenPersist) {
			status, code = "reauth_required", "oauth_token_persist_failed"
		} else if errors.Is(err, errOAuthRefresh) {
			status, code = "reauth_required", "oauth_refresh_failed"
		}
		_ = r.repository.UpdateAccountStatus(ctx, job.accountID, status, code)
		r.events.Publish(events.Event{Type: "account.status", ResourceID: job.accountID, State: status})
		r.logger.Warn("mail sync job failed", "account_id", job.accountID, "folder_id", job.folderID, "kind", job.kind, "error", err)
		return err
	}
	return nil
}

func (r *Runtime) discoverAccount(ctx context.Context, accountID string) error {
	_ = r.repository.UpdateAccountStatus(ctx, accountID, "syncing", "")
	r.events.Publish(events.Event{Type: "sync.progress", ResourceID: accountID, State: "discovering"})
	config, err := r.accountConfig(ctx, accountID)
	if err != nil {
		return err
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return fmt.Errorf("dial IMAP for discovery: %w", err)
	}
	defer session.Close()
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

func (r *Runtime) syncFolder(ctx context.Context, job syncJob) error {
	config, err := r.accountConfig(ctx, job.accountID)
	if err != nil {
		return err
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return fmt.Errorf("dial IMAP for folder sync: %w", err)
	}
	defer session.Close()
	checkpoint, err := r.ingest.Checkpoint(ctx, job.accountID, job.folderID)
	if err != nil {
		return err
	}
	pending, err := r.ingest.PendingOperationIDs(ctx, job.accountID)
	if err != nil {
		return err
	}
	_, err = r.synchronizer.Sync(ctx, session, r.ingest, mailSync.FolderSyncRequest{
		AccountID: job.accountID, FolderID: job.folderID, Mailbox: job.mailbox, Provider: config.Provider,
		Checkpoint: checkpoint, PendingOperationIDs: pending, Now: time.Now().UTC(),
	})
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

func (r *Runtime) backfillFolder(ctx context.Context, job syncJob) error {
	config, err := r.accountConfig(ctx, job.accountID)
	if err != nil {
		return err
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return fmt.Errorf("dial IMAP for history backfill: %w", err)
	}
	defer session.Close()
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
			r.scheduleInboxPolling(ctx)
		case <-reconcile.C:
			r.scheduleNonInbox(ctx)
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
			_ = r.enqueueSync(ctx, syncJob{accountID: account.ID, kind: syncDiscover})
			continue
		}
		if r.idleIsActive(account.ID) {
			continue
		}
		for _, folder := range folders {
			if folder.Role == "inbox" {
				_ = r.enqueueSync(ctx, syncJob{accountID: account.ID, folderID: folder.ID, mailbox: folder.RemoteName, kind: syncFolder})
				break
			}
		}
	}
	r.removeIdleWatchersNotIn(present)
}

func (r *Runtime) scheduleNonInbox(ctx context.Context) {
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
			if folder.Role != "inbox" {
				_ = r.enqueueSync(ctx, syncJob{accountID: account.ID, folderID: folder.ID, mailbox: folder.RemoteName, kind: syncFolder})
			}
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
	r.idleActive[accountID] = false
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
		caps, err := session.Capabilities(ctx)
		if err != nil || !caps.Idle {
			_ = session.Close()
			if err == nil {
				return
			}
			r.waitIdleRetry(ctx, &failures)
			continue
		}
		if _, err := session.Select(ctx, mailbox, true); err != nil {
			_ = session.Close()
			r.waitIdleRetry(ctx, &failures)
			continue
		}
		r.setIdleActive(accountID, token, true)
		failures = 0
		for ctx.Err() == nil {
			idleCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
			_, err := session.Idle(idleCtx)
			cancel()
			if err != nil {
				break
			}
			_ = r.enqueueSync(ctx, syncJob{accountID: accountID, folderID: folderID, mailbox: mailbox, kind: syncFolder})
		}
		r.setIdleActive(accountID, token, false)
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

func (r *Runtime) setIdleActive(accountID string, token uint64, active bool) {
	r.idleMu.Lock()
	defer r.idleMu.Unlock()
	if watcher, ok := r.idleWatchers[accountID]; ok && watcher.token == token {
		r.idleActive[accountID] = active
	}
}

func (r *Runtime) finishIdleWatcher(accountID string, token uint64) {
	r.idleMu.Lock()
	defer r.idleMu.Unlock()
	if watcher, ok := r.idleWatchers[accountID]; ok && watcher.token == token {
		delete(r.idleWatchers, accountID)
		delete(r.idleActive, accountID)
	}
}

func (r *Runtime) idleIsActive(accountID string) bool {
	r.idleMu.Lock()
	defer r.idleMu.Unlock()
	return r.idleActive[accountID]
}

func (r *Runtime) stopIdleWatcher(accountID string) {
	r.idleMu.Lock()
	watcher, ok := r.idleWatchers[accountID]
	if ok {
		delete(r.idleWatchers, accountID)
		delete(r.idleActive, accountID)
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
	clear(r.idleActive)
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
			delete(r.idleActive, accountID)
		}
	}
	r.idleMu.Unlock()
	for _, watcher := range stale {
		watcher.cancel()
	}
}

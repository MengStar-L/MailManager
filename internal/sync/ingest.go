package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
)

const (
	defaultIngestBatchSize = 250
	maximumInitialUIDs     = 100000
)

type IngestSession interface {
	Select(context.Context, string, bool) (connectors.MailboxState, error)
	SearchUIDs(context.Context, connectors.SearchRequest) ([]uint32, error)
	FetchMessages(context.Context, connectors.FetchRequest) ([]connectors.RemoteMessage, error)
	FetchMessageStates(context.Context, connectors.FetchRequest) ([]connectors.RemoteMessageState, error)
}

type FolderSyncRequest struct {
	AccountID           string
	FolderID            string
	Mailbox             string
	Provider            accounts.Provider
	Checkpoint          Checkpoint
	PendingOperationIDs []string
	Now                 time.Time
	// AllowDegradedBodies lets a failing per-message body fetch store the
	// message without a body instead of failing the folder. Callers set it on
	// retry attempts only, so a transient failure gets one clean retry before
	// a poison message is degraded permanently.
	AllowDegradedBodies bool
}

type MessageBatch struct {
	AccountID   string
	FolderID    string
	Provider    accounts.Provider
	UIDValidity uint32
	Messages    []connectors.RemoteMessage
	LowPriority bool
	RefreshBody bool
}

type FolderSnapshot struct {
	AccountID   string
	FolderID    string
	UIDValidity uint32
	// UIDNext is the SELECT-time UIDNEXT of the session that captured the
	// snapshot. Messages at or above it arrived after the capture and may
	// have been stored by a concurrent incremental sync, so applying the
	// snapshot must not treat their absence as a deletion.
	UIDNext    uint32
	RemoteUIDs []uint32
	States     []connectors.RemoteMessageState
}

// IngestSink is implemented by storage as an atomic boundary. PrepareFolder
// resets stale locations and marks pending operations when the decision is a
// rebuild. CompleteFolder persists the checkpoint only after all batches have
// been stored.
type IngestSink interface {
	PrepareFolder(context.Context, ReconcileDecision) error
	StoreMessages(context.Context, MessageBatch) error
	ReconcileFolder(context.Context, FolderSnapshot) error
	CompleteFolder(context.Context, Checkpoint) error
}

// RefreshCandidateSource reports stored messages whose metadata needs to be
// re-derived from the server (an earlier sync stored them with an empty
// envelope). Implemented by the ingest store; the refresh pass heals a
// bounded batch per sync instead of re-downloading whole folders.
type RefreshCandidateSource interface {
	EnvelopeHealCandidates(ctx context.Context, accountID, folderID string, uidValidity uint32, limit int) ([]uint32, error)
}

// envelopeHealLimit bounds how many damaged messages one sync re-fetches so
// a heal pass can never turn into a folder-length monolith that blocks new
// mail behind it.
const envelopeHealLimit = 200

type FolderSynchronizer struct {
	BatchSize        int
	MaxBodyPartBytes int64
}

type bodyPartFetcher interface {
	FetchPart(context.Context, uint32, []int, int64) ([]byte, error)
}

func (s FolderSynchronizer) Sync(ctx context.Context, session IngestSession, sink IngestSink, request FolderSyncRequest) (Checkpoint, error) {
	return s.sync(ctx, session, sink, request, true)
}

func (s FolderSynchronizer) SyncIncremental(ctx context.Context, session IngestSession, sink IngestSink, request FolderSyncRequest) (Checkpoint, error) {
	return s.sync(ctx, session, sink, request, false)
}

func (s FolderSynchronizer) sync(ctx context.Context, session IngestSession, sink IngestSink, request FolderSyncRequest, reconcile bool) (Checkpoint, error) {
	if session == nil || sink == nil {
		return Checkpoint{}, errors.New("IMAP session and ingest sink are required")
	}
	if request.AccountID == "" || request.FolderID == "" || request.Mailbox == "" {
		return Checkpoint{}, errors.New("account, folder, and mailbox are required")
	}
	if request.Checkpoint.AccountID != request.AccountID || request.Checkpoint.FolderID != request.FolderID {
		return Checkpoint{}, errors.New("checkpoint does not belong to the requested folder")
	}
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	state, err := session.Select(ctx, request.Mailbox, true)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("select mailbox for sync: %w", err)
	}
	decision, err := ReconcileCheckpoint(
		request.Checkpoint, state.UIDValidity, state.UIDNext,
		request.PendingOperationIDs, request.Now,
	)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := sink.PrepareFolder(ctx, decision); err != nil {
		return Checkpoint{}, fmt.Errorf("prepare folder sync: %w", err)
	}
	batchSize := s.BatchSize
	if batchSize <= 0 {
		batchSize = defaultIngestBatchSize
	}
	if batchSize > 1000 {
		return Checkpoint{}, errors.New("ingest batch size cannot exceed 1000")
	}

	incrementalProbe := false
	var highestFetched uint32
	if decision.Action == ReconcileInitialize || decision.Action == ReconcileRebuild {
		passes := InitialSyncPasses(request.Now)
		uids, err := session.SearchUIDs(ctx, connectors.SearchRequest{Since: passes[0].Since, Limit: maximumInitialUIDs})
		if err != nil {
			return Checkpoint{}, fmt.Errorf("search recent messages: %w", err)
		}
		highestFetched = maxUID(uids)
		if err := s.fetchExplicitBatches(ctx, session, sink, request, state.UIDValidity, uids, batchSize, false, false); err != nil {
			return Checkpoint{}, err
		}
	} else {
		// Probe for actual new UIDs instead of trusting the SELECT-reported
		// UIDNEXT, which some providers (163/QQ) report stale or omit. A
		// server answers n:* with its highest-UID message even when n exceeds
		// it, so already-covered UIDs must be discarded.
		incrementalProbe = true
		uids, err := session.SearchUIDs(ctx, connectors.SearchRequest{FromUID: decision.FetchFromUID, Limit: maximumInitialUIDs})
		if err != nil {
			return Checkpoint{}, fmt.Errorf("search new messages: %w", err)
		}
		fresh := make([]uint32, 0, len(uids))
		for _, uid := range uids {
			if uid >= decision.FetchFromUID {
				fresh = append(fresh, uid)
			}
		}
		highestFetched = maxUID(fresh)
		if err := s.fetchExplicitBatches(ctx, session, sink, request, state.UIDValidity, fresh, batchSize, false, false); err != nil {
			return Checkpoint{}, err
		}
	}
	// New mail is already stored; now heal a bounded batch of messages an
	// earlier sync stored with an empty envelope. Always best-effort: a
	// message whose header cannot be fetched must not fail the folder.
	refreshRemaining := false
	if request.Checkpoint.BodyRefreshRequired {
		if source, ok := sink.(RefreshCandidateSource); ok {
			candidates, err := source.EnvelopeHealCandidates(ctx, request.AccountID, request.FolderID, state.UIDValidity, envelopeHealLimit)
			if err != nil {
				return Checkpoint{}, fmt.Errorf("list envelope heal candidates: %w", err)
			}
			if len(candidates) > 0 {
				healRequest := request
				healRequest.AllowDegradedBodies = true
				if err := s.fetchExplicitBatches(ctx, session, sink, healRequest, state.UIDValidity, candidates, batchSize, false, true); err != nil {
					return Checkpoint{}, err
				}
			}
			// A full batch may mean more remain; keep the flag for the next
			// sync. Anything below the limit was healed in this pass.
			refreshRemaining = len(candidates) == envelopeHealLimit
		}
	}

	if reconcile {
		if err := s.reconcileFolder(ctx, session, sink, request, state, batchSize); err != nil {
			return Checkpoint{}, err
		}
	}

	next := decision.Checkpoint
	next.BodyRefreshRequired = refreshRemaining
	if incrementalProbe {
		// Advance only over UIDs that were actually stored so a message the
		// server exposes late is fetched by a later probe instead of being
		// skipped forever.
		next.LastUID = request.Checkpoint.LastUID
	} else if state.UIDNext > 0 {
		next.LastUID = state.UIDNext - 1
	}
	if highestFetched > next.LastUID {
		next.LastUID = highestFetched
	}
	next.HighestModSeq = state.HighestModSeq
	next.UpdatedAt = request.Now.UTC()
	if err := sink.CompleteFolder(ctx, next); err != nil {
		return Checkpoint{}, fmt.Errorf("complete folder sync: %w", err)
	}
	return next, nil
}

func maxUID(uids []uint32) uint32 {
	var maximum uint32
	for _, uid := range uids {
		if uid > maximum {
			maximum = uid
		}
	}
	return maximum
}

func (s FolderSynchronizer) BackfillHistory(ctx context.Context, session IngestSession, sink IngestSink, request FolderSyncRequest) error {
	if session == nil || sink == nil {
		return errors.New("IMAP session and ingest sink are required")
	}
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	state, err := session.Select(ctx, request.Mailbox, true)
	if err != nil {
		return fmt.Errorf("select mailbox for history backfill: %w", err)
	}
	if state.UIDValidity != request.Checkpoint.UIDValidity {
		return errors.New("UIDVALIDITY changed before history backfill")
	}
	passes := InitialSyncPasses(request.Now)
	uids, err := session.SearchUIDs(ctx, connectors.SearchRequest{Before: passes[1].Before, Limit: maximumInitialUIDs})
	if err != nil {
		return fmt.Errorf("search historical messages: %w", err)
	}
	batchSize := s.BatchSize
	if batchSize <= 0 {
		batchSize = defaultIngestBatchSize
	}
	if batchSize > 1000 {
		return errors.New("ingest batch size cannot exceed 1000")
	}
	return s.fetchExplicitBatches(ctx, session, sink, request, state.UIDValidity, uids, batchSize, true, false)
}

func (s FolderSynchronizer) fetchExplicitBatches(
	ctx context.Context,
	session IngestSession,
	sink IngestSink,
	request FolderSyncRequest,
	uidValidity uint32,
	uids []uint32,
	batchSize int,
	lowPriority bool,
	refreshBody bool,
) error {
	for start := 0; start < len(uids); start += batchSize {
		end := start + batchSize
		if end > len(uids) {
			end = len(uids)
		}
		messages, err := session.FetchMessages(ctx, connectors.FetchRequest{UIDs: uids[start:end], Limit: batchSize})
		if err != nil {
			return fmt.Errorf("fetch IMAP message batch: %w", err)
		}
		if err := s.storeFetchedMessages(ctx, session, sink, request, uidValidity, messages, lowPriority, refreshBody); err != nil {
			return err
		}
	}
	return nil
}

// maxConsecutiveBodyFailures separates a malformed message (skip its body,
// keep syncing) from a dead connection (every fetch fails; abort).
const maxConsecutiveBodyFailures = 3

func (s FolderSynchronizer) storeFetchedMessages(
	ctx context.Context,
	session IngestSession,
	sink IngestSink,
	request FolderSyncRequest,
	uidValidity uint32,
	messages []connectors.RemoteMessage,
	lowPriority bool,
	refreshBody bool,
) error {
	consecutiveBodyFailures := 0
	for index := range messages {
		degraded := false
		if err := s.fetchTextBody(ctx, session, &messages[index]); err != nil {
			if ctx.Err() != nil {
				messages[index] = connectors.RemoteMessage{}
				return fmt.Errorf("fetch IMAP text bodies: %w", ctx.Err())
			}
			if !request.AllowDegradedBodies {
				messages[index] = connectors.RemoteMessage{}
				return fmt.Errorf("fetch IMAP text bodies: %w", err)
			}
			consecutiveBodyFailures++
			if consecutiveBodyFailures >= maxConsecutiveBodyFailures {
				messages[index] = connectors.RemoteMessage{}
				return fmt.Errorf("fetch IMAP text bodies: %w", err)
			}
			// A poison message must not wedge the whole folder in a permanent
			// retry loop; store it without a body.
			degraded = true
			messages[index].TextBody, messages[index].HTMLBody = "", ""
			messages[index].RemoteImagesBlocked = false
		} else {
			consecutiveBodyFailures = 0
		}
		err := sink.StoreMessages(ctx, MessageBatch{
			AccountID: request.AccountID, FolderID: request.FolderID, Provider: request.Provider, UIDValidity: uidValidity,
			Messages: messages[index : index+1], LowPriority: lowPriority,
			// A degraded message must never overwrite a previously stored
			// body under refresh semantics.
			RefreshBody: refreshBody && !degraded,
		})
		messages[index] = connectors.RemoteMessage{}
		if err != nil {
			return fmt.Errorf("store IMAP message: %w", err)
		}
	}
	return nil
}

func (s FolderSynchronizer) fetchTextBody(ctx context.Context, session IngestSession, message *connectors.RemoteMessage) error {
	fetcher, ok := session.(bodyPartFetcher)
	if !ok {
		return nil
	}
	maximum := s.MaxBodyPartBytes
	if maximum <= 0 {
		maximum = 10 << 20
	}
	plainFetched := message.TextBody != ""
	htmlFetched := message.HTMLBody != ""
	for _, part := range message.Parts {
		if strings.EqualFold(part.Disposition, "attachment") || int64(part.Size) > maximum {
			continue
		}
		mediaType := strings.ToLower(part.MediaType)
		switch mediaType {
		case "text/plain":
			if plainFetched {
				continue
			}
		case "text/html":
			if htmlFetched {
				continue
			}
		default:
			continue
		}
		body, err := fetcher.FetchPart(ctx, message.UID, part.Path, maximum)
		if err != nil {
			return err
		}
		if mediaType == "text/plain" {
			message.TextBody = string(body)
			plainFetched = true
		} else {
			clean, err := connectors.SanitizeHTML(string(body), false)
			if err != nil {
				return err
			}
			message.HTMLBody = clean.HTML
			message.RemoteImagesBlocked = clean.RemoteImagesRemoved
			htmlFetched = true
		}
		if plainFetched && htmlFetched {
			break
		}
	}
	return nil
}

func (s FolderSynchronizer) reconcileFolder(
	ctx context.Context,
	session IngestSession,
	sink IngestSink,
	request FolderSyncRequest,
	state connectors.MailboxState,
	batchSize int,
) error {
	uids, err := session.SearchUIDs(ctx, connectors.SearchRequest{Limit: maximumInitialUIDs})
	if errors.Is(err, connectors.ErrSearchOverflow) {
		// Reconciling against a truncated snapshot would read as mass
		// deletion; skipping the pass is the only safe degradation for a
		// folder beyond the enumeration limit.
		return nil
	}
	if err != nil {
		return fmt.Errorf("search all folder messages: %w", err)
	}
	states := make([]connectors.RemoteMessageState, 0, len(uids))
	for start := 0; start < len(uids); start += batchSize {
		end := start + batchSize
		if end > len(uids) {
			end = len(uids)
		}
		batch, err := session.FetchMessageStates(ctx, connectors.FetchRequest{
			UIDs: uids[start:end], Limit: end - start,
		})
		if err != nil {
			return fmt.Errorf("fetch IMAP message states: %w", err)
		}
		states = append(states, batch...)
	}
	if err := sink.ReconcileFolder(ctx, FolderSnapshot{
		AccountID: request.AccountID, FolderID: request.FolderID, UIDValidity: state.UIDValidity,
		UIDNext: state.UIDNext, RemoteUIDs: uids, States: states,
	}); err != nil {
		return fmt.Errorf("reconcile folder state: %w", err)
	}
	return nil
}

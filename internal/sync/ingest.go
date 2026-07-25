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
	RemoteUIDs  []uint32
	States      []connectors.RemoteMessageState
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

	if request.Checkpoint.BodyRefreshRequired {
		uids, err := session.SearchUIDs(ctx, connectors.SearchRequest{Limit: maximumInitialUIDs})
		if err != nil {
			return Checkpoint{}, fmt.Errorf("search messages for body refresh: %w", err)
		}
		if err := s.fetchExplicitBatches(ctx, session, sink, request, state.UIDValidity, uids, batchSize, false); err != nil {
			return Checkpoint{}, err
		}
	} else if decision.Action == ReconcileInitialize || decision.Action == ReconcileRebuild {
		passes := InitialSyncPasses(request.Now)
		uids, err := session.SearchUIDs(ctx, connectors.SearchRequest{Since: passes[0].Since, Limit: maximumInitialUIDs})
		if err != nil {
			return Checkpoint{}, fmt.Errorf("search recent messages: %w", err)
		}
		if err := s.fetchExplicitBatches(ctx, session, sink, request, state.UIDValidity, uids, batchSize, false); err != nil {
			return Checkpoint{}, err
		}
	} else if state.UIDNext > 1 && decision.FetchFromUID < state.UIDNext {
		if err := s.fetchRangeBatches(ctx, session, sink, request, state.UIDValidity, decision.FetchFromUID, state.UIDNext-1, batchSize); err != nil {
			return Checkpoint{}, err
		}
	}
	if reconcile {
		if err := s.reconcileFolder(ctx, session, sink, request, state.UIDValidity, batchSize); err != nil {
			return Checkpoint{}, err
		}
	}

	next := decision.Checkpoint
	next.BodyRefreshRequired = false
	if state.UIDNext > 0 {
		next.LastUID = state.UIDNext - 1
	}
	next.HighestModSeq = state.HighestModSeq
	next.UpdatedAt = request.Now.UTC()
	if err := sink.CompleteFolder(ctx, next); err != nil {
		return Checkpoint{}, fmt.Errorf("complete folder sync: %w", err)
	}
	return next, nil
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
	return s.fetchExplicitBatches(ctx, session, sink, request, state.UIDValidity, uids, batchSize, true)
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
		if err := s.storeFetchedMessages(ctx, session, sink, request, uidValidity, messages, lowPriority); err != nil {
			return err
		}
	}
	return nil
}

func (s FolderSynchronizer) fetchRangeBatches(
	ctx context.Context,
	session IngestSession,
	sink IngestSink,
	request FolderSyncRequest,
	uidValidity, from, through uint32,
	batchSize int,
) error {
	for start := from; start <= through; {
		end64 := uint64(start) + uint64(batchSize) - 1
		if end64 > uint64(through) {
			end64 = uint64(through)
		}
		end := uint32(end64)
		messages, err := session.FetchMessages(ctx, connectors.FetchRequest{FromUID: start, ThroughUID: end, Limit: batchSize})
		if err != nil {
			return fmt.Errorf("fetch incremental IMAP batch: %w", err)
		}
		if err := s.storeFetchedMessages(ctx, session, sink, request, uidValidity, messages, false); err != nil {
			return err
		}
		if end == through {
			break
		}
		start = end + 1
	}
	return nil
}

func (s FolderSynchronizer) storeFetchedMessages(
	ctx context.Context,
	session IngestSession,
	sink IngestSink,
	request FolderSyncRequest,
	uidValidity uint32,
	messages []connectors.RemoteMessage,
	lowPriority bool,
) error {
	for index := range messages {
		if err := s.fetchTextBody(ctx, session, &messages[index]); err != nil {
			messages[index] = connectors.RemoteMessage{}
			return fmt.Errorf("fetch IMAP text bodies: %w", err)
		}
		err := sink.StoreMessages(ctx, MessageBatch{
			AccountID: request.AccountID, FolderID: request.FolderID, Provider: request.Provider, UIDValidity: uidValidity,
			Messages: messages[index : index+1], LowPriority: lowPriority,
			RefreshBody: request.Checkpoint.BodyRefreshRequired,
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
	uidValidity uint32,
	batchSize int,
) error {
	uids, err := session.SearchUIDs(ctx, connectors.SearchRequest{Limit: maximumInitialUIDs})
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
		AccountID: request.AccountID, FolderID: request.FolderID, UIDValidity: uidValidity,
		RemoteUIDs: uids, States: states,
	}); err != nil {
		return fmt.Errorf("reconcile folder state: %w", err)
	}
	return nil
}

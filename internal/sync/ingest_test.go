package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
)

type fakeIngestSession struct {
	state          connectors.MailboxState
	searchUIDs     []uint32
	allUIDs        []uint32
	messages       map[uint32]connectors.RemoteMessage
	messageStates  map[uint32]connectors.RemoteMessageState
	partBodies     map[int][]byte
	partFailures   map[uint32]error
	searchOverflow bool
	partRequests   [][]int
	searchRequests []connectors.SearchRequest
	fetchRequests  []connectors.FetchRequest
	stateRequests  []connectors.FetchRequest
}

func (s *fakeIngestSession) Select(context.Context, string, bool) (connectors.MailboxState, error) {
	return s.state, nil
}

func (s *fakeIngestSession) SearchUIDs(_ context.Context, request connectors.SearchRequest) ([]uint32, error) {
	s.searchRequests = append(s.searchRequests, request)
	if request.FromUID != 0 {
		result := make([]uint32, 0, len(s.allUIDs))
		for _, uid := range s.allUIDs {
			if uid >= request.FromUID {
				result = append(result, uid)
			}
		}
		// Servers answer n:* with the highest existing UID even when n
		// exceeds it.
		if len(result) == 0 && len(s.allUIDs) > 0 {
			result = append(result, s.allUIDs[len(s.allUIDs)-1])
		}
		return result, nil
	}
	if request.Since.IsZero() && request.Before.IsZero() {
		if s.searchOverflow {
			return nil, connectors.ErrSearchOverflow
		}
		return append([]uint32(nil), s.allUIDs...), nil
	}
	return append([]uint32(nil), s.searchUIDs...), nil
}

func (s *fakeIngestSession) FetchMessages(_ context.Context, request connectors.FetchRequest) ([]connectors.RemoteMessage, error) {
	s.fetchRequests = append(s.fetchRequests, request)
	var uids []uint32
	if len(request.UIDs) > 0 {
		uids = request.UIDs
	} else {
		for uid := request.FromUID; uid <= request.ThroughUID; uid++ {
			uids = append(uids, uid)
		}
	}
	messages := make([]connectors.RemoteMessage, len(uids))
	for index, uid := range uids {
		messages[index] = s.messages[uid]
		messages[index].UID = uid
	}
	return messages, nil
}

func (s *fakeIngestSession) FetchPart(_ context.Context, uid uint32, path []int, _ int64) ([]byte, error) {
	s.partRequests = append(s.partRequests, append([]int(nil), path...))
	if err := s.partFailures[uid]; err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, nil
	}
	return append([]byte(nil), s.partBodies[path[0]]...), nil
}

func (s *fakeIngestSession) FetchMessageStates(_ context.Context, request connectors.FetchRequest) ([]connectors.RemoteMessageState, error) {
	s.stateRequests = append(s.stateRequests, request)
	states := make([]connectors.RemoteMessageState, 0, len(request.UIDs))
	for _, uid := range request.UIDs {
		state, ok := s.messageStates[uid]
		if !ok {
			state = connectors.RemoteMessageState{UID: uid}
		}
		states = append(states, state)
	}
	return states, nil
}

type fakeIngestSink struct {
	decision        ReconcileDecision
	batches         []MessageBatch
	storedMessages  []connectors.RemoteMessage
	reconciliations []FolderSnapshot
	checkpoint      Checkpoint
}

func (s *fakeIngestSink) PrepareFolder(_ context.Context, decision ReconcileDecision) error {
	s.decision = decision
	return nil
}

func (s *fakeIngestSink) StoreMessages(_ context.Context, batch MessageBatch) error {
	s.storedMessages = append(s.storedMessages, batch.Messages...)
	s.batches = append(s.batches, batch)
	return nil
}

func (s *fakeIngestSink) ReconcileFolder(_ context.Context, snapshot FolderSnapshot) error {
	s.reconciliations = append(s.reconciliations, snapshot)
	return nil
}

func (s *fakeIngestSink) CompleteFolder(_ context.Context, checkpoint Checkpoint) error {
	s.checkpoint = checkpoint
	return nil
}

func TestFolderSynchronizerInitialRecentThenHistory(t *testing.T) {
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	session := &fakeIngestSession{
		state:      connectors.MailboxState{UIDValidity: 9, UIDNext: 21, HighestModSeq: 55},
		searchUIDs: []uint32{11, 12, 18},
		allUIDs:    []uint32{1, 2, 11, 12, 18},
	}
	sink := &fakeIngestSink{}
	request := FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX", Provider: accounts.ProviderGoogle, Now: now,
		Checkpoint: Checkpoint{AccountID: "account", FolderID: "folder"},
	}
	next, err := (FolderSynchronizer{BatchSize: 2}).Sync(context.Background(), session, sink, request)
	if err != nil {
		t.Fatal(err)
	}
	if sink.decision.Action != ReconcileInitialize || len(session.searchRequests) != 2 {
		t.Fatalf("initial sync did not search recent mail: decision=%#v searches=%#v", sink.decision, session.searchRequests)
	}
	if !session.searchRequests[0].Since.Equal(now.Add(-InitialSyncWindow)) {
		t.Fatalf("initial window = %s", session.searchRequests[0].Since)
	}
	if len(sink.batches) != 3 {
		t.Fatalf("recent messages were not stored one at a time: %#v", sink.batches)
	}
	for _, batch := range sink.batches {
		if batch.Provider != accounts.ProviderGoogle {
			t.Fatalf("message batch provider = %q", batch.Provider)
		}
		if len(batch.Messages) != 1 {
			t.Fatalf("body-bearing batch retained %d messages", len(batch.Messages))
		}
		if batch.Messages[0].UID != 0 {
			t.Fatalf("stored message reference was not cleared: %#v", batch.Messages[0])
		}
	}
	if len(sink.reconciliations) != 1 || len(sink.reconciliations[0].RemoteUIDs) != len(session.allUIDs) {
		t.Fatalf("folder state was not reconciled: %#v", sink.reconciliations)
	}
	if next.LastUID != 20 || next.HighestModSeq != 55 || sink.checkpoint != next {
		t.Fatalf("checkpoint not completed: next=%#v stored=%#v", next, sink.checkpoint)
	}

	session.searchUIDs = []uint32{1, 2}
	sink.batches = nil
	request.Checkpoint = next
	if err := (FolderSynchronizer{BatchSize: 2}).BackfillHistory(context.Background(), session, sink, request); err != nil {
		t.Fatal(err)
	}
	if len(sink.batches) != 2 || !sink.batches[0].LowPriority || session.searchRequests[2].Before.IsZero() {
		t.Fatalf("history was not stored at low priority: %#v", sink.batches)
	}
}

func TestFolderSynchronizerFetchesOnlyFirstPlainAndHTMLBodies(t *testing.T) {
	session := &fakeIngestSession{
		state:      connectors.MailboxState{UIDValidity: 9, UIDNext: 2},
		searchUIDs: []uint32{1}, allUIDs: []uint32{1},
		messages: map[uint32]connectors.RemoteMessage{1: {Parts: []connectors.RemotePart{
			{Path: []int{1}, MediaType: "text/plain", Size: 10},
			{Path: []int{2}, MediaType: "text/plain", Size: 10},
			{Path: []int{3}, MediaType: "text/html", Size: 10},
			{Path: []int{4}, MediaType: "text/html", Size: 10},
		}}},
		partBodies: map[int][]byte{
			1: []byte("plain-first"), 2: []byte("plain-second"),
			3: []byte("<p>html-first</p>"), 4: []byte("<p>html-second</p>"),
		},
	}
	sink := &fakeIngestSink{}
	request := FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX", Now: time.Now().UTC(),
		Checkpoint: Checkpoint{AccountID: "account", FolderID: "folder"},
	}
	if _, err := (FolderSynchronizer{BatchSize: 10}).Sync(context.Background(), session, sink, request); err != nil {
		t.Fatal(err)
	}
	if len(session.partRequests) != 2 || session.partRequests[0][0] != 1 || session.partRequests[1][0] != 3 {
		t.Fatalf("unexpected body part requests: %#v", session.partRequests)
	}
	if len(sink.storedMessages) != 1 || sink.storedMessages[0].TextBody != "plain-first" || sink.storedMessages[0].HTMLBody == "" {
		t.Fatalf("unexpected stored bodies: %#v", sink.storedMessages)
	}
	if sink.batches[0].Messages[0].UID != 0 || sink.batches[0].Messages[0].TextBody != "" {
		t.Fatalf("body-bearing source message was retained: %#v", sink.batches[0].Messages[0])
	}
}

func TestFolderSynchronizerIncrementalAndUIDValidityChange(t *testing.T) {
	now := time.Now().UTC()
	checkpoint := Checkpoint{AccountID: "account", FolderID: "folder", UIDValidity: 4, UIDNext: 19, LastUID: 18}
	session := &fakeIngestSession{
		state:   connectors.MailboxState{UIDValidity: 4, UIDNext: 21},
		allUIDs: []uint32{19, 20},
	}
	sink := &fakeIngestSink{}
	request := FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX", Now: now, Checkpoint: checkpoint,
	}
	if _, err := (FolderSynchronizer{BatchSize: 10}).Sync(context.Background(), session, sink, request); err != nil {
		t.Fatal(err)
	}
	if len(session.fetchRequests) != 1 || len(session.fetchRequests[0].UIDs) != 2 ||
		session.fetchRequests[0].UIDs[0] != 19 || session.fetchRequests[0].UIDs[1] != 20 {
		t.Fatalf("unexpected incremental request: %#v", session.fetchRequests)
	}

	session.state = connectors.MailboxState{UIDValidity: 5, UIDNext: 2}
	session.searchUIDs = nil
	sink = &fakeIngestSink{}
	request.PendingOperationIDs = []string{"op-1"}
	if _, err := (FolderSynchronizer{}).Sync(context.Background(), session, sink, request); err != nil {
		t.Fatal(err)
	}
	if sink.decision.Action != ReconcileRebuild || len(sink.decision.OperationsNeedingAttention) != 1 {
		t.Fatalf("UIDVALIDITY rebuild was not propagated: %#v", sink.decision)
	}
}

func TestFolderSynchronizerIncrementalSkipsFullReconciliation(t *testing.T) {
	checkpoint := Checkpoint{AccountID: "account", FolderID: "folder", UIDValidity: 4, UIDNext: 19, LastUID: 18}
	session := &fakeIngestSession{
		state:    connectors.MailboxState{UIDValidity: 4, UIDNext: 20},
		allUIDs:  []uint32{19},
		messages: map[uint32]connectors.RemoteMessage{19: {UID: 19, TextBody: "new"}},
	}
	sink := &fakeIngestSink{}
	request := FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX",
		Checkpoint: checkpoint, Now: time.Now().UTC(),
	}
	next, err := (FolderSynchronizer{BatchSize: 10}).SyncIncremental(context.Background(), session, sink, request)
	if err != nil {
		t.Fatal(err)
	}
	if next.LastUID != 19 || len(sink.storedMessages) != 1 {
		t.Fatalf("incremental result = %#v, stored = %d", next, len(sink.storedMessages))
	}
	if len(session.searchRequests) != 1 || session.searchRequests[0].FromUID != 19 {
		t.Fatalf("incremental sync did not probe for new UIDs: %#v", session.searchRequests)
	}
	if len(session.stateRequests) != 0 || len(sink.reconciliations) != 0 {
		t.Fatal("fast sync performed full folder reconciliation")
	}
}

func TestFolderSynchronizerRefreshesBodiesByExplicitUID(t *testing.T) {
	const lastUID = uint32(1_767_690_355)
	checkpoint := Checkpoint{
		AccountID: "account", FolderID: "folder", UIDValidity: 4,
		UIDNext: lastUID + 1, LastUID: lastUID, BodyRefreshRequired: true,
	}
	session := &fakeIngestSession{
		state:   connectors.MailboxState{UIDValidity: 4, UIDNext: lastUID + 1},
		allUIDs: []uint32{7, lastUID},
		messages: map[uint32]connectors.RemoteMessage{
			7:       {TextBody: "old"},
			lastUID: {TextBody: "new"},
		},
	}
	sink := &fakeIngestSink{}
	next, err := (FolderSynchronizer{BatchSize: 1}).Sync(context.Background(), session, sink, FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX",
		Checkpoint: checkpoint, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.fetchRequests) != 2 {
		t.Fatalf("body refresh requests = %#v", session.fetchRequests)
	}
	for _, request := range session.fetchRequests {
		if len(request.UIDs) != 1 || request.FromUID != 0 || request.ThroughUID != 0 {
			t.Fatalf("body refresh used a UID range: %#v", request)
		}
	}
	for _, batch := range sink.batches {
		if !batch.RefreshBody {
			t.Fatalf("body refresh batch was not marked: %#v", batch)
		}
	}
	if next.BodyRefreshRequired || sink.checkpoint.BodyRefreshRequired {
		t.Fatalf("body refresh flag was not cleared after success: next=%#v stored=%#v", next, sink.checkpoint)
	}
}

func TestFolderSynchronizerReconcilesFlagsAndExpungesWithoutUIDNextChange(t *testing.T) {
	checkpoint := Checkpoint{
		AccountID: "account", FolderID: "folder", UIDValidity: 4,
		UIDNext: 7, LastUID: 6, HighestModSeq: 8,
	}
	session := &fakeIngestSession{
		state:   connectors.MailboxState{UIDValidity: 4, UIDNext: 7, HighestModSeq: 11},
		allUIDs: []uint32{5},
		messageStates: map[uint32]connectors.RemoteMessageState{
			5: {UID: 5, Flags: []string{`\Seen`}, ModSeq: 11},
		},
	}
	sink := &fakeIngestSink{}
	request := FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX",
		Checkpoint: checkpoint, Now: time.Now().UTC(),
	}

	if _, err := (FolderSynchronizer{BatchSize: 10}).Sync(context.Background(), session, sink, request); err != nil {
		t.Fatal(err)
	}
	if len(session.fetchRequests) != 0 {
		t.Fatalf("UIDNext did not change but message metadata was fetched: %#v", session.fetchRequests)
	}
	if len(session.stateRequests) != 1 || len(session.stateRequests[0].UIDs) != 1 || session.stateRequests[0].UIDs[0] != 5 {
		t.Fatalf("unexpected state fetches: %#v", session.stateRequests)
	}
	if len(sink.reconciliations) != 1 {
		t.Fatalf("folder reconciliation count = %d", len(sink.reconciliations))
	}
	snapshot := sink.reconciliations[0]
	if len(snapshot.RemoteUIDs) != 1 || snapshot.RemoteUIDs[0] != 5 || len(snapshot.States) != 1 || snapshot.States[0].ModSeq != 11 {
		t.Fatalf("unexpected folder snapshot: %#v", snapshot)
	}
}

func TestFolderSynchronizerSkipsBodyOfPoisonMessageWithoutFailingFolder(t *testing.T) {
	parts := []connectors.RemotePart{{Path: []int{1}, MediaType: "text/plain", Size: 10}}
	checkpoint := Checkpoint{AccountID: "account", FolderID: "folder", UIDValidity: 4, UIDNext: 19, LastUID: 18}
	session := &fakeIngestSession{
		state:   connectors.MailboxState{UIDValidity: 4, UIDNext: 22},
		allUIDs: []uint32{19, 20, 21},
		messages: map[uint32]connectors.RemoteMessage{
			19: {Parts: parts}, 20: {Parts: parts}, 21: {Parts: parts},
		},
		partBodies:   map[int][]byte{1: []byte("body")},
		partFailures: map[uint32]error{20: errors.New("malformed message")},
	}
	sink := &fakeIngestSink{}
	strictRequest := FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX",
		Checkpoint: checkpoint, Now: time.Now().UTC(),
	}
	if _, err := (FolderSynchronizer{BatchSize: 10}).SyncIncremental(context.Background(), session, sink, strictRequest); err == nil {
		t.Fatal("first attempt must fail so a transient error gets a clean retry")
	}

	session.partRequests, sink.storedMessages, sink.batches = nil, nil, nil
	lenientRequest := strictRequest
	lenientRequest.AllowDegradedBodies = true
	next, err := (FolderSynchronizer{BatchSize: 10}).SyncIncremental(context.Background(), session, sink, lenientRequest)
	if err != nil {
		t.Fatalf("a single poison message failed the folder on retry: %v", err)
	}
	if next.LastUID != 21 || len(sink.storedMessages) != 3 {
		t.Fatalf("degraded sync result = %#v, stored = %d", next, len(sink.storedMessages))
	}
	bodies := map[uint32]string{}
	for index, batch := range sink.batches {
		bodies[uint32(19+index)] = sink.storedMessages[index].TextBody
		_ = batch
	}
	if bodies[19] != "body" || bodies[20] != "" || bodies[21] != "body" {
		t.Fatalf("unexpected stored bodies: %#v", bodies)
	}
}

func TestFolderSynchronizerFailsWhenEveryBodyFetchFails(t *testing.T) {
	parts := []connectors.RemotePart{{Path: []int{1}, MediaType: "text/plain", Size: 10}}
	checkpoint := Checkpoint{AccountID: "account", FolderID: "folder", UIDValidity: 4, UIDNext: 19, LastUID: 18}
	failure := errors.New("connection reset")
	session := &fakeIngestSession{
		state:   connectors.MailboxState{UIDValidity: 4, UIDNext: 23},
		allUIDs: []uint32{19, 20, 21, 22},
		messages: map[uint32]connectors.RemoteMessage{
			19: {Parts: parts}, 20: {Parts: parts}, 21: {Parts: parts}, 22: {Parts: parts},
		},
		partFailures: map[uint32]error{19: failure, 20: failure, 21: failure, 22: failure},
	}
	sink := &fakeIngestSink{}
	_, err := (FolderSynchronizer{BatchSize: 10}).SyncIncremental(context.Background(), session, sink, FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX",
		Checkpoint: checkpoint, Now: time.Now().UTC(), AllowDegradedBodies: true,
	})
	if err == nil {
		t.Fatal("consecutive body-fetch failures were not treated as a connection problem")
	}
	if len(sink.storedMessages) != maxConsecutiveBodyFailures-1 {
		t.Fatalf("stored %d degraded messages before aborting, want %d", len(sink.storedMessages), maxConsecutiveBodyFailures-1)
	}
}

func TestFolderSynchronizerDegradedRefreshDoesNotOverwriteStoredBodies(t *testing.T) {
	parts := []connectors.RemotePart{{Path: []int{1}, MediaType: "text/plain", Size: 10}}
	checkpoint := Checkpoint{
		AccountID: "account", FolderID: "folder", UIDValidity: 4,
		UIDNext: 21, LastUID: 20, BodyRefreshRequired: true,
	}
	session := &fakeIngestSession{
		state:        connectors.MailboxState{UIDValidity: 4, UIDNext: 21},
		allUIDs:      []uint32{19, 20},
		messages:     map[uint32]connectors.RemoteMessage{19: {Parts: parts}, 20: {Parts: parts}},
		partBodies:   map[int][]byte{1: []byte("fresh body")},
		partFailures: map[uint32]error{20: errors.New("transient NO")},
	}
	sink := &fakeIngestSink{}
	if _, err := (FolderSynchronizer{BatchSize: 10}).Sync(context.Background(), session, sink, FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX",
		Checkpoint: checkpoint, Now: time.Now().UTC(), AllowDegradedBodies: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.batches) != 2 {
		t.Fatalf("stored %d messages during refresh, want 2", len(sink.batches))
	}
	if !sink.batches[0].RefreshBody {
		t.Fatal("healthy message lost its refresh semantics")
	}
	if sink.batches[1].RefreshBody {
		t.Fatal("degraded message kept refresh semantics and would wipe its stored body")
	}
}

func TestFolderSynchronizerRefreshOverflowFallsBackToIncrementalProbe(t *testing.T) {
	checkpoint := Checkpoint{
		AccountID: "account", FolderID: "folder", UIDValidity: 4,
		UIDNext: 19, LastUID: 18, BodyRefreshRequired: true,
	}
	session := &fakeIngestSession{
		state:          connectors.MailboxState{UIDValidity: 4, UIDNext: 40},
		allUIDs:        []uint32{19, 20},
		messages:       map[uint32]connectors.RemoteMessage{19: {TextBody: "new"}, 20: {TextBody: "newer"}},
		searchOverflow: true,
	}
	sink := &fakeIngestSink{}
	next, err := (FolderSynchronizer{BatchSize: 10}).SyncIncremental(context.Background(), session, sink, FolderSyncRequest{
		AccountID: "account", FolderID: "folder", Mailbox: "INBOX",
		Checkpoint: checkpoint, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("refresh overflow failed the sync: %v", err)
	}
	if len(sink.storedMessages) != 2 {
		t.Fatalf("overflow fallback stored %d messages, want the 2 new ones", len(sink.storedMessages))
	}
	if next.LastUID != 20 {
		t.Fatalf("LastUID = %d; must advance only over fetched UIDs, not to UIDNEXT-1=39", next.LastUID)
	}
	if next.BodyRefreshRequired {
		t.Fatal("refresh flag must clear so the oversized folder does not loop")
	}
}

package mailstore

import (
	"errors"
	"testing"
	"time"
)

func TestGroupThreadsIsAccountScoped(t *testing.T) {
	messages := []ThreadMessage{
		{LocalID: "a1", AccountID: "account-a", MessageID: "<root@example.test>"},
		{LocalID: "a2", AccountID: "account-a", MessageID: "reply@example.test", InReplyTo: []string{"<root@example.test>"}},
		{LocalID: "b1", AccountID: "account-b", MessageID: "root@example.test"},
		{LocalID: "b2", AccountID: "account-b", MessageID: "standalone@example.test"},
	}
	groups, err := GroupThreads(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3: %#v", len(groups), groups)
	}
	if groups[0].AccountID != "account-a" || len(groups[0].LocalIDs) != 2 {
		t.Fatalf("account A thread not grouped: %#v", groups[0])
	}
	for _, group := range groups {
		if group.AccountID == "account-b" && len(group.LocalIDs) > 1 {
			t.Fatalf("cross-account Message-ID caused a merge: %#v", group)
		}
	}
}

func TestOperationUndoAndPermanentDeleteConfirmation(t *testing.T) {
	now := time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC)
	operation, err := NewOperation("op-1", OperationArchive, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Ready(now) || !operation.Ready(now.Add(DefaultUndoWindow)) {
		t.Fatal("archive did not honor its undo window")
	}
	if err := operation.Undo(now.Add(time.Second)); err != nil || operation.Status != OperationUndone {
		t.Fatalf("undo failed: %v, %s", err, operation.Status)
	}
	if _, err := NewOperation("op-2", OperationPermanentDelete, now, false); err == nil {
		t.Fatal("unconfirmed permanent delete was accepted")
	}
}

func TestOperationFailureRetryLifecycle(t *testing.T) {
	now := time.Now().UTC()
	operation, err := NewOperation("op", OperationMarkRead, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.Start(now); err != nil {
		t.Fatal(err)
	}
	if err := operation.Fail(now, errors.New("server rejected STORE")); err != nil {
		t.Fatal(err)
	}
	if err := operation.Retry(now.Add(time.Second)); err != nil || operation.Status != OperationScheduled {
		t.Fatalf("retry failed: %v, %s", err, operation.Status)
	}
}

func TestOutboxUnknownCannotAutomaticallyRetry(t *testing.T) {
	now := time.Now().UTC()
	item, err := NewOutboxItem("outbox-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := item.Start(now); err != nil {
		t.Fatal(err)
	}
	if err := item.MarkUnknown(now, errors.New("connection lost after DATA")); err != nil {
		t.Fatal(err)
	}
	if err := item.Retry(now); err == nil {
		t.Fatal("unknown delivery was allowed to retry automatically")
	}
	if err := item.MarkSent(now); err != nil || item.Status != OutboxSent {
		t.Fatalf("manual unknown resolution failed: %v, %s", err, item.Status)
	}
}

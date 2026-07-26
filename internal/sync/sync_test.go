package sync

import (
	"context"
	"testing"
	"time"

	"mailmanager/internal/accounts"
)

func TestUIDValidityChangeRequiresRebuild(t *testing.T) {
	now := time.Date(2026, time.July, 11, 1, 0, 0, 0, time.UTC)
	checkpoint := Checkpoint{AccountID: "account", FolderID: "inbox", UIDValidity: 7, UIDNext: 50, LastUID: 48}
	decision, err := ReconcileCheckpoint(checkpoint, 8, 3, []string{"op-1", "op-1", "op-2"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ReconcileRebuild || decision.FetchFromUID != 1 || decision.Checkpoint.LastUID != 0 {
		t.Fatalf("unexpected rebuild decision: %#v", decision)
	}
	if len(decision.OperationsNeedingAttention) != 2 {
		t.Fatalf("pending operations were not marked once: %#v", decision.OperationsNeedingAttention)
	}
}

func TestUIDValidityUnchangedContinuesIncrementally(t *testing.T) {
	checkpoint := Checkpoint{AccountID: "account", FolderID: "inbox", UIDValidity: 7, UIDNext: 50, LastUID: 48}
	decision, err := ReconcileCheckpoint(checkpoint, 7, 55, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ReconcileIncremental || decision.FetchFromUID != 49 {
		t.Fatalf("unexpected incremental decision: %#v", decision)
	}
}

func TestMovePlanNeverUsesFullExpunge(t *testing.T) {
	if plan := PlanMove(MoveCapabilities{Move: true}); plan.Strategy != MoveDirect || plan.UseSelectiveExpunge {
		t.Fatalf("unexpected MOVE plan: %#v", plan)
	}
	if plan := PlanMove(MoveCapabilities{UIDPlus: true}); plan.Strategy != MoveCopyDeleteUIDPlus || !plan.UseSelectiveExpunge {
		t.Fatalf("unexpected UIDPLUS plan: %#v", plan)
	}
	if plan := PlanMove(MoveCapabilities{}); plan.Strategy != MoveNeedsAttention || plan.UseSelectiveExpunge {
		t.Fatalf("unsafe fallback was selected: %#v", plan)
	}
}

func TestBackoffAndMailboxSchedule(t *testing.T) {
	backoff := DefaultBackoff()
	if delay := backoff.Delay(1, 0.5); delay != 30*time.Second {
		t.Fatalf("first retry delay = %s", delay)
	}
	if delay := backoff.Delay(20, 0.5); delay != 30*time.Minute {
		t.Fatalf("maximum retry delay = %s", delay)
	}
	if delay := backoff.Delay(1, 0); delay < 30*time.Second {
		t.Fatalf("jitter dropped delay below minimum: %s", delay)
	}
	if schedule := ScheduleForMailbox(true, true); schedule.Mode != ScheduleIdle {
		t.Fatalf("Inbox did not use IDLE: %#v", schedule)
	}
	if InboxPollInterval != 30*time.Second {
		t.Fatalf("Inbox poll interval = %s, want 30s", InboxPollInterval)
	}
	if schedule := ScheduleForMailbox(true, false); schedule.Interval != 30*time.Second {
		t.Fatalf("Inbox fallback interval = %s", schedule.Interval)
	}
	if schedule := ScheduleForMailbox(false, true); schedule.Interval != 15*time.Minute {
		t.Fatalf("folder reconciliation interval = %s", schedule.Interval)
	}
}

func TestInitialSyncLoadsRecentMailBeforeHistory(t *testing.T) {
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	passes := InitialSyncPasses(now)
	boundary := now.UTC().Add(-90 * 24 * time.Hour)
	if len(passes) != 2 || passes[0].Phase != SyncRecent || !passes[0].Since.Equal(boundary) {
		t.Fatalf("unexpected recent pass: %#v", passes)
	}
	if passes[1].Phase != SyncHistory || !passes[1].Before.Equal(boundary) || !passes[1].LowPriority {
		t.Fatalf("unexpected history pass: %#v", passes)
	}
}

func TestWorkerLimiterHonorsCancellation(t *testing.T) {
	limiter := NewWorkerLimiter(1)
	release, err := limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := limiter.Acquire(ctx); err == nil {
		t.Fatal("canceled worker acquisition succeeded")
	}
	release()
}

func TestGmailCanonicalKeyDeduplicatesFolders(t *testing.T) {
	left, err := CanonicalMessageKey(accounts.ProviderGoogle, Location{AccountID: "a", FolderID: "inbox", UIDValidity: 1, UID: 10}, 999)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalMessageKey(accounts.ProviderGoogle, Location{AccountID: "a", FolderID: "all", UIDValidity: 4, UID: 77}, 999)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("Gmail folder copies were not deduplicated: %q != %q", left, right)
	}
	otherAccount, _ := CanonicalMessageKey(accounts.ProviderGoogle, Location{AccountID: "b"}, 999)
	if left == otherAccount {
		t.Fatal("Gmail key was not account scoped")
	}
}

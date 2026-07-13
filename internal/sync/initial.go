package sync

import "time"

const InitialSyncWindow = 90 * 24 * time.Hour

type SyncPhase string

const (
	SyncRecent  SyncPhase = "recent"
	SyncHistory SyncPhase = "history"
)

type SyncPass struct {
	Phase       SyncPhase
	Since       time.Time
	Before      time.Time
	LowPriority bool
}

// InitialSyncPasses keeps first-use latency bounded by loading the most recent
// 90 days before scheduling the older mailbox history at low priority.
func InitialSyncPasses(now time.Time) []SyncPass {
	boundary := now.UTC().Add(-InitialSyncWindow)
	return []SyncPass{
		{Phase: SyncRecent, Since: boundary},
		{Phase: SyncHistory, Before: boundary, LowPriority: true},
	}
}

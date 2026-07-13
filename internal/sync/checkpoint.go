package sync

import (
	"errors"
	"fmt"
	"time"
)

type Location struct {
	AccountID   string
	FolderID    string
	UIDValidity uint32
	UID         uint32
}

func (l Location) Validate() error {
	if l.AccountID == "" || l.FolderID == "" {
		return errors.New("account ID and folder ID are required")
	}
	if l.UIDValidity == 0 || l.UID == 0 {
		return errors.New("UIDVALIDITY and UID must be non-zero")
	}
	return nil
}

func (l Location) StableKey() (string, error) {
	if err := l.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", l.AccountID, l.FolderID, l.UIDValidity, l.UID), nil
}

type Checkpoint struct {
	AccountID           string
	FolderID            string
	UIDValidity         uint32
	UIDNext             uint32
	LastUID             uint32
	HighestModSeq       uint64
	BodyRefreshRequired bool
	UpdatedAt           time.Time
}

type ReconcileAction string

const (
	ReconcileInitialize  ReconcileAction = "initialize"
	ReconcileIncremental ReconcileAction = "incremental"
	ReconcileRebuild     ReconcileAction = "rebuild"
)

type ReconcileDecision struct {
	Action                     ReconcileAction
	Checkpoint                 Checkpoint
	FetchFromUID               uint32
	OperationsNeedingAttention []string
}

func ReconcileCheckpoint(current Checkpoint, observedUIDValidity, observedUIDNext uint32, pendingOperationIDs []string, now time.Time) (ReconcileDecision, error) {
	if current.AccountID == "" || current.FolderID == "" {
		return ReconcileDecision{}, errors.New("checkpoint account ID and folder ID are required")
	}
	if observedUIDValidity == 0 || observedUIDNext == 0 {
		return ReconcileDecision{}, errors.New("observed UIDVALIDITY and UIDNEXT must be non-zero")
	}
	next := current
	next.UIDValidity = observedUIDValidity
	next.UIDNext = observedUIDNext
	next.UpdatedAt = now.UTC()
	if current.UIDValidity == 0 {
		next.LastUID = 0
		return ReconcileDecision{Action: ReconcileInitialize, Checkpoint: next, FetchFromUID: 1}, nil
	}
	if current.UIDValidity != observedUIDValidity {
		next.LastUID = 0
		return ReconcileDecision{
			Action: ReconcileRebuild, Checkpoint: next, FetchFromUID: 1,
			OperationsNeedingAttention: uniqueNonEmpty(pendingOperationIDs),
		}, nil
	}
	fetchFrom := current.LastUID + 1
	if current.LastUID == ^uint32(0) {
		fetchFrom = current.LastUID
	}
	return ReconcileDecision{Action: ReconcileIncremental, Checkpoint: next, FetchFromUID: fetchFrom}, nil
}

func AdvanceCheckpoint(checkpoint Checkpoint, highestFetchedUID uint32, observedUIDNext uint32, now time.Time) (Checkpoint, error) {
	if checkpoint.UIDValidity == 0 {
		return Checkpoint{}, errors.New("cannot advance a checkpoint without UIDVALIDITY")
	}
	if observedUIDNext == 0 {
		return Checkpoint{}, errors.New("observed UIDNEXT must be non-zero")
	}
	if highestFetchedUID >= observedUIDNext {
		return Checkpoint{}, errors.New("highest fetched UID must be lower than UIDNEXT")
	}
	if highestFetchedUID < checkpoint.LastUID {
		return Checkpoint{}, errors.New("checkpoint UID cannot move backwards")
	}
	checkpoint.LastUID = highestFetchedUID
	checkpoint.UIDNext = observedUIDNext
	checkpoint.UpdatedAt = now.UTC()
	return checkpoint, nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

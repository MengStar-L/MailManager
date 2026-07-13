package mailstore

import (
	"errors"
	"fmt"
	"time"
)

const DefaultUndoWindow = 5 * time.Second

type OperationKind string

const (
	OperationMarkRead        OperationKind = "mark_read"
	OperationMarkUnread      OperationKind = "mark_unread"
	OperationStar            OperationKind = "star"
	OperationUnstar          OperationKind = "unstar"
	OperationArchive         OperationKind = "archive"
	OperationMove            OperationKind = "move"
	OperationTrash           OperationKind = "trash"
	OperationPermanentDelete OperationKind = "permanent_delete"
)

type OperationStatus string

const (
	OperationScheduled      OperationStatus = "scheduled"
	OperationApplying       OperationStatus = "applying"
	OperationApplied        OperationStatus = "applied"
	OperationFailed         OperationStatus = "failed"
	OperationUndone         OperationStatus = "undone"
	OperationNeedsAttention OperationStatus = "needs_attention"
)

type Operation struct {
	ID           string
	Kind         OperationKind
	Status       OperationStatus
	ExecuteAfter time.Time
	UpdatedAt    time.Time
	Error        string
	Confirmed    bool
}

func NewOperation(id string, kind OperationKind, now time.Time, confirmed bool) (Operation, error) {
	if id == "" {
		return Operation{}, errors.New("operation ID is required")
	}
	if !kind.valid() {
		return Operation{}, fmt.Errorf("unsupported operation kind %q", kind)
	}
	if kind == OperationPermanentDelete && !confirmed {
		return Operation{}, errors.New("permanent delete requires explicit confirmation")
	}
	executeAfter := now.UTC()
	if kind == OperationArchive || kind == OperationTrash {
		executeAfter = executeAfter.Add(DefaultUndoWindow)
	}
	return Operation{
		ID: id, Kind: kind, Status: OperationScheduled, ExecuteAfter: executeAfter,
		UpdatedAt: now.UTC(), Confirmed: confirmed,
	}, nil
}

func (o Operation) Ready(now time.Time) bool {
	return o.Status == OperationScheduled && !now.Before(o.ExecuteAfter)
}

func (o *Operation) Start(now time.Time) error {
	if o.Status != OperationScheduled {
		return invalidOperationTransition(o.Status, OperationApplying)
	}
	if now.Before(o.ExecuteAfter) {
		return errors.New("operation undo window is still open")
	}
	o.Status, o.UpdatedAt, o.Error = OperationApplying, now.UTC(), ""
	return nil
}

func (o *Operation) Complete(now time.Time) error {
	if o.Status != OperationApplying {
		return invalidOperationTransition(o.Status, OperationApplied)
	}
	o.Status, o.UpdatedAt, o.Error = OperationApplied, now.UTC(), ""
	return nil
}

func (o *Operation) Fail(now time.Time, cause error) error {
	if o.Status != OperationApplying {
		return invalidOperationTransition(o.Status, OperationFailed)
	}
	if cause == nil {
		return errors.New("failure cause is required")
	}
	o.Status, o.UpdatedAt, o.Error = OperationFailed, now.UTC(), cause.Error()
	return nil
}

func (o *Operation) Retry(now time.Time) error {
	if o.Status != OperationFailed {
		return invalidOperationTransition(o.Status, OperationScheduled)
	}
	o.Status, o.ExecuteAfter, o.UpdatedAt, o.Error = OperationScheduled, now.UTC(), now.UTC(), ""
	return nil
}

func (o *Operation) Undo(now time.Time) error {
	if o.Status != OperationScheduled {
		return invalidOperationTransition(o.Status, OperationUndone)
	}
	if !now.Before(o.ExecuteAfter) {
		return errors.New("operation undo window has closed")
	}
	o.Status, o.UpdatedAt = OperationUndone, now.UTC()
	return nil
}

func (o *Operation) MarkNeedsAttention(now time.Time, reason string) error {
	if o.Status != OperationScheduled && o.Status != OperationApplying {
		return invalidOperationTransition(o.Status, OperationNeedsAttention)
	}
	if reason == "" {
		return errors.New("attention reason is required")
	}
	o.Status, o.UpdatedAt, o.Error = OperationNeedsAttention, now.UTC(), reason
	return nil
}

func (kind OperationKind) valid() bool {
	switch kind {
	case OperationMarkRead, OperationMarkUnread, OperationStar, OperationUnstar,
		OperationArchive, OperationMove, OperationTrash, OperationPermanentDelete:
		return true
	default:
		return false
	}
}

func invalidOperationTransition(from, to OperationStatus) error {
	return fmt.Errorf("invalid operation transition from %q to %q", from, to)
}

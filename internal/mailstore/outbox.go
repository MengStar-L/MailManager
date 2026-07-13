package mailstore

import (
	"errors"
	"fmt"
	"time"
)

type OutboxStatus string

const (
	OutboxQueued  OutboxStatus = "queued"
	OutboxSending OutboxStatus = "sending"
	OutboxSent    OutboxStatus = "sent"
	OutboxFailed  OutboxStatus = "failed"
	OutboxUnknown OutboxStatus = "unknown"
)

type OutboxItem struct {
	ID        string
	Status    OutboxStatus
	Attempts  int
	UpdatedAt time.Time
	Error     string
}

func NewOutboxItem(id string, now time.Time) (OutboxItem, error) {
	if id == "" {
		return OutboxItem{}, errors.New("outbox ID is required")
	}
	return OutboxItem{ID: id, Status: OutboxQueued, UpdatedAt: now.UTC()}, nil
}

func (item *OutboxItem) Start(now time.Time) error {
	if item.Status != OutboxQueued {
		return invalidOutboxTransition(item.Status, OutboxSending)
	}
	item.Status, item.UpdatedAt, item.Error = OutboxSending, now.UTC(), ""
	item.Attempts++
	return nil
}

func (item *OutboxItem) MarkSent(now time.Time) error {
	if item.Status != OutboxSending && item.Status != OutboxUnknown {
		return invalidOutboxTransition(item.Status, OutboxSent)
	}
	item.Status, item.UpdatedAt, item.Error = OutboxSent, now.UTC(), ""
	return nil
}

func (item *OutboxItem) MarkFailed(now time.Time, cause error) error {
	if item.Status != OutboxSending && item.Status != OutboxUnknown {
		return invalidOutboxTransition(item.Status, OutboxFailed)
	}
	if cause == nil {
		return errors.New("failure cause is required")
	}
	item.Status, item.UpdatedAt, item.Error = OutboxFailed, now.UTC(), cause.Error()
	return nil
}

func (item *OutboxItem) MarkUnknown(now time.Time, cause error) error {
	if item.Status != OutboxSending {
		return invalidOutboxTransition(item.Status, OutboxUnknown)
	}
	if cause == nil {
		return errors.New("unknown delivery cause is required")
	}
	item.Status, item.UpdatedAt, item.Error = OutboxUnknown, now.UTC(), cause.Error()
	return nil
}

func (item *OutboxItem) Retry(now time.Time) error {
	if item.Status != OutboxFailed {
		return invalidOutboxTransition(item.Status, OutboxQueued)
	}
	item.Status, item.UpdatedAt, item.Error = OutboxQueued, now.UTC(), ""
	return nil
}

func invalidOutboxTransition(from, to OutboxStatus) error {
	return fmt.Errorf("invalid outbox transition from %q to %q", from, to)
}

package sync

import (
	"context"
	"math"
	"sync"
	"time"
)

const (
	DefaultWorkerLimit = 4
	// Polls ride the pooled per-account connection (NOOP + SELECT + SEARCH),
	// so a short interval no longer multiplies logins.
	InboxPollInterval       = 30 * time.Second
	FolderReconcileInterval = 15 * time.Minute
)

type ScheduleMode string

const (
	ScheduleIdle ScheduleMode = "idle"
	SchedulePoll ScheduleMode = "poll"
)

type MailboxSchedule struct {
	Mode     ScheduleMode
	Interval time.Duration
}

func ScheduleForMailbox(isInbox, supportsIdle bool) MailboxSchedule {
	if isInbox && supportsIdle {
		return MailboxSchedule{Mode: ScheduleIdle}
	}
	if isInbox {
		return MailboxSchedule{Mode: SchedulePoll, Interval: InboxPollInterval}
	}
	return MailboxSchedule{Mode: SchedulePoll, Interval: FolderReconcileInterval}
}

type Backoff struct {
	Minimum time.Duration
	Maximum time.Duration
	Factor  float64
	Jitter  float64
}

func DefaultBackoff() Backoff {
	return Backoff{Minimum: 30 * time.Second, Maximum: 30 * time.Minute, Factor: 2, Jitter: 0.2}
}

// Delay returns a bounded exponential delay. sample must be in [0, 1] and is
// injectable so scheduling tests remain deterministic.
func (b Backoff) Delay(failures int, sample float64) time.Duration {
	if b.Minimum <= 0 {
		b.Minimum = 30 * time.Second
	}
	if b.Maximum < b.Minimum {
		b.Maximum = 30 * time.Minute
		if b.Maximum < b.Minimum {
			b.Maximum = b.Minimum
		}
	}
	if b.Factor < 1 {
		b.Factor = 2
	}
	if b.Jitter < 0 {
		b.Jitter = 0
	}
	if b.Jitter > 1 {
		b.Jitter = 1
	}
	if failures < 1 {
		failures = 1
	}
	if sample < 0 {
		sample = 0
	}
	if sample > 1 {
		sample = 1
	}
	delay := float64(b.Minimum) * math.Pow(b.Factor, float64(failures-1))
	if delay > float64(b.Maximum) {
		delay = float64(b.Maximum)
	}
	multiplier := 1 + b.Jitter*(2*sample-1)
	delay *= multiplier
	if delay > float64(b.Maximum) {
		delay = float64(b.Maximum)
	}
	if delay < float64(b.Minimum) {
		delay = float64(b.Minimum)
	}
	if delay < 0 {
		return 0
	}
	return time.Duration(delay)
}

func (b Backoff) NextAttempt(now time.Time, failures int, sample float64) time.Time {
	return now.Add(b.Delay(failures, sample))
}

type WorkerLimiter struct {
	permits chan struct{}
}

func NewWorkerLimiter(limit int) *WorkerLimiter {
	if limit <= 0 {
		limit = DefaultWorkerLimit
	}
	return &WorkerLimiter{permits: make(chan struct{}, limit)}
}

func (l *WorkerLimiter) Acquire(ctx context.Context) (func(), error) {
	select {
	case l.permits <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-l.permits }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

package auth

import (
	"sync"
	"time"
)

type RateLimiterOptions struct {
	MaxFailures int
	Window      time.Duration
	Now         func() time.Time
}

// RateLimiter is an in-memory sliding-window limiter. It intentionally records
// failures only, so successful logins are never penalized by normal traffic.
type RateLimiter struct {
	mu          sync.Mutex
	maxFailures int
	window      time.Duration
	now         func() time.Time
	failures    map[string][]time.Time
}

func NewRateLimiter(options RateLimiterOptions) *RateLimiter {
	if options.MaxFailures <= 0 {
		options.MaxFailures = 5
	}
	if options.Window <= 0 {
		options.Window = 15 * time.Minute
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return &RateLimiter{maxFailures: options.MaxFailures, window: options.Window, now: options.Now, failures: make(map[string][]time.Time)}
}

func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entries := l.active(key, now)
	if len(entries) < l.maxFailures {
		return true, 0
	}
	retry := entries[0].Add(l.window).Sub(now)
	if retry < 0 {
		retry = 0
	}
	return false, retry
}

func (l *RateLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entries := l.active(key, now)
	l.failures[key] = append(entries, now)
}

func (l *RateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

func (l *RateLimiter) active(key string, now time.Time) []time.Time {
	entries := l.failures[key]
	cutoff := now.Add(-l.window)
	first := 0
	for first < len(entries) && !entries[first].After(cutoff) {
		first++
	}
	if first == len(entries) {
		delete(l.failures, key)
		return nil
	}
	entries = entries[first:]
	l.failures[key] = entries
	return entries
}

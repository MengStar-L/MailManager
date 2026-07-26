package mailruntime

import (
	"context"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
)

const (
	// sessionMaxAge bounds reuse so credential changes and provider-side
	// connection state (e.g. QQ serving stale mailbox views on long-lived
	// connections) are refreshed on a fresh login.
	sessionMaxAge       = 10 * time.Minute
	sessionMaxIdle      = 5 * time.Minute
	sessionProbeTimeout = 30 * time.Second
)

// pooledSession is a per-account foreground IMAP connection reused across
// poll ticks so each account performs a handful of logins per hour instead of
// one per minute, which 163/QQ throttle.
type pooledSession struct {
	session  connectors.IMAPSession
	identity string
	dialed   time.Time
	lastUsed time.Time
}

func sessionIdentity(config accounts.Config) string {
	return config.IMAP.Address() + "\x00" + config.Credentials.Username
}

// backgroundPoolKey namespaces the pool slot used by serialized background
// jobs (reconcile, backfill) so they share one connection per account without
// ever holding the foreground new-mail session.
func backgroundPoolKey(accountID string) string {
	return "bg\x00" + accountID
}

// acquireSession checks the slot's cached session out of the pool, verifying
// it with NOOP, and dials a fresh one when there is none, it aged out, the
// account identity changed, or the probe fails. Access to a slot must be
// serialized by its owning lock: the foreground account lock for plain
// account keys, the background lock for backgroundPoolKey slots. Hand the
// session back via releaseSession.
func (r *Runtime) acquireSession(ctx context.Context, accountID string, config accounts.Config) (*pooledSession, error) {
	identity := sessionIdentity(config)
	r.poolMu.Lock()
	entry := r.sessions[accountID]
	delete(r.sessions, accountID)
	r.poolMu.Unlock()
	if entry != nil {
		if entry.identity == identity && time.Since(entry.dialed) < sessionMaxAge {
			probeCtx, cancel := context.WithTimeout(ctx, sessionProbeTimeout)
			err := entry.session.Noop(probeCtx)
			cancel()
			if err == nil {
				entry.lastUsed = time.Now()
				return entry, nil
			}
		}
		_ = entry.session.Close()
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &pooledSession{session: session, identity: identity, dialed: now, lastUsed: now}, nil
}

func (r *Runtime) releaseSession(accountID string, entry *pooledSession, healthy bool) {
	if entry == nil {
		return
	}
	if !healthy {
		_ = entry.session.Close()
		return
	}
	entry.lastUsed = time.Now()
	r.poolMu.Lock()
	previous := r.sessions[accountID]
	r.sessions[accountID] = entry
	r.poolMu.Unlock()
	if previous != nil && previous != entry {
		_ = previous.session.Close()
	}
}

// reapSessions closes checked-in sessions that idled out (their account was
// removed or disabled) or exceeded their maximum age.
func (r *Runtime) reapSessions() {
	now := time.Now()
	r.poolMu.Lock()
	var stale []*pooledSession
	for accountID, entry := range r.sessions {
		if now.Sub(entry.lastUsed) > sessionMaxIdle || now.Sub(entry.dialed) > sessionMaxAge {
			stale = append(stale, entry)
			delete(r.sessions, accountID)
		}
	}
	r.poolMu.Unlock()
	for _, entry := range stale {
		_ = entry.session.Close()
	}
}

func (r *Runtime) closeAllSessions() {
	r.poolMu.Lock()
	entries := make([]*pooledSession, 0, len(r.sessions))
	for _, entry := range r.sessions {
		entries = append(entries, entry)
	}
	clear(r.sessions)
	r.poolMu.Unlock()
	for _, entry := range entries {
		_ = entry.session.Close()
	}
}

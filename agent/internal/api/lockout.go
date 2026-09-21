package api

import (
	"math"
	"net"
	"net/http"
	"sync"
	"time"
)

// Lockout policy. Five wrong tokens from one address and that address is shut
// out for fifteen minutes. The counter is per remote IP rather than global so
// one noisy source cannot lock the real panel out.
const (
	MaxFailures   = 5
	LockoutFor    = 15 * time.Minute
	RetryAfterSec = 900 // LockoutFor in seconds, sent in the Retry-After header
	IdlePurge     = 30 * time.Minute
	MaxTracked    = 10000
)

type lockEntry struct {
	failures int
	until    time.Time
	seen     time.Time
	logged   bool // a locked-out address is logged once, not once per request
}

// lockout tracks failed authentication per remote address.
//
// It is consulted BEFORE the token is hashed and compared. Doing the work
// first would hand an attacker a timing signal and let a flood of requests
// burn CPU on hashing that can never succeed.
type lockout struct {
	mu      sync.Mutex
	entries map[string]*lockEntry
	now     func() time.Time
	purged  time.Time
}

func newLockout(now func() time.Time) *lockout {
	if now == nil {
		now = time.Now
	}
	return &lockout{entries: map[string]*lockEntry{}, now: now}
}

// clientIP strips the port. Locking out "203.0.113.5:54321" would lock out one
// TCP connection and nothing else.
func clientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// locked reports whether this address is currently shut out, and for how many
// more seconds.
func (l *lockout) locked(ip string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		return false, 0
	}
	now := l.now()
	e.seen = now
	if now.Before(e.until) {
		// Round up so Retry-After never tells a caller to come back a
		// fraction of a second before the lockout actually lifts.
		return true, int(math.Ceil(e.until.Sub(now).Seconds()))
	}
	return false, 0
}

// fail records one failed attempt. It returns true the first time the address
// crosses into a lockout, so the caller logs it exactly once.
func (l *lockout) fail(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.purgeLocked(now)
	e, ok := l.entries[ip]
	if !ok {
		if len(l.entries) >= MaxTracked {
			// The table is full of addresses we are still tracking. Drop the
			// least recently seen rather than growing without bound: an
			// attacker with a large address pool must not be able to turn this
			// counter into a memory leak.
			l.evictOldestLocked()
		}
		e = &lockEntry{}
		l.entries[ip] = e
	}
	e.seen = now
	e.failures++
	if e.failures >= MaxFailures {
		e.until = now.Add(LockoutFor)
		if !e.logged {
			e.logged = true
			return true
		}
	}
	return false
}

// success clears an address after a request that authenticated.
func (l *lockout) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

// purgeLocked drops entries nothing has touched for IdlePurge. Rate limited to
// once a minute so a request flood does not walk the whole map every time.
func (l *lockout) purgeLocked(now time.Time) {
	if now.Sub(l.purged) < time.Minute {
		return
	}
	l.purged = now
	for ip, e := range l.entries {
		if now.Sub(e.seen) > IdlePurge && now.After(e.until) {
			delete(l.entries, ip)
		}
	}
}

func (l *lockout) evictOldestLocked() {
	var oldestIP string
	var oldest time.Time
	for ip, e := range l.entries {
		if oldestIP == "" || e.seen.Before(oldest) {
			oldestIP, oldest = ip, e.seen
		}
	}
	if oldestIP != "" {
		delete(l.entries, oldestIP)
	}
}

// size reports how many addresses are tracked. Tests only.
func (l *lockout) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func writeLockout(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", itoa(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, errorResponse{
		Error: "too many failed authentication attempts from this address; try again later",
	})
}

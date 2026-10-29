// Package ratelimit provides functionality for Herald.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter provides per-key rate limiting using token bucket algorithm
type Limiter struct {
	mu        sync.Mutex
	limiters  map[string]*rate.Limiter
	rate      rate.Limit
	burst     int
	cleanup   time.Duration
	lastSweep time.Time
}

// New creates a new Limiter with the given rate (requests per second) and burst size
func New(rps float64, burst int) *Limiter {
	return &Limiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Limit(rps),
		burst:    burst,
		cleanup:  5 * time.Minute,
	}
}

// Allow checks if the request for the given key is allowed
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Sweep lazily on use, so a limiter needs no background goroutine to stop.
	if now := time.Now(); now.Sub(l.lastSweep) >= l.cleanup {
		l.sweepLocked(now)
		l.lastSweep = now
	}

	limiter, exists := l.limiters[key]
	if !exists {
		limiter = rate.NewLimiter(l.rate, l.burst)
		l.limiters[key] = limiter
	}
	return limiter.Allow()
}

// sweep drops only buckets that have fully refilled, since a fresh bucket
// would grant exactly the same requests; evicting anything earlier resets
// the limit.
func (l *Limiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}

func (l *Limiter) sweepLocked(now time.Time) {
	for key, limiter := range l.limiters {
		if limiter.TokensAt(now) >= float64(l.burst) {
			delete(l.limiters, key)
		}
	}
}

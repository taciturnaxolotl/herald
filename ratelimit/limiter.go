// Package ratelimit provides functionality for Herald.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter provides per-key rate limiting using token bucket algorithm
type Limiter struct {
	mu       sync.RWMutex
	limiters map[string]*rate.Limiter
	rate     rate.Limit
	burst    int
	cleanup  time.Duration
	lastSeen map[string]time.Time
}

// New creates a new Limiter with the given rate (requests per second) and burst size
func New(rps float64, burst int) *Limiter {
	l := &Limiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Limit(rps),
		burst:    burst,
		cleanup:  5 * time.Minute,
		lastSeen: make(map[string]time.Time),
	}
	go l.cleanupLoop()
	return l
}

// Allow checks if the request for the given key is allowed
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	limiter, exists := l.limiters[key]
	if !exists {
		limiter = rate.NewLimiter(l.rate, l.burst)
		l.limiters[key] = limiter
	}

	l.lastSeen[key] = time.Now()
	return limiter.Allow()
}

// cleanupLoop removes limiters that haven't been used recently
func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(l.cleanup)
	defer ticker.Stop()

	for range ticker.C {
		l.mu.Lock()
		cutoff := time.Now().Add(-l.cleanup * 2)
		for key, lastSeen := range l.lastSeen {
			if lastSeen.Before(cutoff) {
				delete(l.limiters, key)
				delete(l.lastSeen, key)
			}
		}
		l.mu.Unlock()
	}
}

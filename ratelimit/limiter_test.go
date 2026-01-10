package ratelimit

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestNew(t *testing.T) {
	limiter := New(10, 5)
	if limiter == nil {
		t.Fatal("New() returned nil")
	}
	if limiter.rate != 10 {
		t.Errorf("expected rate 10, got %v", limiter.rate)
	}
	if limiter.burst != 5 {
		t.Errorf("expected burst 5, got %d", limiter.burst)
	}
	if limiter.limiters == nil {
		t.Error("limiters map not initialized")
	}
	if limiter.lastSeen == nil {
		t.Error("lastSeen map not initialized")
	}
}

func TestAllow_SingleKey(t *testing.T) {
	limiter := New(10, 2) // 10 req/sec, burst of 2
	key := "test-key"

	// First two requests should succeed (burst)
	if !limiter.Allow(key) {
		t.Error("first request should be allowed")
	}
	if !limiter.Allow(key) {
		t.Error("second request should be allowed")
	}

	// Third request should fail (burst exhausted)
	if limiter.Allow(key) {
		t.Error("third request should be blocked")
	}
}

func TestAllow_MultipleKeys(t *testing.T) {
	limiter := New(10, 1)
	
	key1 := "key1"
	key2 := "key2"

	// Each key should have independent limit
	if !limiter.Allow(key1) {
		t.Error("key1 first request should be allowed")
	}
	if !limiter.Allow(key2) {
		t.Error("key2 first request should be allowed")
	}

	// Second requests should fail for both
	if limiter.Allow(key1) {
		t.Error("key1 second request should be blocked")
	}
	if limiter.Allow(key2) {
		t.Error("key2 second request should be blocked")
	}
}

func TestAllow_TokenRefill(t *testing.T) {
	limiter := New(10, 1) // 10 req/sec = 100ms per token
	key := "test-key"

	// Exhaust burst
	if !limiter.Allow(key) {
		t.Fatal("first request should be allowed")
	}
	if limiter.Allow(key) {
		t.Fatal("second request should be blocked")
	}

	// Wait for token refill
	time.Sleep(150 * time.Millisecond)

	// Should be allowed again
	if !limiter.Allow(key) {
		t.Error("request after refill should be allowed")
	}
}

func TestAllow_UpdatesLastSeen(t *testing.T) {
	limiter := New(10, 5)
	key := "test-key"

	before := time.Now()
	limiter.Allow(key)
	after := time.Now()

	limiter.mu.RLock()
	lastSeen, exists := limiter.lastSeen[key]
	limiter.mu.RUnlock()

	if !exists {
		t.Fatal("lastSeen not updated for key")
	}
	if lastSeen.Before(before) || lastSeen.After(after) {
		t.Errorf("lastSeen timestamp %v not in range [%v, %v]", lastSeen, before, after)
	}
}

func TestCleanup(t *testing.T) {
	limiter := &Limiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     10,
		burst:    5,
		cleanup:  100 * time.Millisecond,
		lastSeen: make(map[string]time.Time),
	}

	// Add old entries
	oldTime := time.Now().Add(-1 * time.Hour)
	limiter.limiters["old-key"] = rate.NewLimiter(10, 5)
	limiter.lastSeen["old-key"] = oldTime

	// Add recent entry
	limiter.Allow("recent-key")

	// Run cleanup
	limiter.mu.Lock()
	cutoff := time.Now().Add(-limiter.cleanup * 2)
	for key, lastSeen := range limiter.lastSeen {
		if lastSeen.Before(cutoff) {
			delete(limiter.limiters, key)
			delete(limiter.lastSeen, key)
		}
	}
	limiter.mu.Unlock()

	// Check old key removed
	limiter.mu.RLock()
	_, oldExists := limiter.limiters["old-key"]
	_, recentExists := limiter.limiters["recent-key"]
	limiter.mu.RUnlock()

	if oldExists {
		t.Error("old key should be removed")
	}
	if !recentExists {
		t.Error("recent key should remain")
	}
}

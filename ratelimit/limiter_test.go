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

func TestSweepKeepsExhaustedBuckets(t *testing.T) {
	limiter := &Limiter{limiters: make(map[string]*rate.Limiter), rate: rate.Limit(5.0 / 86400), burst: 5}

	for i := 0; i < 5; i++ {
		if !limiter.Allow("ip") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	limiter.sweep(time.Now().Add(15 * time.Minute))

	if limiter.Allow("ip") {
		t.Error("an idle period must not reset a daily limit")
	}
}

func TestSweepEvictsRefilledBuckets(t *testing.T) {
	limiter := &Limiter{limiters: make(map[string]*rate.Limiter), rate: 10, burst: 5}

	limiter.Allow("old-key")
	limiter.sweep(time.Now().Add(time.Hour))

	limiter.mu.Lock()
	_, exists := limiter.limiters["old-key"]
	limiter.mu.Unlock()
	if exists {
		t.Error("refilled bucket should be evicted")
	}
}

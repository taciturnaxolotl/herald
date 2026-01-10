package web

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

// Metrics holds runtime metrics for observability
type Metrics struct {
	StartTime      time.Time
	RequestsTotal  atomic.Uint64
	RequestsActive atomic.Int64
	EmailsSent     atomic.Uint64
	FeedsFetched   atomic.Uint64
	ItemsSeen      atomic.Uint64
	ConfigsActive  atomic.Uint64
	ErrorsTotal    atomic.Uint64
	RateLimitHits  atomic.Uint64
}

// NewMetrics creates a new Metrics instance
func NewMetrics() *Metrics {
	return &Metrics{
		StartTime: time.Now(),
	}
}

// MetricsSnapshot represents a point-in-time view of metrics
type MetricsSnapshot struct {
	// System info
	UptimeSeconds   int64  `json:"uptime_seconds"`
	GoVersion       string `json:"go_version"`
	NumGoroutines   int    `json:"goroutines"`
	MemAllocMB      uint64 `json:"mem_alloc_mb"`
	MemTotalAllocMB uint64 `json:"mem_total_alloc_mb"`
	MemSysMB        uint64 `json:"mem_sys_mb"`

	// Application metrics
	RequestsTotal  uint64 `json:"requests_total"`
	RequestsActive int64  `json:"requests_active"`
	EmailsSent     uint64 `json:"emails_sent"`
	FeedsFetched   uint64 `json:"feeds_fetched"`
	ItemsSeen      uint64 `json:"items_seen"`
	ConfigsActive  uint64 `json:"configs_active"`
	ErrorsTotal    uint64 `json:"errors_total"`
	RateLimitHits  uint64 `json:"rate_limit_hits"`
}

// Snapshot creates a snapshot of current metrics
func (m *Metrics) Snapshot() MetricsSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	return MetricsSnapshot{
		UptimeSeconds:   int64(time.Since(m.StartTime).Seconds()),
		GoVersion:       runtime.Version(),
		NumGoroutines:   runtime.NumGoroutine(),
		MemAllocMB:      mem.Alloc / 1024 / 1024,
		MemTotalAllocMB: mem.TotalAlloc / 1024 / 1024,
		MemSysMB:        mem.Sys / 1024 / 1024,
		RequestsTotal:   m.RequestsTotal.Load(),
		RequestsActive:  m.RequestsActive.Load(),
		EmailsSent:      m.EmailsSent.Load(),
		FeedsFetched:    m.FeedsFetched.Load(),
		ItemsSeen:       m.ItemsSeen.Load(),
		ConfigsActive:   m.ConfigsActive.Load(),
		ErrorsTotal:     m.ErrorsTotal.Load(),
		RateLimitHits:   m.RateLimitHits.Load(),
	}
}

// handleMetrics serves the /metrics endpoint
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	snapshot := s.metrics.Snapshot()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		s.logger.Warn("failed to encode metrics", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleHealth serves the /health endpoint
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Simple health check - could be extended to check DB connection, etc.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	response := map[string]string{
		"status": "ok",
		"uptime": time.Since(s.metrics.StartTime).String(),
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Warn("failed to encode health response", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

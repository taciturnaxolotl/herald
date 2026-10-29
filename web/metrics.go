package web

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

// metrics counts HTTP activity for the /metrics endpoint.
type metrics struct {
	startTime      time.Time
	requestsTotal  atomic.Uint64
	requestsActive atomic.Int64
	errorsTotal    atomic.Uint64
	rateLimitHits  atomic.Uint64
}

type metricsSnapshot struct {
	UptimeSeconds   int64  `json:"uptime_seconds"`
	GoVersion       string `json:"go_version"`
	NumGoroutines   int    `json:"goroutines"`
	MemAllocMB      uint64 `json:"mem_alloc_mb"`
	MemTotalAllocMB uint64 `json:"mem_total_alloc_mb"`
	MemSysMB        uint64 `json:"mem_sys_mb"`
	RequestsTotal   uint64 `json:"requests_total"`
	RequestsActive  int64  `json:"requests_active"`
	ErrorsTotal     uint64 `json:"errors_total"`
	RateLimitHits   uint64 `json:"rate_limit_hits"`
}

func (m *metrics) snapshot() metricsSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	return metricsSnapshot{
		UptimeSeconds:   int64(time.Since(m.startTime).Seconds()),
		GoVersion:       runtime.Version(),
		NumGoroutines:   runtime.NumGoroutine(),
		MemAllocMB:      mem.Alloc / 1024 / 1024,
		MemTotalAllocMB: mem.TotalAlloc / 1024 / 1024,
		MemSysMB:        mem.Sys / 1024 / 1024,
		RequestsTotal:   m.requestsTotal.Load(),
		RequestsActive:  m.requestsActive.Load(),
		ErrorsTotal:     m.errorsTotal.Load(),
		RateLimitHits:   m.rateLimitHits.Load(),
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	writeNoCacheJSON(w, s.metrics.snapshot())
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeNoCacheJSON(w, struct {
		Status string `json:"status"`
		Uptime string `json:"uptime"`
	}{"ok", time.Since(s.metrics.startTime).String()})
}

func writeNoCacheJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_ = json.NewEncoder(w).Encode(v)
}

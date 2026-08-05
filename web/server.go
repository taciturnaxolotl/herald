// Package web provides functionality for Herald.
package web

import (
	"context"
	"embed"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kierank/herald/ratelimit"
	"github.com/kierank/herald/store"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed public/*
var publicFS embed.FS

const (
	// HTTP rate limiting
	httpRequestsPerSecond = 10
	httpRateLimiterBurst  = 20
)

type Server struct {
	store       *store.DB
	addr        string
	origin      string
	sshPort     int
	logger      *log.Logger
	tmpl        *template.Template
	commitHash  string
	rateLimiter *ratelimit.Limiter
	metrics     *Metrics
}

func NewServer(st *store.DB, addr string, origin string, sshPort int, logger *log.Logger, commitHash string) *Server {
	tmpl := template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	return &Server{
		store:       st,
		addr:        addr,
		origin:      origin,
		sshPort:     sshPort,
		logger:      logger,
		tmpl:        tmpl,
		commitHash:  commitHash,
		rateLimiter: ratelimit.New(httpRequestsPerSecond, httpRateLimiterBurst),
		metrics:     NewMetrics(),
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.routeHandler)
	mux.HandleFunc("/style.css", s.handleStyleCSS)
	mux.HandleFunc("/favicon.svg", s.handleFaviconSVG)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.loggingMiddleware(s.rateLimitMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() { //nolint:gosec // Background context needed for graceful shutdown; request ctx is already cancelled
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	s.logger.Info("web server listening", "addr", s.addr)
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)

		if !s.rateLimiter.Allow(ip) {
			s.metrics.RateLimitHits.Add(1)
			s.logger.Warn("rate limit exceeded", "ip", ip, "path", r.URL.Path)
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// clientIP returns the real client address for rate limiting and logging.
//
// Herald runs behind a local Caddy reverse proxy, so every request arrives
// from loopback and r.RemoteAddr is useless as an identity -- keying the rate
// limiter on it collapses all clients into a single bucket. Caddy forwards the
// real address in X-Forwarded-For.
//
// The header is only trusted when the direct peer is loopback (i.e. the proxy).
// A direct connection to the HTTP port is keyed on its socket address instead,
// so an attacker reaching the port cannot spoof arbitrary IPs via the header.
// When trusted, the rightmost entry is used: that is the address the proxy
// observed, whereas any values to its left were supplied by the client and
// must not be believed.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	peer := net.ParseIP(host)
	if peer == nil || !peer.IsLoopback() {
		return host
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if ip := strings.TrimSpace(parts[i]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return host
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		s.metrics.RequestsTotal.Add(1)
		s.metrics.RequestsActive.Add(1)
		defer s.metrics.RequestsActive.Add(-1)

		// Wrap response writer to capture status code
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(lrw, r)

		duration := time.Since(start)

		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", lrw.statusCode,
			"duration_ms", duration.Milliseconds(),
			"remote_addr", clientIP(r),
		)

		if lrw.statusCode >= 500 {
			s.metrics.ErrorsTotal.Add(1)
		}
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (s *Server) routeHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")

	if path == "" {
		s.handleIndex(w, r)
		return
	}

	parts := strings.Split(path, "/")

	if len(parts) == 2 && parts[0] == "unsubscribe" {
		s.handleUnsubscribe(w, r, parts[1])
		return
	}

	if len(parts) == 2 && parts[0] == "keep-alive" {
		s.handleKeepAlive(w, r, parts[1])
		return
	}

	switch len(parts) {
	case 1:
		s.handleUser(w, r, parts[0])
	case 2:
		// Check if it's a feed file (ends with .xml or .json)
		if strings.HasSuffix(parts[1], ".xml") {
			// Extract base name by removing .xml extension, then append .txt to find config
			baseName := strings.TrimSuffix(parts[1], ".xml")
			configFile := baseName + ".txt"
			s.handleFeedXML(w, r, parts[0], configFile)
		} else if strings.HasSuffix(parts[1], ".json") {
			// Extract base name by removing .json extension, then append .txt to find config
			baseName := strings.TrimSuffix(parts[1], ".json")
			configFile := baseName + ".txt"
			s.handleFeedJSON(w, r, parts[0], configFile)
		} else {
			// Raw config file
			s.handleConfig(w, r, parts[0], parts[1])
		}
	default:
		s.handle404(w, r)
	}
}

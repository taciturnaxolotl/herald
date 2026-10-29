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

	"charm.land/log/v2"
	"github.com/taciturnaxolotl/herald/ratelimit"
	"github.com/taciturnaxolotl/herald/store"
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
	mux         *http.ServeMux
}

func NewServer(st *store.DB, addr string, origin string, sshPort int, logger *log.Logger, commitHash string) *Server {
	tmpl := template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	s := &Server{
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
	s.mux = s.routes()
	return s
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /style.css", staticFile(templatesFS, "templates/style.css", "text/css; charset=utf-8"))
	mux.Handle("GET /favicon.svg", staticFile(publicFS, "public/favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	mux.HandleFunc("GET /unsubscribe/{token}", s.handleUnsubscribeGET)
	mux.HandleFunc("POST /unsubscribe/{token}", s.handleUnsubscribePOST)
	mux.HandleFunc("GET /verify/{token}", s.handleVerifyGET)
	mux.HandleFunc("POST /verify/{token}", s.handleVerifyPOST)
	mux.HandleFunc("GET /keep-alive/{token}", s.handleKeepAlive)
	mux.HandleFunc("POST /keep-alive/{token}", s.handleKeepAlive)

	mux.HandleFunc("GET /{fingerprint}", s.handleUser)
	mux.HandleFunc("GET /{fingerprint}/{$}", s.handleUser)
	mux.HandleFunc("GET /{fingerprint}/{file}", s.handleConfigOrFeed)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { s.notFound(w, "", "") })
	return mux
}

func staticFile(fsys embed.FS, name, contentType string) http.Handler {
	data, err := fsys.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	})
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.loggingMiddleware(s.rateLimitMiddleware(s.mux)),
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

// handleConfigOrFeed serves /fingerprint/name.txt as the raw config and
// name.xml or name.json as that config's feed.
func (s *Server) handleConfigOrFeed(w http.ResponseWriter, r *http.Request) {
	fingerprint, file := r.PathValue("fingerprint"), r.PathValue("file")
	if base, ok := strings.CutSuffix(file, ".xml"); ok {
		s.handleFeedXML(w, r, fingerprint, base+".txt")
	} else if base, ok := strings.CutSuffix(file, ".json"); ok {
		s.handleFeedJSON(w, r, fingerprint, base+".txt")
	} else {
		s.handleConfig(w, r, fingerprint, file)
	}
}

// feedPath is the inverse of handleConfigOrFeed: a config's generated feed path.
func feedPath(fingerprint, configFilename, ext string) string {
	return "/" + fingerprint + "/" + strings.TrimSuffix(configFilename, ".txt") + ext
}

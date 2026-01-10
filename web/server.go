package web

import (
	"context"
	"embed"
	"html/template"
	"net"
	"net/http"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/kierank/herald/ratelimit"
	"github.com/kierank/herald/store"
)

//go:embed templates/*
var templatesFS embed.FS

type Server struct {
	store       *store.DB
	addr        string
	origin      string
	sshPort     int
	logger      *log.Logger
	tmpl        *template.Template
	commitHash  string
	rateLimiter *ratelimit.Limiter
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
		rateLimiter: ratelimit.New(10, 20), // 10 req/sec, burst of 20
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.routeHandler)
	mux.HandleFunc("/style.css", s.handleStyleCSS)

	srv := &http.Server{
		Addr:    s.addr,
		Handler: s.rateLimitMiddleware(mux),
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
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
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		if !s.rateLimiter.Allow(ip) {
			s.logger.Warn("rate limit exceeded", "ip", ip, "path", r.URL.Path)
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
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

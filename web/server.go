package web

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/kierank/herald/store"
)

//go:embed templates/*
var templatesFS embed.FS

type Server struct {
	store   *store.DB
	addr    string
	origin  string
	sshPort int
	logger  *log.Logger
	tmpl    *template.Template
}

func NewServer(st *store.DB, addr string, origin string, sshPort int, logger *log.Logger) *Server {
	tmpl := template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	return &Server{
		store:   st,
		addr:    addr,
		origin:  origin,
		sshPort: sshPort,
		logger:  logger,
		tmpl:    tmpl,
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.routeHandler)
	mux.HandleFunc("/style.css", s.handleStyleCSS)

	srv := &http.Server{
		Addr:    s.addr,
		Handler: mux,
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

func (s *Server) routeHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")

	if path == "" {
		s.handleIndex(w, r)
		return
	}

	parts := strings.Split(path, "/")

	switch len(parts) {
	case 1:
		s.handleUser(w, r, parts[0])
	case 2:
		switch parts[1] {
		case "feed.xml":
			s.handleFeedXML(w, r, parts[0])
		case "feed.json":
			s.handleFeedJSON(w, r, parts[0])
		default:
			s.handleConfig(w, r, parts[0], parts[1])
		}
	default:
		http.NotFound(w, r)
	}
}

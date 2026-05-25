package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Config struct {
	Addr          string
	DataDir       string
	MaxUploadSize int64
	MaxAge        time.Duration
}

type Server struct {
	cfg       Config
	store     *Store
	mux       *http.ServeMux
	tmpls     map[string]*template.Template
	chromaCSS template.CSS
}

func main() {
	var cfg Config
	var maxMB int
	var maxAgeDays int
	flag.StringVar(&cfg.Addr, "addr", ":7995", "listen address")
	flag.StringVar(&cfg.DataDir, "data", "./data", "data directory")
	flag.IntVar(&maxMB, "max-upload-mb", 256, "max upload size in MB")
	flag.IntVar(&maxAgeDays, "max-age", 0, "auto-delete files after N days (0 = keep forever)")
	flag.Parse()
	cfg.MaxUploadSize = int64(maxMB) << 20
	cfg.MaxAge = time.Duration(maxAgeDays) * 24 * time.Hour

	store, err := NewStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	srv := NewServer(cfg, store)
	if cfg.MaxAge > 0 {
		go store.CleanupLoop(cfg.MaxAge)
		log.Printf("cleanup: files older than %d days will be removed", maxAgeDays)
	}
	log.Printf("filebin listening on %s", cfg.Addr)
	log.Fatal(http.ListenAndServe(cfg.Addr, srv))
}

func NewServer(cfg Config, store *Store) *Server {
	s := &Server{cfg: cfg, store: store}
	s.chromaCSS = template.CSS(generateChromaCSS())
	s.loadTemplates()
	s.registerRoutes()
	return s
}

func (s *Server) loadTemplates() {
	fm := template.FuncMap{
		"formatBytes": formatBytes,
		"formatDate":  formatDate,
		"isImage":     isImage,
		"isVideo":     isVideo,
		"isAudio":     isAudio,
		"isText":      isText,
		"isAsciinema": isAsciinema,
		"rawURL":      func(id string) string { return "/" + id + "/raw" },
		"chromaCSS":   func() template.CSS { return s.chromaCSS },
	}
	shared := []string{"templates/header.html", "templates/footer.html"}
	s.tmpls = make(map[string]*template.Template)
	for _, page := range []string{"upload", "paste", "media", "history", "gallery", "info", "collection", "login", "password", "error"} {
		files := append([]string{"templates/" + page + ".html"}, shared...)
		s.tmpls[page] = template.Must(template.New("").Funcs(fm).ParseFS(templateFS, files...))
	}
}

func (s *Server) registerRoutes() {
	s.mux = http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))

	// Web
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /login", s.handleLoginPage)
	s.mux.HandleFunc("POST /login", s.handleLogin)
	s.mux.HandleFunc("GET /logout", s.handleLogout)
	s.mux.HandleFunc("GET /history", s.handleHistory)
	s.mux.HandleFunc("GET /gallery", s.handleGallery)
	s.mux.HandleFunc("POST /upload", s.handleWebUpload)
	s.mux.HandleFunc("POST /delete", s.handleWebDelete)
	s.mux.HandleFunc("POST /collection", s.handleWebCollection)

	// API
	s.mux.HandleFunc("POST /api/upload", s.handleAPIUpload)
	s.mux.HandleFunc("GET /api/history", s.handleAPIHistory)
	s.mux.HandleFunc("DELETE /api/{id}", s.handleAPIDelete)
	s.mux.HandleFunc("POST /api/collection", s.handleAPICollection)
	s.mux.HandleFunc("GET /api/config", s.handleAPIConfig)

	// Catch-all: file/collection view
	s.mux.HandleFunc("GET /{rest...}", s.handleView)
	s.mux.HandleFunc("POST /{rest...}", s.handleView)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Helpers

func formatBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	d, e := int64(u), 0
	for n := b / u; n >= u; n /= u {
		d *= u
		e++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(d), "KMGTPE"[e])
}

func formatDate(unix int64) string {
	return time.Unix(unix, 0).Format("2006-01-02 15:04")
}

func isImage(m string) bool  { return strings.HasPrefix(m, "image/") }
func isVideo(m string) bool  { return strings.HasPrefix(m, "video/") }
func isAudio(m string) bool  { return strings.HasPrefix(m, "audio/") }

func isText(m string) bool {
	if strings.HasPrefix(m, "text/") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/javascript",
		"application/x-sh", "application/x-shellscript", "application/toml",
		"application/yaml", "application/x-yaml", "application/x-httpd-php":
		return true
	}
	return false
}

func isAsciinema(name string) bool {
	return strings.HasSuffix(name, ".cast") || strings.HasSuffix(name, ".asciinema.json")
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

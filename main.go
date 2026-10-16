package main

import (
	"crypto/rand"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

const maxFilesPerRequest = 20

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
	lexers    []LexerOpt
	secret    []byte
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
	go store.CleanupLoop(10 * time.Minute)
	if cfg.MaxAge > 0 {
		log.Printf("cleanup: files older than %d days will be removed", maxAgeDays)
	}
	log.Printf("filebin listening on %s", cfg.Addr)
	log.Fatal(http.ListenAndServe(cfg.Addr, srv))
}

func NewServer(cfg Config, store *Store) *Server {
	store.MaxAge = cfg.MaxAge
	s := &Server{cfg: cfg, store: store}
	var err error
	if s.secret, err = loadSecret(filepath.Join(store.dir, "secret")); err != nil {
		log.Fatalf("server secret: %v", err)
	}
	s.chromaCSS = template.CSS(generateChromaCSS())
	s.lexers = lexerOptions()
	s.loadTemplates()
	s.registerRoutes()
	if files, err := store.ListFiles(); err == nil {
		s.pregenThumbs(files...)
	}
	return s
}

// loadSecret reads the persistent HMAC secret, creating it on first start.
func loadSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, os.WriteFile(path, b, 0600)
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
		"canThumb":    canThumb,
		"typeLabel":   typeLabel,
		"fileKind":    fileKind,
		"expiresIn":   expiresIn,
		"expiryLabel": func(v string) string {
			return map[string]string{"1h": "1 hour", "1d": "1 day", "1w": "1 week", "1m": "1 month", "never": "Never"}[v]
		},
		"effExpiry": s.store.EffectiveExpiry,
		"rawURL":    func(id string) string { return "/" + id + "/raw" },
		"chromaCSS": func() template.CSS { return s.chromaCSS },
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[kv[i].(string)] = kv[i+1]
			}
			return m
		},
	}
	shared := []string{"templates/header.html", "templates/footer.html", "templates/partials.html"}
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
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})

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
	s.mux.HandleFunc("GET /api/config", s.handleAPIConfig)
	s.mux.HandleFunc("POST /api/collection", s.handleAPICollection)
	s.mux.HandleFunc("GET /api/{id}", s.handleAPIGet)
	s.mux.HandleFunc("PATCH /api/{id}", s.handleAPIPatch)
	s.mux.HandleFunc("DELETE /api/{id}", s.handleAPIDelete)

	// Catch-all: file/collection view
	s.mux.HandleFunc("GET /{rest...}", s.handleView)
	s.mux.HandleFunc("POST /{rest...}", s.handleView)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
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

// expiresIn renders a short relative expiry like "3d", "5h", "12m" ("" = never).
func expiresIn(unix int64) string {
	if unix == 0 {
		return ""
	}
	d := time.Until(time.Unix(unix, 0))
	switch {
	case d <= time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func baseMime(m string) string {
	if mt, _, err := mime.ParseMediaType(m); err == nil {
		return mt
	}
	return strings.TrimSpace(strings.SplitN(m, ";", 2)[0])
}

func isImage(m string) bool { return strings.HasPrefix(m, "image/") }
func isVideo(m string) bool { return strings.HasPrefix(m, "video/") }
func isAudio(m string) bool { return strings.HasPrefix(m, "audio/") }

func canThumb(m string) bool {
	switch baseMime(m) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

func isText(m string) bool {
	m = baseMime(m)
	if strings.HasPrefix(m, "text/") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/javascript",
		"application/x-sh", "application/x-shellscript", "application/toml",
		"application/yaml", "application/x-yaml", "application/x-httpd-php", "application/sql":
		return true
	}
	return false
}

func isAsciinema(name string) bool {
	return strings.HasSuffix(name, ".cast") || strings.HasSuffix(name, ".asciinema.json")
}

// fileKind buckets a file for filtering: image, video, audio, text or other.
func fileKind(m string) string {
	switch {
	case isImage(m):
		return "image"
	case isVideo(m):
		return "video"
	case isAudio(m):
		return "audio"
	case isText(m):
		return "text"
	}
	return "other"
}

var typeLabels = map[string]string{
	"image/jpeg": "JPEG image", "image/png": "PNG image", "image/gif": "GIF image", "image/webp": "WebP image",
	"image/svg+xml": "SVG image", "image/x-icon": "Icon", "image/vnd.microsoft.icon": "Icon", "image/avif": "AVIF image",
	"video/mp4": "MP4 video", "video/webm": "WebM video", "video/x-matroska": "MKV video", "video/quicktime": "QuickTime video",
	"audio/mpeg": "MP3 audio", "audio/ogg": "Ogg audio", "audio/flac": "FLAC audio", "audio/wav": "WAV audio",
	"audio/x-wav": "WAV audio", "audio/opus": "Opus audio", "audio/mp4": "M4A audio", "audio/aac": "AAC audio",
	"text/plain": "Text", "text/markdown": "Markdown", "text/html": "HTML", "text/css": "CSS", "text/csv": "CSV",
	"text/javascript": "JavaScript", "application/javascript": "JavaScript", "text/typescript": "TypeScript",
	"text/tsx": "TSX", "text/jsx": "JSX", "text/x-go": "Go source", "text/x-rust": "Rust source",
	"text/x-python": "Python script", "text/x-c": "C source", "text/xml": "XML", "application/xml": "XML",
	"application/x-sh": "Shell script", "application/x-shellscript": "Shell script", "text/x-sh": "Shell script",
	"application/json": "JSON", "application/toml": "TOML", "application/yaml": "YAML", "application/x-yaml": "YAML",
	"application/pdf": "PDF document", "application/zip": "ZIP archive", "application/x-tar": "Tar archive",
	"application/gzip": "Gzip archive", "application/x-gzip": "Gzip archive", "application/x-xz": "XZ archive",
	"application/zstd": "Zstd archive", "application/x-7z-compressed": "7z archive", "application/vnd.rar": "RAR archive",
	"application/x-asciicast": "Asciinema recording", "application/octet-stream": "Binary",
	"application/x-httpd-php": "PHP script", "application/sql": "SQL", "application/epub+zip": "EPUB book",
}

// typeLabel turns a MIME type into a short human readable label.
func typeLabel(m string) string {
	b := baseMime(m)
	if l, ok := typeLabels[b]; ok {
		return l
	}
	major, sub, _ := strings.Cut(b, "/")
	sub = strings.TrimPrefix(strings.TrimPrefix(sub, "x-"), "vnd.")
	switch major {
	case "image", "video", "audio":
		return strings.ToUpper(sub) + " " + major
	case "text":
		return "Text (" + sub + ")"
	}
	if sub == "" {
		return "File"
	}
	return sub
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

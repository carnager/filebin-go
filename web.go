package main

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

type PageData struct {
	Title     string
	LoggedIn  bool
	Flash     string
	FlashType string

	File      *FileMeta
	Highlight template.HTML
	ChromaCSS template.CSS
	LexerName string
	RawURL    string
	MediaType string // video, audio, asciinema, image

	Collection *Collection
	ColFiles   []*FileMeta

	Files       []*FileMeta
	Collections []*Collection
	TotalSize   int64
}

func (s *Server) render(w http.ResponseWriter, name string, d PageData) {
	t, ok := s.tmpls[name]
	if !ok {
		http.Error(w, "template not found", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name+".html", d); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func (s *Server) renderErr(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	s.render(w, "error", PageData{Title: "Error", Flash: msg})
}

func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie("apikey")
	return err == nil && s.store.ValidAPIKey(c.Value)
}

func (s *Server) requireLogin(w http.ResponseWriter, r *http.Request) bool {
	if s.loggedIn(r) {
		return true
	}
	http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
	return false
}

// --- Password protection ---

// hasUnlock checks if the user has a cookie granting access to this specific ID.
func (s *Server) hasUnlock(r *http.Request, id string) bool {
	c, err := r.Cookie("unlock_" + id)
	return err == nil && c.Value == "1"
}

// hasCollectionUnlock checks if a file is unlocked via a collection password.
func (s *Server) hasCollectionUnlock(r *http.Request, meta *FileMeta) bool {
	cols, _ := s.store.ListCollections()
	for _, col := range cols {
		for _, fid := range col.Files {
			if fid == meta.ID {
				if s.hasUnlock(r, col.ID) {
					return true
				}
			}
		}
	}
	return false
}

// handlePasswordCheck shows the password form or validates the submitted password.
// Returns true if access is granted (caller should continue), false if blocked (response already sent).
func (s *Server) handlePasswordCheck(w http.ResponseWriter, r *http.Request, id string, check func(string) bool) bool {
	// API clients: check X-Password header or ?password= query
	if !wantsHTML(r) {
		pw := r.Header.Get("X-Password")
		if pw == "" {
			pw = r.URL.Query().Get("password")
		}
		if pw != "" && check(pw) {
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "password required"})
		return false
	}

	if r.Method == http.MethodPost {
		pw := r.FormValue("password")
		if pw != "" && check(pw) {
			http.SetCookie(w, &http.Cookie{
				Name: "unlock_" + id, Value: "1", Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
				MaxAge: 86400, // 24 hours
			})
			http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
			return false
		}
		s.render(w, "password", PageData{Title: "Password Required", Flash: "Wrong password", FlashType: "error"})
		return false
	}
	s.render(w, "password", PageData{Title: "Password Required"})
	return false
}

// --- Pages ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, "upload", PageData{LoggedIn: s.loggedIn(r)})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", PageData{Title: "Login"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.FormValue("apikey"))
	if !s.store.ValidAPIKey(key) {
		s.render(w, "login", PageData{Title: "Login", Flash: "Invalid API key", FlashType: "error"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "apikey", Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	next := r.FormValue("next")
	if next == "" {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "apikey", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- View (catch-all) ---

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	if rest == "" {
		s.renderErr(w, "Not Found", 404)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	suffix := ""
	if len(parts) > 1 {
		suffix = parts[1]
	}

	// Collection?
	if col, err := s.store.GetCollection(id); err == nil {
		if col.IsProtected() && !s.hasUnlock(r, id) {
			if !s.handlePasswordCheck(w, r, id, col.CheckPassword) {
				return
			}
		}
		s.viewCollection(w, r, col, suffix)
		return
	}

	// File?
	meta, err := s.store.GetFile(id)
	if err != nil {
		s.renderErr(w, "Not Found", 404)
		return
	}

	// Check file password (skip if unlocked via collection)
	if meta.IsProtected() && !s.hasUnlock(r, id) && !s.hasCollectionUnlock(r, meta) {
		if !s.handlePasswordCheck(w, r, id, meta.CheckPassword) {
			return
		}
	}

	path := s.store.FilePath(meta.Hash)

	switch suffix {
	case "raw":
		s.serveRaw(w, r, meta, path)
	case "info":
		s.render(w, "info", PageData{Title: meta.Name + " — Info", File: meta, LoggedIn: s.loggedIn(r)})
	case "":
		s.viewFile(w, r, meta, path)
	default:
		// treat suffix as language override
		s.renderPaste(w, r, meta, path, suffix)
	}
}

func (s *Server) viewFile(w http.ResponseWriter, r *http.Request, meta *FileMeta, path string) {
	// Non-browser → raw
	if !wantsHTML(r) {
		s.serveRaw(w, r, meta, path)
		return
	}
	switch {
	case isAsciinema(meta.Name):
		s.renderMedia(w, r, meta, "asciinema")
	case isVideo(meta.Mimetype):
		s.renderMedia(w, r, meta, "video")
	case isAudio(meta.Mimetype):
		s.renderMedia(w, r, meta, "audio")
	case isImage(meta.Mimetype):
		s.renderMedia(w, r, meta, "image")
	case isText(meta.Mimetype):
		s.renderPaste(w, r, meta, path, "")
	default:
		s.serveRaw(w, r, meta, path)
	}
}

func (s *Server) serveRaw(w http.ResponseWriter, r *http.Request, meta *FileMeta, path string) {
	w.Header().Set("Content-Type", meta.Mimetype)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, meta.Name))
	http.ServeFile(w, r, path)
}

func (s *Server) renderMedia(w http.ResponseWriter, r *http.Request, meta *FileMeta, kind string) {
	s.render(w, "media", PageData{
		Title: meta.Name, File: meta, MediaType: kind,
		RawURL: "/" + meta.ID + "/raw", LoggedIn: s.loggedIn(r),
	})
}

func (s *Server) renderPaste(w http.ResponseWriter, r *http.Request, meta *FileMeta, path, lang string) {
	content, err := os.ReadFile(path)
	if err != nil {
		s.renderErr(w, "read error", 500)
		return
	}
	const maxHL = 2 << 20
	text := string(content)
	if len(content) > maxHL {
		text = string(content[:maxHL]) + "\n\n... (truncated) ..."
	}
	hl, lexName := highlightCode(text, meta.Name, lang)
	s.render(w, "paste", PageData{
		Title: meta.Name, File: meta,
		Highlight: template.HTML(hl), ChromaCSS: s.chromaCSS, LexerName: lexName,
		RawURL: "/" + meta.ID + "/raw", LoggedIn: s.loggedIn(r),
	})
}

// --- Collection view ---

func (s *Server) viewCollection(w http.ResponseWriter, r *http.Request, col *Collection, suffix string) {
	switch suffix {
	case "tar":
		s.serveCollectionTar(w, col)
		return
	case "info":
		files := s.resolveFiles(col.Files)
		s.render(w, "info", PageData{Title: col.ID + " — Info", Collection: col, ColFiles: files, LoggedIn: s.loggedIn(r)})
		return
	}

	files := s.resolveFiles(col.Files)
	if !wantsHTML(r) && len(files) > 0 {
		p := s.store.FilePath(files[0].Hash)
		s.serveRaw(w, r, files[0], p)
		return
	}
	s.render(w, "collection", PageData{Title: col.ID, Collection: col, ColFiles: files, LoggedIn: s.loggedIn(r)})
}

func (s *Server) resolveFiles(ids []string) []*FileMeta {
	var out []*FileMeta
	for _, id := range ids {
		if m, err := s.store.GetFile(id); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func (s *Server) serveCollectionTar(w http.ResponseWriter, col *Collection) {
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, col.ID+".tar"))
	tw := tar.NewWriter(w)
	defer tw.Close()
	for _, fid := range col.Files {
		meta, err := s.store.GetFile(fid)
		if err != nil {
			continue
		}
		info, err := os.Stat(s.store.FilePath(meta.Hash))
		if err != nil {
			continue
		}
		tw.WriteHeader(&tar.Header{Name: meta.Name, Size: info.Size(), Mode: 0644})
		f, err := os.Open(s.store.FilePath(meta.Hash))
		if err != nil {
			continue
		}
		io.Copy(tw, f)
		f.Close()
	}
}

// --- Web uploads & actions ---

func (s *Server) handleWebUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	r.ParseMultipartForm(s.cfg.MaxUploadSize + (1 << 20))

	password := r.FormValue("password")
	var ids []string

	// Text paste
	if content := r.FormValue("content"); content != "" {
		name := r.FormValue("name")
		if name == "" {
			name = "paste.txt"
		}
		meta, err := s.store.SaveFileFromBytes([]byte(content), name, 3, 6, password)
		if err != nil {
			s.renderErr(w, err.Error(), 500)
			return
		}
		ids = append(ids, meta.ID)
	}

	// File uploads
	if r.MultipartForm != nil {
		for _, headers := range r.MultipartForm.File {
			for _, fh := range headers {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				meta, err := s.store.SaveFile(f, fh.Filename, 3, 6, password)
				f.Close()
				if err != nil {
					s.renderErr(w, err.Error(), 500)
					return
				}
				ids = append(ids, meta.ID)
			}
		}
	}

	if len(ids) == 0 {
		s.renderErr(w, "nothing uploaded", 400)
		return
	}
	if len(ids) == 1 {
		http.Redirect(w, r, "/"+ids[0], http.StatusSeeOther)
		return
	}
	col, err := s.store.SaveCollection(ids, 3, 6, password)
	if err != nil {
		s.renderErr(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/"+col.ID, http.StatusSeeOther)
}

func (s *Server) handleWebDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	r.ParseForm()
	deleteFiles := r.FormValue("delete_files") == "1"
	for _, id := range r.Form["ids"] {
		if s.store.FileExists(id) {
			s.store.DeleteFile(id)
		} else if s.store.CollectionExists(id) {
			if deleteFiles {
				if col, err := s.store.GetCollection(id); err == nil {
					for _, fid := range col.Files {
						s.store.DeleteFile(fid)
					}
				}
			}
			s.store.DeleteCollection(id)
		}
	}
	http.Redirect(w, r, "/history", http.StatusSeeOther)
}

func (s *Server) handleWebCollection(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	r.ParseForm()
	ids := r.Form["ids"]
	if len(ids) == 0 {
		s.renderErr(w, "no files selected", 400)
		return
	}
	_, err := s.store.SaveCollection(ids, 3, 6)
	if err != nil {
		s.renderErr(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/history", http.StatusSeeOther)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	files, _ := s.store.ListFiles()
	cols, _ := s.store.ListCollections()
	var total int64
	seen := map[string]bool{}
	for _, f := range files {
		if !seen[f.Hash] {
			total += f.Size
			seen[f.Hash] = true
		}
	}
	s.render(w, "history", PageData{
		Title: "History", LoggedIn: true,
		Files: files, Collections: cols, TotalSize: total,
	})
}

func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	all, _ := s.store.ListFiles()
	var imgs []*FileMeta
	for _, f := range all {
		if isImage(f.Mimetype) {
			imgs = append(imgs, f)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "gallery", PageData{Title: "Gallery", LoggedIn: true, Files: imgs})
}

// --- Syntax highlighting ---

func highlightCode(code, filename, lang string) (string, string) {
	var l chroma.Lexer
	if lang != "" {
		l = lexers.Get(lang)
	}
	if l == nil {
		l = lexers.Match(filename)
	}
	if l == nil {
		l = lexers.Analyse(code)
	}
	if l == nil {
		l = lexers.Fallback
	}
	l = chroma.Coalesce(l)

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}
	f := chromahtml.New(chromahtml.WithClasses(true), chromahtml.WithLineNumbers(true), chromahtml.WithLinkableLineNumbers(true, "L"))

	it, err := l.Tokenise(nil, code)
	if err != nil {
		return template.HTMLEscapeString(code), "text"
	}
	var buf strings.Builder
	if err := f.Format(&buf, style, it); err != nil {
		return template.HTMLEscapeString(code), "text"
	}
	return buf.String(), l.Config().Name
}

func generateChromaCSS() string {
	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}
	var buf strings.Builder
	chromahtml.New(chromahtml.WithClasses(true)).WriteCSS(&buf, style)
	return buf.String()
}

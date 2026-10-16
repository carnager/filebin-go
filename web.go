package main

import (
	"archive/tar"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	qrcode "github.com/skip2/go-qrcode"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

type OG struct {
	Title, Description, Image, URL, Card string
}

// Share drives the share bar partial.
type Share struct {
	ID, Name, Raw, Download string
	Collection              bool
}

type LexerOpt struct{ Name, Alias string }

// FileItem is a file plus whether the viewer may see an inline preview.
type FileItem struct {
	*FileMeta
	Preview bool
}

type HistItem struct {
	ID, Name, Kind, Type          string
	Size, Date, Expires           int64
	Count                         int
	Burn, Protected, IsCol, Thumb bool
}

type PageData struct {
	Title     string
	LoggedIn  bool
	Flash     string
	FlashType string
	CSRFToken string
	Next      string
	OG        *OG
	Share     *Share
	Locked    string

	File      *FileMeta
	Highlight template.HTML
	Markdown  template.HTML
	ChromaCSS template.CSS
	LexerName string
	Lexers    []LexerOpt
	RawURL    string
	MediaType string // video, audio, asciinema, image
	Burned    bool
	Expires   int64

	Collection *Collection
	ColFiles   []*FileMeta
	ColItems   []FileItem
	ColSize    int64

	Files       []*FileMeta
	Collections []*Collection
	Items       []HistItem
	TotalSize   int64
	NFiles      int
	NCols       int

	MaxUpload     int64
	MaxFiles      int
	ExpiryOptions []string
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, d PageData) {
	s.renderCode(w, r, name, d, http.StatusOK)
}

func (s *Server) renderCode(w http.ResponseWriter, r *http.Request, name string, d PageData, code int) {
	t, ok := s.tmpls[name]
	if !ok {
		http.Error(w, "template not found", 500)
		return
	}
	if r != nil {
		d.LoggedIn = d.LoggedIn || s.loggedIn(r)
		if d.LoggedIn && d.CSRFToken == "" {
			d.CSRFToken = s.ensureCSRF(w, r)
		}
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name+".html", d); err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "template error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
}

func (s *Server) renderErr(w http.ResponseWriter, r *http.Request, msg string, code int) {
	s.renderCode(w, r, "error", PageData{Title: "Error", Flash: msg}, code)
}

func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie("apikey")
	return err == nil && s.store.ValidAPIKey(c.Value)
}

func isXHR(r *http.Request) bool { return r.Header.Get("X-Requested-With") == "XMLHttpRequest" }

func (s *Server) requireLogin(w http.ResponseWriter, r *http.Request) bool {
	if s.loggedIn(r) {
		return true
	}
	if isXHR(r) {
		apiErr(w, "not logged in", http.StatusUnauthorized)
		return false
	}
	next := "/"
	if r.Method == http.MethodGet {
		next = r.URL.RequestURI()
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
	return false
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.ContainsAny(next, "\r\n") {
		return "/"
	}
	return next
}

func isSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) {
	c.Path, c.HttpOnly, c.SameSite, c.Secure = "/", true, http.SameSiteLaxMode, isSecure(r)
	http.SetCookie(w, c)
}

// --- CSRF protection ---

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("csrf"); err == nil && len(c.Value) == 64 {
		return c.Value
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	s.setCookie(w, r, &http.Cookie{Name: "csrf", Value: token})
	return token
}

func (s *Server) validateCSRF(r *http.Request) bool {
	c, err := r.Cookie("csrf")
	if err != nil || len(c.Value) != 64 {
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf_token")
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1
}

// --- Password protection ---

func (s *Server) unlockValue(id string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(id))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) hasUnlock(r *http.Request, id string) bool {
	c, err := r.Cookie("unlock_" + id)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.unlockValue(id))) == 1
}

// hasCollectionUnlock checks if a file is unlocked via a collection password.
func (s *Server) hasCollectionUnlock(r *http.Request, meta *FileMeta) bool {
	for _, cid := range s.store.CollectionsWith(meta.ID) {
		if s.hasUnlock(r, cid) {
			return true
		}
	}
	return false
}

func (s *Server) canSee(r *http.Request, m *FileMeta, owner bool) bool {
	return owner || !m.IsProtected() || s.hasUnlock(r, m.ID) || s.hasCollectionUnlock(r, m)
}

// handlePasswordCheck shows the password form or validates the submitted password.
// Returns true if access is granted (caller should continue), false if blocked (response already sent).
func (s *Server) handlePasswordCheck(w http.ResponseWriter, r *http.Request, id, label string, check func(string) bool) bool {
	if !wantsHTML(r) {
		pw := r.Header.Get("X-Password")
		if pw == "" {
			pw = r.URL.Query().Get("password")
		}
		if pw != "" && check(pw) {
			return true
		}
		apiErr(w, "password required", http.StatusForbidden)
		return false
	}
	d := PageData{Title: "Password Required", Locked: label, OG: &OG{Title: "Password protected"}}
	if r.Method == http.MethodPost {
		if !s.validateCSRF(r) {
			s.renderErr(w, r, "Invalid request", http.StatusForbidden)
			return false
		}
		if pw := r.FormValue("password"); pw != "" && check(pw) {
			s.setCookie(w, r, &http.Cookie{Name: "unlock_" + id, Value: s.unlockValue(id), MaxAge: 86400})
			http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
			return false
		}
		d.Flash, d.FlashType = "Wrong password", "error"
	}
	d.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, r, "password", d)
	return false
}

// isPreviewBot detects link unfurlers so they don't consume burn-after-reading items.
func isPreviewBot(r *http.Request) bool {
	ua := strings.ToLower(r.UserAgent())
	for _, b := range []string{"bot", "facebookexternalhit", "slack", "discord", "telegram", "whatsapp", "preview", "embedly", "skype", "mastodon", "matrix"} {
		if strings.Contains(ua, b) {
			return true
		}
	}
	return false
}

// --- Pages ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "upload", PageData{CSRFToken: s.ensureCSRF(w, r), MaxUpload: s.cfg.MaxUploadSize,
		MaxFiles: maxFilesPerRequest, ExpiryOptions: expiryOptions})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login", PageData{Title: "Login", Next: safeNext(r.URL.Query().Get("next")), CSRFToken: s.ensureCSRF(w, r)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.validateCSRF(r) {
		s.renderErr(w, r, "Invalid request", http.StatusForbidden)
		return
	}
	key := strings.TrimSpace(r.FormValue("apikey"))
	next := safeNext(r.FormValue("next"))
	if !s.store.ValidAPIKey(key) {
		s.render(w, r, "login", PageData{Title: "Login", Flash: "Invalid API key", FlashType: "error", CSRFToken: s.ensureCSRF(w, r), Next: next})
		return
	}
	c := &http.Cookie{Name: "apikey", Value: key}
	if r.FormValue("remember") != "" {
		c.MaxAge = 30 * 86400
	}
	s.setCookie(w, r, c)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, r, &http.Cookie{Name: "apikey", Value: "", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- View (catch-all) ---

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	if rest == "" {
		s.renderErr(w, r, "Not Found", 404)
		return
	}
	id, suffix, _ := strings.Cut(rest, "/")
	owner := s.isOwner(r)

	if suffix == "qr.png" {
		if !s.store.FileExists(id) && !s.store.CollectionExists(id) {
			http.NotFound(w, r)
			return
		}
		png, err := qrcode.Encode(baseURL(r)+"/"+id, qrcode.Medium, 320)
		if err != nil {
			http.Error(w, "qr error", 500)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(png)
		return
	}

	if col, err := s.store.GetCollection(id); err == nil {
		if col.IsProtected() && !owner && !s.hasUnlock(r, id) {
			if !s.handlePasswordCheck(w, r, id, "Collection "+col.Title(), col.CheckPassword) {
				return
			}
		}
		s.viewCollection(w, r, col, suffix, owner)
		return
	}

	meta, err := s.store.GetFile(id)
	if err != nil {
		s.renderErr(w, r, "Not Found", 404)
		return
	}
	if !s.canSee(r, meta, owner) {
		if !s.handlePasswordCheck(w, r, id, meta.Name, meta.CheckPassword) {
			return
		}
	}
	burn := meta.Burn && !owner && r.Method == http.MethodGet
	if burn && suffix != "info" && isPreviewBot(r) {
		s.render(w, r, "error", PageData{Title: "filebin", Flash: "This item can only be viewed once.", OG: &OG{Title: "filebin"}})
		return
	}

	switch suffix {
	case "raw":
		s.serveRaw(w, r, meta, burn)
	case "thumb":
		s.serveThumb(w, r, meta, owner)
	case "info":
		s.render(w, r, "info", PageData{Title: meta.Name + " — Info", File: meta, Share: fileShare(meta),
			Expires: s.store.EffectiveExpiry(meta.Date, meta.Expires)})
	case "":
		s.viewFile(w, r, meta, burn)
	default:
		s.renderPaste(w, r, meta, suffix, burn)
	}
}

func fileShare(m *FileMeta) *Share {
	return &Share{ID: m.ID, Name: m.Name, Raw: "/" + m.ID + "/raw", Download: "/" + m.ID + "/raw?download=1"}
}

func (s *Server) viewFile(w http.ResponseWriter, r *http.Request, meta *FileMeta, burn bool) {
	if !wantsHTML(r) {
		s.serveRaw(w, r, meta, burn)
		return
	}
	switch {
	case isAsciinema(meta.Name):
		s.renderMedia(w, r, meta, "asciinema", burn)
	case isVideo(meta.Mimetype):
		s.renderMedia(w, r, meta, "video", burn)
	case isAudio(meta.Mimetype):
		s.renderMedia(w, r, meta, "audio", burn)
	case isImage(meta.Mimetype):
		s.renderMedia(w, r, meta, "image", burn)
	case isText(meta.Mimetype):
		s.renderPaste(w, r, meta, "", burn)
	default:
		s.serveRaw(w, r, meta, burn)
	}
}

func contentDisposition(disp, name string) string {
	if v := mime.FormatMediaType(disp, map[string]string{"filename": name}); v != "" {
		return v
	}
	return disp
}

// serveRaw serves the file content; burn items are deleted atomically and served without range support.
func (s *Server) serveRaw(w http.ResponseWriter, r *http.Request, meta *FileMeta, burn bool) {
	f, err := os.Open(s.store.FilePath(meta.Hash))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	disp := "inline"
	if truthy(r.URL.Query().Get("download")) {
		disp = "attachment"
	}
	w.Header().Set("Content-Type", meta.Mimetype)
	w.Header().Set("Content-Disposition", contentDisposition(disp, meta.Name))
	if needsSandbox(meta.Mimetype) {
		w.Header().Set("Content-Security-Policy", rawCSP)
	}
	if burn {
		if s.store.DeleteFile(meta.ID) != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", fmt.Sprint(meta.Size))
		io.Copy(w, f)
		return
	}
	if meta.IsProtected() {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// rawCSP renders uploaded HTML/SVG/etc. in an opaque origin with no scripts,
// so it can't read cookies or act on behalf of the logged-in owner.
const rawCSP = "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; font-src 'self' data:"

// needsSandbox is false only for types browsers render passively; Chrome's PDF
// viewer refuses to load under a sandbox CSP, so PDFs are exempt too.
func needsSandbox(mt string) bool {
	mt = strings.ToLower(strings.TrimSpace(strings.SplitN(mt, ";", 2)[0]))
	switch {
	case mt == "image/svg+xml":
		return true
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "video/"),
		strings.HasPrefix(mt, "audio/"), mt == "application/pdf":
		return false
	}
	return true
}

func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request, meta *FileMeta, owner bool) {
	if meta.Burn && !owner {
		http.NotFound(w, r)
		return
	}
	p, err := s.thumbnail(meta)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) fileOG(r *http.Request, m *FileMeta, text string) *OG {
	if m.IsProtected() || m.Burn {
		return &OG{Title: "filebin"}
	}
	og := &OG{Title: m.Name, URL: baseURL(r) + "/" + m.ID, Card: "summary",
		Description: typeLabel(m.Mimetype) + " · " + formatBytes(m.Size)}
	if text != "" {
		og.Description = excerpt(text, 200)
	}
	if canThumb(m.Mimetype) {
		og.Image, og.Card = baseURL(r)+"/"+m.ID+"/thumb", "summary_large_image"
	}
	return og
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func (s *Server) renderMedia(w http.ResponseWriter, r *http.Request, meta *FileMeta, kind string, burn bool) {
	s.render(w, r, "media", PageData{
		Title: meta.Name, File: meta, MediaType: kind, Burned: burn, Share: fileShare(meta),
		RawURL: "/" + meta.ID + "/raw", OG: s.fileOG(r, meta, ""),
		Expires: s.store.EffectiveExpiry(meta.Date, meta.Expires),
	})
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func isMarkdown(m *FileMeta) bool {
	ext := strings.ToLower(filepath.Ext(m.Name))
	return baseMime(m.Mimetype) == "text/markdown" || ext == ".md" || ext == ".markdown"
}

func (s *Server) renderPaste(w http.ResponseWriter, r *http.Request, meta *FileMeta, lang string, burn bool) {
	content, err := os.ReadFile(s.store.FilePath(meta.Hash))
	if err != nil {
		s.renderErr(w, r, "read error", 500)
		return
	}
	if burn && s.store.DeleteFile(meta.ID) != nil {
		s.renderErr(w, r, "Not Found", 404)
		return
	}
	const maxHL = 2 << 20
	text := string(content)
	if len(content) > maxHL {
		text = string(content[:maxHL]) + "\n\n... (truncated) ..."
	}
	hl, lexName := highlightCode(text, meta.Name, lang)
	d := PageData{
		Title: meta.Name, File: meta, Burned: burn, Share: fileShare(meta),
		Highlight: template.HTML(hl), ChromaCSS: s.chromaCSS, LexerName: lexName, Lexers: s.lexers,
		RawURL: "/" + meta.ID + "/raw", OG: s.fileOG(r, meta, text),
		Expires: s.store.EffectiveExpiry(meta.Date, meta.Expires),
	}
	if lang == "" && isMarkdown(meta) {
		var buf bytes.Buffer
		if md.Convert([]byte(text), &buf) == nil {
			d.Markdown = template.HTML(buf.String())
		}
	}
	s.render(w, r, "paste", d)
}

// --- Collection view ---

func (s *Server) viewCollection(w http.ResponseWriter, r *http.Request, col *Collection, suffix string, owner bool) {
	files := s.resolveFiles(col.Files)
	burn := col.Burn && !owner && r.Method == http.MethodGet
	share := &Share{ID: col.ID, Name: col.Name, Download: "/" + col.ID + "/tar", Collection: true}
	switch suffix {
	case "tar":
		if burn && s.store.DeleteCollection(col.ID) != nil {
			http.NotFound(w, r)
			return
		}
		s.serveCollectionTar(w, col, files)
		return
	case "info":
		s.render(w, r, "info", PageData{Title: col.Title() + " — Info", Collection: col, ColFiles: files, Share: share,
			ColSize: sumSize(files), Expires: s.store.EffectiveExpiry(col.Date, col.Expires)})
		return
	case "":
	default:
		s.renderErr(w, r, "Not Found", 404)
		return
	}
	if !wantsHTML(r) && len(files) > 0 {
		s.serveRaw(w, r, files[0], files[0].Burn && !owner)
		return
	}
	if burn && isPreviewBot(r) {
		s.render(w, r, "error", PageData{Title: "filebin", Flash: "This item can only be viewed once.", OG: &OG{Title: "filebin"}})
		return
	}
	if burn && s.store.DeleteCollection(col.ID) != nil {
		s.renderErr(w, r, "Not Found", 404)
		return
	}
	items := make([]FileItem, len(files))
	og := &OG{Title: "filebin"}
	if !col.IsProtected() && !col.Burn {
		og = &OG{Title: col.Title(), URL: baseURL(r) + "/" + col.ID, Card: "summary",
			Description: fmt.Sprintf("%d file(s) · %s", len(files), formatBytes(sumSize(files)))}
	}
	for i, f := range files {
		items[i] = FileItem{f, !f.Burn && s.canSee(r, f, owner) || owner}
		if og.URL != "" && og.Image == "" && canThumb(f.Mimetype) && !f.Burn && !f.IsProtected() {
			og.Image, og.Card = baseURL(r)+"/"+f.ID+"/thumb", "summary_large_image"
		}
	}
	s.render(w, r, "collection", PageData{Title: col.Title(), Collection: col, ColFiles: files, ColItems: items,
		ColSize: sumSize(files), Share: share, OG: og, Burned: burn,
		Expires: s.store.EffectiveExpiry(col.Date, col.Expires)})
}

func sumSize(files []*FileMeta) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
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

func (s *Server) serveCollectionTar(w http.ResponseWriter, col *Collection, files []*FileMeta) {
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", col.ID+".tar"))
	tw := tar.NewWriter(w)
	defer tw.Close()
	for _, meta := range files {
		f, err := os.Open(s.store.FilePath(meta.Hash))
		if err != nil {
			continue
		}
		if info, err := f.Stat(); err == nil {
			tw.WriteHeader(&tar.Header{Name: meta.Name, Size: info.Size(), Mode: 0644, ModTime: time.Unix(meta.Date, 0)})
			io.Copy(tw, f)
		}
		f.Close()
	}
}

// --- Web uploads & actions ---

func (s *Server) webFail(w http.ResponseWriter, r *http.Request, msg string, code int) {
	if isXHR(r) {
		apiErr(w, msg, code)
		return
	}
	s.renderErr(w, r, msg, code)
}

func (s *Server) handleWebUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadSize+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.webFail(w, r, "Upload too large (max "+formatBytes(s.cfg.MaxUploadSize)+")", http.StatusRequestEntityTooLarge)
		return
	}
	if !s.validateCSRF(r) {
		s.webFail(w, r, "Invalid request", http.StatusForbidden)
		return
	}
	req, err := parseUploadOpts(r)
	if err != nil {
		s.webFail(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	content := r.FormValue("content")
	var headers []*multipart.FileHeader
	if r.MultipartForm != nil {
		headers = r.MultipartForm.File["file"]
	}
	n := len(headers)
	if content != "" {
		n++
	}
	if n == 0 {
		s.webFail(w, r, "Nothing uploaded", http.StatusBadRequest)
		return
	}
	if n > maxFilesPerRequest {
		s.webFail(w, r, fmt.Sprintf("Too many files (max %d per upload)", maxFilesPerRequest), http.StatusBadRequest)
		return
	}

	var ids []string
	if content != "" {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			name = "paste.txt"
		}
		meta, err := s.store.SaveFileFromBytes([]byte(content), name, req.opts)
		if err != nil {
			log.Printf("save paste: %v", err)
			s.webFail(w, r, "Failed to save paste", 500)
			return
		}
		ids = append(ids, meta.ID)
	}
	metas, err := s.saveUploads(headers, req.opts)
	if err != nil {
		log.Printf("save file: %v", err)
		s.webFail(w, r, "Failed to save file", 500)
		return
	}
	for _, m := range metas {
		ids = append(ids, m.ID)
	}

	target := "/" + ids[0]
	if len(ids) > 1 {
		col, err := s.store.SaveCollection(ids, req.colName, req.opts)
		if err != nil {
			log.Printf("save collection: %v", err)
			s.webFail(w, r, "Failed to create collection", 500)
			return
		}
		target = "/" + col.ID
	}
	if isXHR(r) {
		apiOK(w, map[string]string{"url": target})
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleWebDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	r.ParseForm()
	if !s.validateCSRF(r) {
		s.renderErr(w, r, "Invalid request", http.StatusForbidden)
		return
	}
	withFiles := r.FormValue("delete_files") == "1"
	for _, id := range r.Form["ids"] {
		s.deleteItem(id, withFiles)
	}
	http.Redirect(w, r, "/history", http.StatusSeeOther)
}

func (s *Server) handleWebCollection(w http.ResponseWriter, r *http.Request) {
	if !s.requireLogin(w, r) {
		return
	}
	r.ParseForm()
	if !s.validateCSRF(r) {
		s.renderErr(w, r, "Invalid request", http.StatusForbidden)
		return
	}
	var ids []string
	for _, id := range r.Form["ids"] {
		if s.store.FileExists(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.renderErr(w, r, "No files selected", 400)
		return
	}
	exp, err := parseExpiry(r.FormValue("expires"))
	if err != nil {
		s.renderErr(w, r, err.Error(), 400)
		return
	}
	if _, err := s.store.SaveCollection(ids, strings.TrimSpace(r.FormValue("name")),
		ItemOpts{Password: r.FormValue("password"), Expires: exp}); err != nil {
		s.renderErr(w, r, "Failed to create collection", 400)
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
	var items []HistItem
	for _, f := range files {
		if !seen[f.Hash] {
			total += f.Size
			seen[f.Hash] = true
		}
		items = append(items, HistItem{ID: f.ID, Name: f.Name, Kind: fileKind(f.Mimetype), Type: typeLabel(f.Mimetype),
			Size: f.Size, Date: f.Date, Expires: s.store.EffectiveExpiry(f.Date, f.Expires),
			Burn: f.Burn, Protected: f.IsProtected(), Thumb: canThumb(f.Mimetype)})
	}
	for _, c := range cols {
		members := s.resolveFiles(c.Files)
		items = append(items, HistItem{ID: c.ID, Name: c.Title(), Kind: "collection", Type: "Collection",
			Size: sumSize(members), Date: c.Date, Expires: s.store.EffectiveExpiry(c.Date, c.Expires), Count: len(members),
			Burn: c.Burn, Protected: c.IsProtected(), IsCol: true})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Date > items[j].Date })
	s.render(w, r, "history", PageData{
		Title: "History", LoggedIn: true, Items: items, Collections: cols,
		NFiles: len(files), NCols: len(cols), TotalSize: total, CSRFToken: s.ensureCSRF(w, r),
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
	s.render(w, r, "gallery", PageData{Title: "Gallery", LoggedIn: true, Files: imgs})
}

// --- Syntax highlighting ---

func lexerOptions() []LexerOpt {
	var out []LexerOpt
	for _, l := range lexers.GlobalLexerRegistry.Lexers {
		c := l.Config()
		alias := strings.ToLower(c.Name)
		if len(c.Aliases) > 0 {
			alias = c.Aliases[0]
		}
		if strings.ContainsAny(alias, "/?# ") {
			continue
		}
		out = append(out, LexerOpt{Name: c.Name, Alias: alias})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

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

	f := chromahtml.New(chromahtml.WithClasses(true), chromahtml.WithLineNumbers(true), chromahtml.WithLinkableLineNumbers(true, "L"))
	it, err := l.Tokenise(nil, code)
	if err != nil {
		return template.HTMLEscapeString(code), "text"
	}
	var buf strings.Builder
	if err := f.Format(&buf, styles.Fallback, it); err != nil {
		return template.HTMLEscapeString(code), "text"
	}
	return buf.String(), l.Config().Name
}

// generateChromaCSS emits a dark (monokai) and light (github) theme, scoped like the theme vars in style.css.
func generateChromaCSS() string {
	css := func(name string) string {
		st := styles.Get(name)
		if st == nil {
			st = styles.Fallback
		}
		var buf strings.Builder
		chromahtml.New(chromahtml.WithClasses(true)).WriteCSS(&buf, st)
		return buf.String()
	}
	dark, light := css("monokai"), css("github")
	return scopeCSS(dark, ":root[data-theme=dark]") +
		"@media not all and (prefers-color-scheme:light){\n" + scopeCSS(dark, ":root:not([data-theme=light])") + "}\n" +
		scopeCSS(light, ":root[data-theme=light]") +
		"@media (prefers-color-scheme:light){\n" + scopeCSS(light, ":root:not([data-theme=dark])") + "}\n"
}

func scopeCSS(css, prefix string) string {
	var out strings.Builder
	for _, line := range strings.Split(css, "\n") {
		i := strings.Index(line, "{")
		if i < 0 {
			continue
		}
		head := line[:i]
		if j := strings.LastIndex(head, "*/"); j >= 0 {
			head = head[j+2:]
		}
		var sels []string
		for _, sel := range strings.Split(head, ",") {
			if sel = strings.TrimSpace(sel); sel != "" {
				sels = append(sels, prefix+" "+sel)
			}
		}
		out.WriteString(strings.Join(sels, ",") + line[i:] + "\n")
	}
	return out.String()
}

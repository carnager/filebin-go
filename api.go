package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

var expiryOptions = []string{"1h", "1d", "1w", "1m", "never"}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

// parseExpiry converts "1h", "1d", "1w", "1m", "never"/"" or integer seconds to a unix timestamp (0 = never).
func parseExpiry(v string) (int64, error) {
	d := map[string]time.Duration{"1h": time.Hour, "1d": 24 * time.Hour, "1w": 7 * 24 * time.Hour, "1m": 30 * 24 * time.Hour}
	switch v = strings.TrimSpace(v); v {
	case "", "never", "0":
		return 0, nil
	}
	if dur, ok := d[v]; ok {
		return time.Now().Add(dur).Unix(), nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		return time.Now().Unix() + n, nil
	}
	return 0, fmt.Errorf("invalid expires value %q (use 1h, 1d, 1w, 1m, never or seconds)", v)
}

func truthy(v string) bool { return v == "true" || v == "1" || v == "on" }

// --- API views (never expose password hashes) ---

type FileView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mimetype  string `json:"mimetype"`
	Hash      string `json:"hash"`
	Size      int64  `json:"size"`
	Date      int64  `json:"date"`
	Expires   int64  `json:"expires"`
	Burn      bool   `json:"burn"`
	Protected bool   `json:"protected"`
	URL       string `json:"url"`
	Thumb     bool   `json:"thumb"`
}

type CollectionView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Files     []string `json:"files"`
	Date      int64    `json:"date"`
	Expires   int64    `json:"expires"`
	Protected bool     `json:"protected"`
	URL       string   `json:"url"`
	Size      int64    `json:"size"`
}

func (s *Server) fileView(r *http.Request, m *FileMeta) FileView {
	return FileView{ID: m.ID, Name: m.Name, Mimetype: m.Mimetype, Hash: m.Hash, Size: m.Size, Date: m.Date,
		Expires: s.store.EffectiveExpiry(m.Date, m.Expires), Burn: m.Burn, Protected: m.IsProtected(),
		URL: baseURL(r) + "/" + m.ID, Thumb: canThumb(m.Mimetype)}
}

func (s *Server) colView(r *http.Request, c *Collection) CollectionView {
	var size int64
	for _, f := range s.resolveFiles(c.Files) {
		size += f.Size
	}
	files := c.Files
	if files == nil {
		files = []string{}
	}
	return CollectionView{ID: c.ID, Name: c.Name, Files: files, Date: c.Date,
		Expires: s.store.EffectiveExpiry(c.Date, c.Expires), Protected: c.IsProtected(),
		URL: baseURL(r) + "/" + c.ID, Size: size}
}

// --- Auth ---

// isOwner reports whether the request carries a valid Bearer key or login cookie.
func (s *Server) isOwner(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") && s.store.ValidAPIKey(strings.TrimPrefix(h, "Bearer ")) {
		return true
	}
	return s.loggedIn(r)
}

// apiAuth accepts a Bearer key, or the web login cookie plus a CSRF token for writes.
func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if h == "" && s.loggedIn(r) && (r.Method == http.MethodGet || s.validateCSRF(r)) {
		return true
	}
	if !strings.HasPrefix(h, "Bearer ") {
		apiErr(w, "missing or invalid Authorization header", http.StatusUnauthorized)
		return false
	}
	if !s.store.ValidAPIKey(strings.TrimPrefix(h, "Bearer ")) {
		apiErr(w, "invalid api key", http.StatusForbidden)
		return false
	}
	return true
}

func apiErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func apiOK(w http.ResponseWriter, v any) { apiStatus(w, http.StatusOK, v) }

func apiStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// --- Upload ---

type uploadReq struct {
	opts       ItemOpts
	collection bool
	colName    string
}

func parseUploadOpts(r *http.Request) (uploadReq, error) {
	exp, err := parseExpiry(r.FormValue("expires"))
	if err != nil {
		return uploadReq{}, err
	}
	return uploadReq{
		opts:       ItemOpts{Password: r.FormValue("password"), Expires: exp, Burn: truthy(r.FormValue("burn"))},
		collection: truthy(r.FormValue("collection")),
		colName:    strings.TrimSpace(r.FormValue("collection_name")),
	}, nil
}

func (s *Server) saveUploads(headers []*multipart.FileHeader, o ItemOpts) ([]*FileMeta, error) {
	var out []*FileMeta
	for _, fh := range headers {
		f, err := fh.Open()
		if err != nil {
			return out, err
		}
		meta, err := s.store.SaveFile(f, fh.Filename, o)
		f.Close()
		if err != nil {
			return out, err
		}
		out = append(out, meta)
	}
	return out, nil
}

// POST /api/upload  (multipart, field name: "file")
func (s *Server) handleAPIUpload(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadSize+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		apiErr(w, "request too large or malformed", http.StatusRequestEntityTooLarge)
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		apiErr(w, "no file uploaded (use form field \"file\")", http.StatusBadRequest)
		return
	}
	if len(headers) > maxFilesPerRequest {
		apiErr(w, fmt.Sprintf("too many files (max %d per request)", maxFilesPerRequest), http.StatusBadRequest)
		return
	}
	req, err := parseUploadOpts(r)
	if err != nil {
		apiErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	metas, err := s.saveUploads(headers, req.opts)
	if err != nil {
		log.Printf("api upload: %v", err)
		apiErr(w, "failed to save file", http.StatusInternalServerError)
		return
	}
	out := struct {
		Files      []FileView      `json:"files"`
		Collection *CollectionView `json:"collection"`
	}{Files: []FileView{}}
	ids := []string{}
	for _, m := range metas {
		out.Files = append(out.Files, s.fileView(r, m))
		ids = append(ids, m.ID)
	}
	if req.collection {
		col, err := s.store.SaveCollection(ids, req.colName, req.opts)
		if err != nil {
			apiErr(w, "failed to create collection", http.StatusInternalServerError)
			return
		}
		cv := s.colView(r, col)
		out.Collection = &cv
	}
	apiStatus(w, http.StatusCreated, out)
}

// --- History ---

func matchQuery(q string, fields ...string) bool {
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// GET /api/history?q=&type=&limit=&offset=
func (s *Server) handleAPIHistory(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	qs := r.URL.Query()
	q := strings.ToLower(strings.TrimSpace(qs.Get("q")))
	typ := qs.Get("type")
	switch typ {
	case "", "image", "video", "audio", "text", "other", "collection":
	default:
		apiErr(w, "invalid type (use image, video, audio, text, other or collection)", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(qs.Get("limit"))
	offset, _ := strconv.Atoi(qs.Get("offset"))
	if limit < 0 || offset < 0 {
		apiErr(w, "invalid limit/offset", http.StatusBadRequest)
		return
	}

	files, _ := s.store.ListFiles()
	cols, _ := s.store.ListCollections()

	type item struct {
		date int64
		id   string
		f    *FileMeta
		c    *Collection
	}
	var items []item
	var total int64
	nf, nc := 0, 0
	seen := map[string]bool{}
	if typ != "collection" {
		for _, f := range files {
			if (typ == "" || fileKind(f.Mimetype) == typ) && matchQuery(q, f.ID, f.Name) {
				items = append(items, item{f.Date, f.ID, f, nil})
				nf++
				if !seen[f.Hash] {
					total += f.Size
					seen[f.Hash] = true
				}
			}
		}
	}
	if typ == "" || typ == "collection" {
		for _, c := range cols {
			if matchQuery(q, c.ID, c.Name) {
				items = append(items, item{c.Date, c.ID, nil, c})
				nc++
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].date != items[j].date {
			return items[i].date > items[j].date
		}
		return items[i].id < items[j].id
	})
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:]
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}

	outF, outC := []FileView{}, []CollectionView{}
	for _, it := range items {
		if it.f != nil {
			outF = append(outF, s.fileView(r, it.f))
		} else {
			outC = append(outC, s.colView(r, it.c))
		}
	}
	apiOK(w, map[string]any{
		"files": outF, "collections": outC, "total_size": total,
		"total_files": nf, "total_collections": nc,
	})
}

// --- Single item ---

func (s *Server) itemResponse(r *http.Request, id string) (any, bool) {
	if f, err := s.store.GetFile(id); err == nil {
		return map[string]any{"type": "file", "file": s.fileView(r, f)}, true
	}
	if c, err := s.store.GetCollection(id); err == nil {
		files := []FileView{}
		for _, f := range s.resolveFiles(c.Files) {
			files = append(files, s.fileView(r, f))
		}
		return map[string]any{"type": "collection", "collection": s.colView(r, c), "files": files}, true
	}
	return nil, false
}

// GET /api/{id}
func (s *Server) handleAPIGet(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	if v, ok := s.itemResponse(r, r.PathValue("id")); ok {
		apiOK(w, v)
		return
	}
	apiErr(w, "not found", http.StatusNotFound)
}

type patchReq struct {
	Name     *string  `json:"name"`
	Password *string  `json:"password"`
	Expires  *string  `json:"expires"`
	Add      []string `json:"add"`
	Remove   []string `json:"remove"`
}

// PATCH /api/{id}
func (s *Server) handleAPIPatch(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	id := r.PathValue("id")
	var req patchReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		apiErr(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := s.applyPatch(id, req); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errNotFound) {
			code = http.StatusNotFound
		}
		apiErr(w, err.Error(), code)
		return
	}
	v, _ := s.itemResponse(r, id)
	apiOK(w, v)
}

func (s *Server) applyPatch(id string, req patchReq) error {
	var exp *int64
	if req.Expires != nil {
		e, err := parseExpiry(*req.Expires)
		if err != nil {
			return err
		}
		exp = &e
	}
	var pw *string
	if req.Password != nil {
		h := ""
		if *req.Password != "" {
			var err error
			if h, err = HashPassword(*req.Password); err != nil {
				return err
			}
		}
		pw = &h
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		req.Name = &n
	}
	if s.store.FileExists(id) {
		if len(req.Add) > 0 || len(req.Remove) > 0 {
			return errors.New("add/remove only apply to collections")
		}
		if req.Name != nil && (*req.Name == "" || strings.ContainsAny(*req.Name, "/\\\x00")) {
			return errors.New("invalid file name")
		}
		_, err := s.store.UpdateFile(id, func(m *FileMeta) {
			if req.Name != nil {
				m.Name = *req.Name
			}
			if pw != nil {
				m.Password = *pw
			}
			if exp != nil {
				m.Expires = *exp
			}
		})
		return err
	}
	if !s.store.CollectionExists(id) {
		return errNotFound
	}
	for _, fid := range req.Add {
		if !s.store.FileExists(fid) {
			return fmt.Errorf("file %q not found", fid)
		}
	}
	_, err := s.store.UpdateCollection(id, func(c *Collection) {
		if req.Name != nil {
			c.Name = *req.Name
		}
		if pw != nil {
			c.Password = *pw
		}
		if exp != nil {
			c.Expires = *exp
		}
		c.Files = append(c.Files, req.Add...)
		if len(req.Remove) > 0 {
			rm := map[string]bool{}
			for _, x := range req.Remove {
				rm[x] = true
			}
			kept := []string{}
			for _, f := range c.Files {
				if !rm[f] {
					kept = append(kept, f)
				}
			}
			c.Files = kept
		}
	})
	return err
}

// DELETE /api/{id}
func (s *Server) handleAPIDelete(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	if !s.deleteItem(r.PathValue("id"), r.URL.Query().Get("files") == "true") {
		apiErr(w, "not found", http.StatusNotFound)
		return
	}
	apiOK(w, map[string]bool{"deleted": true})
}

func (s *Server) deleteItem(id string, withFiles bool) bool {
	if s.store.DeleteFile(id) == nil {
		return true
	}
	col, err := s.store.GetCollection(id)
	if err != nil {
		return false
	}
	if withFiles {
		for _, fid := range col.Files {
			s.store.DeleteFile(fid)
		}
	}
	return s.store.DeleteCollection(id) == nil
}

// POST /api/collection   body: {"ids": ["a","b"], "name": "", "password": "", "expires": "never"}
func (s *Server) handleAPICollection(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	var req struct {
		IDs      []string `json:"ids"`
		Password string   `json:"password"`
		Name     string   `json:"name"`
		Expires  string   `json:"expires"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || len(req.IDs) == 0 {
		apiErr(w, "provide {\"ids\": [...]}", http.StatusBadRequest)
		return
	}
	exp, err := parseExpiry(req.Expires)
	if err != nil {
		apiErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	col, err := s.store.SaveCollection(req.IDs, strings.TrimSpace(req.Name), ItemOpts{Password: req.Password, Expires: exp})
	if err != nil {
		apiErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiStatus(w, http.StatusCreated, s.colView(r, col))
}

// GET /api/config  (public)
func (s *Server) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	apiOK(w, map[string]any{
		"max_upload_size":       s.cfg.MaxUploadSize,
		"max_files_per_request": maxFilesPerRequest,
		"expiry_options":        expiryOptions,
		"default_expiry":        "never",
		"version":               "2",
	})
}

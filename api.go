package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

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

// Auth: Authorization: Bearer <key>
func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) bool {
	h := r.Header.Get("Authorization")
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

func apiOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// POST /api/upload  (multipart, field name: "file")
func (s *Server) handleAPIUpload(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	if err := r.ParseMultipartForm(s.cfg.MaxUploadSize + (1 << 20)); err != nil {
		apiErr(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		apiErr(w, "no file uploaded (use form field \"file\")", http.StatusBadRequest)
		return
	}

	password := r.FormValue("password")

	type result struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	var out []result

	for _, fh := range headers {
		f, err := fh.Open()
		if err != nil {
			apiErr(w, "failed to read upload", http.StatusInternalServerError)
			return
		}
		meta, err := s.store.SaveFile(f, fh.Filename, 3, 6, password)
		f.Close()
		if err != nil {
			apiErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, result{ID: meta.ID, URL: baseURL(r) + "/" + meta.ID, Name: meta.Name, Size: meta.Size})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(out)
}

// GET /api/history
func (s *Server) handleAPIHistory(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	files, _ := s.store.ListFiles()
	cols, _ := s.store.ListCollections()

	if files == nil {
		files = []*FileMeta{}
	}
	if cols == nil {
		cols = []*Collection{}
	}

	var total int64
	seen := map[string]bool{}
	for _, f := range files {
		if !seen[f.Hash] {
			total += f.Size
			seen[f.Hash] = true
		}
	}

	apiOK(w, map[string]any{
		"files":       files,
		"collections": cols,
		"total_size":  total,
	})
}

// DELETE /api/{id}
func (s *Server) handleAPIDelete(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	id := r.PathValue("id")

	if s.store.FileExists(id) {
		if err := s.store.DeleteFile(id); err != nil {
			apiErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if s.store.CollectionExists(id) {
		if r.URL.Query().Get("files") == "true" {
			if col, err := s.store.GetCollection(id); err == nil {
				for _, fid := range col.Files {
					s.store.DeleteFile(fid)
				}
			}
		}
		if err := s.store.DeleteCollection(id); err != nil {
			apiErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		apiErr(w, "not found", http.StatusNotFound)
		return
	}

	apiOK(w, map[string]bool{"deleted": true})
}

// POST /api/collection   body: {"ids": ["a","b"]}
func (s *Server) handleAPICollection(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuth(w, r) {
		return
	}
	var req struct {
		IDs      []string `json:"ids"`
		Password string   `json:"password,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		apiErr(w, "provide {\"ids\": [...]}", http.StatusBadRequest)
		return
	}

	col, err := s.store.SaveCollection(req.IDs, 3, 6, req.Password)
	if err != nil {
		apiErr(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"id":  col.ID,
		"url": baseURL(r) + "/" + col.ID,
	})
}

// GET /api/config  (public)
func (s *Server) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	apiOK(w, map[string]any{
		"max_upload_size":       s.cfg.MaxUploadSize,
		"max_files_per_request": 20,
	})
}

package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type FileMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
	Date     int64  `json:"date"`
	Password string `json:"password,omitempty"`
}

func (f *FileMeta) IsProtected() bool {
	return f.Password != ""
}

func (f *FileMeta) CheckPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(f.Password), []byte(pw)) == nil
}

type Collection struct {
	ID       string   `json:"id"`
	Files    []string `json:"files"`
	Date     int64    `json:"date"`
	Password string   `json:"password,omitempty"`
}

func (c *Collection) IsProtected() bool {
	return c.Password != ""
}

func (c *Collection) CheckPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(c.Password), []byte(pw)) == nil
}

func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

type Store struct {
	dir string
	mu  sync.Mutex
	keys map[string]bool
}

func NewStore(dir string) (*Store, error) {
	for _, d := range []string{"files", "meta", "collections"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			return nil, err
		}
	}
	s := &Store{dir: dir, keys: make(map[string]bool)}
	_ = s.loadKeys()
	return s, nil
}

// --- Files ---

func (s *Store) SaveFile(r io.Reader, filename string, minLen, maxLen int, password ...string) (*FileMeta, error) {
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	h := md5.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	tmp.Close()
	if err != nil {
		return nil, err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	mt := detectMimetype(filename, tmpPath)

	dest := filepath.Join(s.dir, "files", hash)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		if err := os.Rename(tmpPath, dest); err != nil {
			copyFile(tmpPath, dest)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.genID(minLen, maxLen)
	meta := &FileMeta{ID: id, Name: filename, Mimetype: mt, Hash: hash, Size: size, Date: time.Now().Unix()}
	if len(password) > 0 && password[0] != "" {
		h, err := HashPassword(password[0])
		if err != nil {
			return nil, err
		}
		meta.Password = h
	}
	return meta, s.writeMeta(id, meta)
}

func (s *Store) SaveFileFromBytes(content []byte, filename string, minLen, maxLen int, password ...string) (*FileMeta, error) {
	h := md5.Sum(content)
	hash := hex.EncodeToString(h[:])

	dest := filepath.Join(s.dir, "files", hash)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		if err := os.WriteFile(dest, content, 0644); err != nil {
			return nil, err
		}
	}

	mt := detectMimetype(filename, dest)

	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.genID(minLen, maxLen)
	meta := &FileMeta{ID: id, Name: filename, Mimetype: mt, Hash: hash, Size: int64(len(content)), Date: time.Now().Unix()}
	if len(password) > 0 && password[0] != "" {
		ph, err := HashPassword(password[0])
		if err != nil {
			return nil, err
		}
		meta.Password = ph
	}
	return meta, s.writeMeta(id, meta)
}

func (s *Store) GetFile(id string) (*FileMeta, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "meta", id+".json"))
	if err != nil {
		return nil, err
	}
	var m FileMeta
	return &m, json.Unmarshal(data, &m)
}

func (s *Store) FilePath(hash string) string {
	return filepath.Join(s.dir, "files", hash)
}

func (s *Store) DeleteFile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.GetFile(id)
	if err != nil {
		return err
	}
	os.Remove(filepath.Join(s.dir, "meta", id+".json"))
	if !s.hashUsedBy(m.Hash, id) {
		os.Remove(filepath.Join(s.dir, "files", m.Hash))
	}
	return nil
}

func (s *Store) ListFiles() ([]*FileMeta, error) {
	entries, _ := os.ReadDir(filepath.Join(s.dir, "meta"))
	var out []*FileMeta
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, "meta", e.Name()))
		if err != nil {
			continue
		}
		var m FileMeta
		if json.Unmarshal(data, &m) == nil {
			out = append(out, &m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, nil
}

func (s *Store) FileExists(id string) bool {
	_, err := os.Stat(filepath.Join(s.dir, "meta", id+".json"))
	return err == nil
}

// --- Collections ---

func (s *Store) SaveCollection(fileIDs []string, minLen, maxLen int, password ...string) (*Collection, error) {
	for _, fid := range fileIDs {
		if !s.FileExists(fid) {
			return nil, fmt.Errorf("file %q not found", fid)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.genID(minLen, maxLen)
	c := &Collection{ID: id, Files: fileIDs, Date: time.Now().Unix()}
	if len(password) > 0 && password[0] != "" {
		h, err := HashPassword(password[0])
		if err != nil {
			return nil, err
		}
		c.Password = h
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return c, os.WriteFile(filepath.Join(s.dir, "collections", id+".json"), data, 0644)
}

func (s *Store) GetCollection(id string) (*Collection, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "collections", id+".json"))
	if err != nil {
		return nil, err
	}
	var c Collection
	return &c, json.Unmarshal(data, &c)
}

func (s *Store) DeleteCollection(id string) error {
	return os.Remove(filepath.Join(s.dir, "collections", id+".json"))
}

func (s *Store) ListCollections() ([]*Collection, error) {
	entries, _ := os.ReadDir(filepath.Join(s.dir, "collections"))
	var out []*Collection
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, "collections", e.Name()))
		if err != nil {
			continue
		}
		var c Collection
		if json.Unmarshal(data, &c) == nil {
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, nil
}

func (s *Store) CollectionExists(id string) bool {
	_, err := os.Stat(filepath.Join(s.dir, "collections", id+".json"))
	return err == nil
}

// --- API keys ---

func (s *Store) ValidAPIKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[key]
}

func (s *Store) AddAPIKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[strings.TrimSpace(key)] = true
	return s.saveKeys()
}

func (s *Store) loadKeys() error {
	data, err := os.ReadFile(filepath.Join(s.dir, "apikeys"))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			s.keys[line] = true
		}
	}
	return nil
}

func (s *Store) saveKeys() error {
	var lines []string
	for k := range s.keys {
		lines = append(lines, k)
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(s.dir, "apikeys"), []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// --- Cleanup ---

func (s *Store) CleanupLoop(maxAge time.Duration) {
	s.cleanup(maxAge)
	for range time.Tick(1 * time.Hour) {
		s.cleanup(maxAge)
	}
}

func (s *Store) cleanup(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge).Unix()

	files, _ := s.ListFiles()
	for _, f := range files {
		if f.Date < cutoff {
			if err := s.DeleteFile(f.ID); err == nil {
				log.Printf("cleanup: deleted file %s (%s)", f.ID, f.Name)
			}
		}
	}

	cols, _ := s.ListCollections()
	for _, c := range cols {
		if c.Date < cutoff {
			if err := s.DeleteCollection(c.ID); err == nil {
				log.Printf("cleanup: deleted collection %s", c.ID)
			}
		}
	}
}

// --- Internal ---

func (s *Store) writeMeta(id string, m *FileMeta) error {
	data, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(s.dir, "meta", id+".json"), data, 0644)
}

func (s *Store) hashUsedBy(hash, excludeID string) bool {
	entries, _ := os.ReadDir(filepath.Join(s.dir, "meta"))
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if name == excludeID {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, "meta", e.Name()))
		if err != nil {
			continue
		}
		var m FileMeta
		if json.Unmarshal(data, &m) == nil && m.Hash == hash {
			return true
		}
	}
	return false
}

func (s *Store) genID(minLen, maxLen int) string {
	if minLen < 3 {
		minLen = 3
	}
	if maxLen < minLen {
		maxLen = minLen + 3
	}
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	reserved := map[string]bool{
		"api": true, "static": true, "login": true, "logout": true,
		"history": true, "gallery": true, "upload": true, "delete": true, "collection": true,
	}

	for range 200 {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(maxLen-minLen+1)))
		length := minLen + int(n.Int64())
		b := make([]byte, length)
		for i := range b {
			idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
			b[i] = chars[idx.Int64()]
		}
		id := string(b)
		if reserved[id] {
			continue
		}
		if s.FileExists(id) || s.CollectionExists(id) {
			continue
		}
		return id
	}
	// fallback
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)[:12]
}

func detectMimetype(filename, path string) string {
	overrides := map[string]string{
		".cast": "application/x-asciicast", ".toml": "application/toml",
		".yaml": "application/x-yaml", ".yml": "application/x-yaml",
		".md": "text/markdown", ".rs": "text/x-rust", ".go": "text/x-go",
		".ts": "text/typescript", ".tsx": "text/tsx", ".jsx": "text/jsx",
	}
	ext := filepath.Ext(filename)
	if m, ok := overrides[ext]; ok {
		return m
	}
	if ext != "" {
		if m := mime.TypeByExtension(ext); m != "" {
			return m
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

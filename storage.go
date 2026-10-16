package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

var errNotFound = errors.New("not found")

// On-disk format; Password holds a bcrypt hash and must never be sent to clients.
type FileMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
	Date     int64  `json:"date"`
	Password string `json:"password,omitempty"`
	Expires  int64  `json:"expires,omitempty"`
	Burn     bool   `json:"burn,omitempty"`
}

func (f *FileMeta) IsProtected() bool { return f.Password != "" }

func (f *FileMeta) CheckPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(f.Password), []byte(pw)) == nil
}

type Collection struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Files    []string `json:"files"`
	Date     int64    `json:"date"`
	Password string   `json:"password,omitempty"`
	Expires  int64    `json:"expires,omitempty"`
	Burn     bool     `json:"burn,omitempty"`
}

func (c *Collection) IsProtected() bool { return c.Password != "" }

func (c *Collection) CheckPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(c.Password), []byte(pw)) == nil
}

func (c *Collection) Title() string {
	if c.Name != "" {
		return c.Name
	}
	return "Collection"
}

func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

// ItemOpts are the per-upload options shared by files and collections.
type ItemOpts struct {
	Password string
	Expires  int64
	Burn     bool
}

type Store struct {
	dir    string
	MaxAge time.Duration
	mu     sync.RWMutex
	keys   map[string]bool
	files  map[string]*FileMeta
	cols   map[string]*Collection
	refs   map[string]int
}

func NewStore(dir string) (*Store, error) {
	for _, d := range []string{"files", "meta", "collections", "thumbs"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			return nil, err
		}
	}
	s := &Store{dir: dir, keys: map[string]bool{}, files: map[string]*FileMeta{}, cols: map[string]*Collection{}, refs: map[string]int{}}
	_ = s.loadKeys()
	s.loadIndex()
	return s, nil
}

func (s *Store) loadIndex() {
	load := func(sub string, fn func([]byte)) {
		entries, _ := os.ReadDir(filepath.Join(s.dir, sub))
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(s.dir, sub, e.Name())); err == nil {
				fn(data)
			}
		}
	}
	load("meta", func(b []byte) {
		var m FileMeta
		if json.Unmarshal(b, &m) == nil && m.ID != "" {
			s.files[m.ID] = &m
			s.refs[m.Hash]++
		}
	})
	load("collections", func(b []byte) {
		var c Collection
		if json.Unmarshal(b, &c) == nil && c.ID != "" {
			s.cols[c.ID] = &c
		}
	})
}

// EffectiveExpiry combines the per-item expiry with the global max age (0 = never).
func (s *Store) EffectiveExpiry(date, expires int64) int64 {
	if s.MaxAge > 0 {
		if g := date + int64(s.MaxAge/time.Second); expires == 0 || g < expires {
			return g
		}
	}
	return expires
}

func (s *Store) expired(date, expires int64) bool {
	e := s.EffectiveExpiry(date, expires)
	return e > 0 && time.Now().Unix() >= e
}

func cloneFile(m *FileMeta) *FileMeta { c := *m; return &c }

func cloneCol(c *Collection) *Collection {
	n := *c
	n.Files = append([]string{}, c.Files...)
	return &n
}

// --- Files ---

func (s *Store) SaveFile(r io.Reader, filename string, o ItemOpts) (*FileMeta, error) {
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
	return s.addFile(filename, mt, hash, size, o, func(dest string) error {
		if err := os.Rename(tmpPath, dest); err != nil {
			return copyFile(tmpPath, dest)
		}
		return nil
	})
}

func (s *Store) SaveFileFromBytes(content []byte, filename string, o ItemOpts) (*FileMeta, error) {
	h := md5.Sum(content)
	hash := hex.EncodeToString(h[:])
	mt := detectMimetypeBytes(filename, content)
	return s.addFile(filename, mt, hash, int64(len(content)), o, func(dest string) error {
		return os.WriteFile(dest, content, 0644)
	})
}

func (s *Store) addFile(name, mt, hash string, size int64, o ItemOpts, write func(string) error) (*FileMeta, error) {
	pw := ""
	if o.Password != "" {
		var err error
		if pw, err = HashPassword(o.Password); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dest := s.FilePath(hash)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		if err := write(dest); err != nil {
			return nil, err
		}
	}
	meta := &FileMeta{ID: s.genID(3, 6), Name: name, Mimetype: mt, Hash: hash, Size: size,
		Date: time.Now().Unix(), Password: pw, Expires: o.Expires, Burn: o.Burn}
	if err := s.writeJSON("meta", meta.ID, meta); err != nil {
		return nil, err
	}
	s.files[meta.ID] = meta
	s.refs[hash]++
	return cloneFile(meta), nil
}

func (s *Store) GetFile(id string) (*FileMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.files[id]
	if !ok || s.expired(m.Date, m.Expires) {
		return nil, errNotFound
	}
	return cloneFile(m), nil
}

func (s *Store) UpdateFile(id string, fn func(*FileMeta)) (*FileMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.files[id]
	if !ok {
		return nil, errNotFound
	}
	n := cloneFile(m)
	fn(n)
	if err := s.writeJSON("meta", id, n); err != nil {
		return nil, err
	}
	s.files[id] = n
	return cloneFile(n), nil
}

func (s *Store) FilePath(hash string) string  { return filepath.Join(s.dir, "files", hash) }
func (s *Store) ThumbPath(hash string) string { return filepath.Join(s.dir, "thumbs", hash+".jpg") }

func (s *Store) DeleteFile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.files[id]
	if !ok {
		return errNotFound
	}
	if err := os.Remove(filepath.Join(s.dir, "meta", id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.files, id)
	if s.refs[m.Hash]--; s.refs[m.Hash] <= 0 {
		delete(s.refs, m.Hash)
		os.Remove(s.FilePath(m.Hash))
		os.Remove(s.ThumbPath(m.Hash))
	}
	return nil
}

func (s *Store) ListFiles() ([]*FileMeta, error) {
	s.mu.RLock()
	out := make([]*FileMeta, 0, len(s.files))
	for _, m := range s.files {
		if !s.expired(m.Date, m.Expires) {
			out = append(out, cloneFile(m))
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) FileExists(id string) bool {
	_, err := s.GetFile(id)
	return err == nil
}

// --- Collections ---

func (s *Store) SaveCollection(fileIDs []string, name string, o ItemOpts) (*Collection, error) {
	for _, fid := range fileIDs {
		if !s.FileExists(fid) {
			return nil, fmt.Errorf("file %q not found", fid)
		}
	}
	pw := ""
	if o.Password != "" {
		var err error
		if pw, err = HashPassword(o.Password); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Collection{ID: s.genID(3, 6), Name: name, Files: dedup(fileIDs), Date: time.Now().Unix(),
		Password: pw, Expires: o.Expires, Burn: o.Burn}
	if err := s.writeJSON("collections", c.ID, c); err != nil {
		return nil, err
	}
	s.cols[c.ID] = c
	return cloneCol(c), nil
}

func (s *Store) GetCollection(id string) (*Collection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cols[id]
	if !ok || s.expired(c.Date, c.Expires) {
		return nil, errNotFound
	}
	return cloneCol(c), nil
}

func (s *Store) UpdateCollection(id string, fn func(*Collection)) (*Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cols[id]
	if !ok {
		return nil, errNotFound
	}
	n := cloneCol(c)
	fn(n)
	n.Files = dedup(n.Files)
	if err := s.writeJSON("collections", id, n); err != nil {
		return nil, err
	}
	s.cols[id] = n
	return cloneCol(n), nil
}

func (s *Store) DeleteCollection(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cols[id]; !ok {
		return errNotFound
	}
	if err := os.Remove(filepath.Join(s.dir, "collections", id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.cols, id)
	return nil
}

func (s *Store) ListCollections() ([]*Collection, error) {
	s.mu.RLock()
	out := make([]*Collection, 0, len(s.cols))
	for _, c := range s.cols {
		if !s.expired(c.Date, c.Expires) {
			out = append(out, cloneCol(c))
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) CollectionExists(id string) bool {
	_, err := s.GetCollection(id)
	return err == nil
}

// CollectionsWith returns the IDs of live collections containing fileID.
func (s *Store) CollectionsWith(fileID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, c := range s.cols {
		if s.expired(c.Date, c.Expires) {
			continue
		}
		for _, f := range c.Files {
			if f == fileID {
				out = append(out, c.ID)
				break
			}
		}
	}
	return out
}

func dedup(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// --- API keys ---

func (s *Store) ValidAPIKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
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

func (s *Store) CleanupLoop(every time.Duration) {
	s.Cleanup()
	for range time.Tick(every) {
		s.Cleanup()
	}
}

// Cleanup removes expired files and collections, and collections whose files are all gone.
func (s *Store) Cleanup() {
	s.mu.RLock()
	var files, cols []string
	for id, m := range s.files {
		if s.expired(m.Date, m.Expires) {
			files = append(files, id)
		}
	}
	s.mu.RUnlock()
	for _, id := range files {
		if err := s.DeleteFile(id); err == nil {
			log.Printf("cleanup: deleted file %s", id)
		}
	}
	s.mu.RLock()
	for id, c := range s.cols {
		alive := false
		for _, f := range c.Files {
			if m, ok := s.files[f]; ok && !s.expired(m.Date, m.Expires) {
				alive = true
				break
			}
		}
		if !alive || s.expired(c.Date, c.Expires) {
			cols = append(cols, id)
		}
	}
	s.mu.RUnlock()
	for _, id := range cols {
		if err := s.DeleteCollection(id); err == nil {
			log.Printf("cleanup: deleted collection %s", id)
		}
	}
}

// --- Internal ---

func (s *Store) writeJSON(sub, id string, v any) error {
	data, _ := json.MarshalIndent(v, "", "  ")
	p := filepath.Join(s.dir, sub, id+".json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// genID must be called with s.mu held.
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
		b := make([]byte, minLen+int(n.Int64()))
		for i := range b {
			idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
			b[i] = chars[idx.Int64()]
		}
		id := string(b)
		if _, f := s.files[id]; f || reserved[id] {
			continue
		}
		if _, c := s.cols[id]; c {
			continue
		}
		return id
	}
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)[:12]
}

var mimeOverrides = map[string]string{
	".cast": "application/x-asciicast", ".toml": "application/toml",
	".yaml": "application/x-yaml", ".yml": "application/x-yaml",
	".md": "text/markdown", ".rs": "text/x-rust", ".go": "text/x-go",
	".ts": "text/typescript", ".tsx": "text/tsx", ".jsx": "text/jsx",
	".sh": "application/x-sh", ".py": "text/x-python",
}

func mimeByName(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if m, ok := mimeOverrides[ext]; ok {
		return m
	}
	if ext != "" {
		return mime.TypeByExtension(ext)
	}
	return ""
}

func detectMimetypeBytes(filename string, b []byte) string {
	if m := mimeByName(filename); m != "" {
		return m
	}
	if len(b) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(b[:min(len(b), 512)])
}

func detectMimetype(filename, path string) string {
	if m := mimeByName(filename); m != "" {
		return m
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return detectMimetypeBytes("", buf[:n])
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

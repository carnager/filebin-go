package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "testkey"

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "apikeys"), []byte(testKey+"\n"), 0600)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Config{DataDir: dir, MaxUploadSize: 10 << 20}, store)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

type part struct{ name, filename, content string }

func upload(t *testing.T, ts *httptest.Server, parts ...part) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.filename != "" {
			w, _ := mw.CreateFormFile(p.name, p.filename)
			w.Write([]byte(p.content))
		} else {
			mw.WriteField(p.name, p.content)
		}
	}
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testKey)
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func get(t *testing.T, ts *httptest.Server, path string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return do(t, req)
}

func apiJSON(t *testing.T, ts *httptest.Server, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, r)
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/json")
	return do(t, req)
}

type uploadResp struct {
	Files      []FileView      `json:"files"`
	Collection *CollectionView `json:"collection"`
}

func mustUpload(t *testing.T, ts *httptest.Server, parts ...part) uploadResp {
	t.Helper()
	resp, b := upload(t, ts, parts...)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	var out uploadResp
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func pngBytes() string {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.String()
}

func TestUploadResponseShape(t *testing.T) {
	_, ts := newTestServer(t)
	resp, b := upload(t, ts, part{"file", "a.txt", "hello"}, part{"file", "b.txt", "world"},
		part{"collection", "", "true"}, part{"collection_name", "", "Holiday"}, part{"expires", "", "1d"})
	if resp.StatusCode != 201 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(b, &raw)
	if _, ok := raw["files"]; !ok {
		t.Fatal("missing files key")
	}
	var out uploadResp
	json.Unmarshal(b, &out)
	if len(out.Files) != 2 || out.Collection == nil {
		t.Fatalf("unexpected: %s", b)
	}
	f := out.Files[0]
	if f.URL != ts.URL+"/"+f.ID || f.Name != "a.txt" || f.Size != 5 || f.Expires == 0 || f.Thumb || f.Protected {
		t.Fatalf("bad file view: %+v", f)
	}
	c := out.Collection
	if c.Name != "Holiday" || len(c.Files) != 2 || c.Size != 10 || c.URL != ts.URL+"/"+c.ID {
		t.Fatalf("bad collection view: %+v", c)
	}
	// without collection=true the collection is null
	_, b = upload(t, ts, part{"file", "c.txt", "x"})
	if !strings.Contains(string(b), `"collection":null`) {
		t.Fatalf("expected null collection: %s", b)
	}
}

func TestHistoryNeverContainsPasswordHash(t *testing.T) {
	_, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "secret.txt", "s3cret"}, part{"password", "", "hunter2"}, part{"collection", "", "true"})
	if !out.Files[0].Protected || !out.Collection.Protected {
		t.Fatal("expected protected")
	}
	for _, p := range []string{"/api/history", "/api/" + out.Files[0].ID, "/api/" + out.Collection.ID} {
		_, b := apiJSON(t, ts, "GET", p, nil)
		if strings.Contains(string(b), "$2a$") || strings.Contains(string(b), "\"password\"") {
			t.Fatalf("%s leaks password: %s", p, b)
		}
	}
	_, b := apiJSON(t, ts, "PATCH", "/api/"+out.Files[0].ID, map[string]any{"name": "x.txt"})
	if strings.Contains(string(b), "$2a$") {
		t.Fatalf("patch leaks password: %s", b)
	}
}

func TestHistoryFilters(t *testing.T) {
	_, ts := newTestServer(t)
	mustUpload(t, ts, part{"file", "pic.png", pngBytes()}, part{"file", "notes.txt", "hi"}, part{"collection", "", "1"})
	var h struct {
		Files       []FileView       `json:"files"`
		Collections []CollectionView `json:"collections"`
		TotalFiles  int              `json:"total_files"`
		TotalCols   int              `json:"total_collections"`
	}
	_, b := apiJSON(t, ts, "GET", "/api/history?type=image", nil)
	json.Unmarshal(b, &h)
	if len(h.Files) != 1 || h.Files[0].Name != "pic.png" || len(h.Collections) != 0 {
		t.Fatalf("type filter: %s", b)
	}
	_, b = apiJSON(t, ts, "GET", "/api/history?q=NOTES", nil)
	json.Unmarshal(b, &h)
	if len(h.Files) != 1 || h.Files[0].Name != "notes.txt" {
		t.Fatalf("q filter: %s", b)
	}
	_, b = apiJSON(t, ts, "GET", "/api/history?limit=1", nil)
	json.Unmarshal(b, &h)
	if len(h.Files)+len(h.Collections) != 1 || h.TotalFiles != 2 || h.TotalCols != 1 {
		t.Fatalf("limit: %s", b)
	}
}

func TestMaxFilesEnforced(t *testing.T) {
	_, ts := newTestServer(t)
	var parts []part
	for i := 0; i < maxFilesPerRequest+1; i++ {
		parts = append(parts, part{"file", "f.txt", "x"})
	}
	resp, b := upload(t, ts, parts...)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "too many files") {
		t.Fatalf("api: %d %s", resp.StatusCode, b)
	}
	// web upload
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf_token", strings.Repeat("a", 64))
	for i := 0; i < maxFilesPerRequest+1; i++ {
		w, _ := mw.CreateFormFile("file", "f.txt")
		w.Write([]byte("x"))
	}
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Cookie", "apikey="+testKey+"; csrf="+strings.Repeat("a", 64))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, b = do(t, req)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "Too many files") {
		t.Fatalf("web: %d %s", resp.StatusCode, b)
	}
}

func TestExpiry404(t *testing.T) {
	s, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "a.txt", "hello"}, part{"expires", "", "3600"})
	id := out.Files[0].ID
	if resp, _ := get(t, ts, "/"+id+"/raw"); resp.StatusCode != 200 {
		t.Fatal("expected 200 before expiry")
	}
	if _, err := s.store.UpdateFile(id, func(m *FileMeta) { m.Expires = time.Now().Unix() - 1 }); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(t, ts, "/"+id+"/raw"); resp.StatusCode != 404 {
		t.Fatalf("expected 404 after expiry, got %d", resp.StatusCode)
	}
	if resp, _ := apiJSON(t, ts, "GET", "/api/"+id, nil); resp.StatusCode != 404 {
		t.Fatal("api should 404 expired item")
	}
	s.store.Cleanup()
	if _, err := os.Stat(filepath.Join(s.store.dir, "meta", id+".json")); !os.IsNotExist(err) {
		t.Fatal("cleanup should remove expired meta")
	}
	if resp, _ := upload(t, ts, part{"file", "a.txt", "x"}, part{"expires", "", "bogus"}); resp.StatusCode != 400 {
		t.Fatal("invalid expires should 400")
	}
}

func TestCleanupRemovesEmptyCollections(t *testing.T) {
	s, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "a.txt", "hello"}, part{"collection", "", "true"})
	s.store.DeleteFile(out.Files[0].ID)
	s.store.Cleanup()
	if s.store.CollectionExists(out.Collection.ID) {
		t.Fatal("collection with no files should be cleaned up")
	}
}

func TestBurnAfterReading(t *testing.T) {
	s, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "a.txt", "burn me"}, part{"burn", "", "true"})
	id := out.Files[0].ID
	if !out.Files[0].Burn {
		t.Fatal("burn flag missing")
	}
	// owner (Bearer) does not trigger burn
	for i := 0; i < 2; i++ {
		resp, b := get(t, ts, "/"+id+"/raw", "Authorization", "Bearer "+testKey)
		if resp.StatusCode != 200 || string(b) != "burn me" {
			t.Fatalf("owner view %d: %d %s", i, resp.StatusCode, b)
		}
	}
	// link preview bots don't consume it
	resp, b := get(t, ts, "/"+id, "Accept", "text/html", "User-Agent", "Slackbot-LinkExpanding 1.0")
	if resp.StatusCode != 200 || strings.Contains(string(b), "burn me") {
		t.Fatalf("bot view: %d", resp.StatusCode)
	}
	// first anonymous view gets it, second is 404
	resp, b = get(t, ts, "/"+id+"/raw")
	if resp.StatusCode != 200 || string(b) != "burn me" {
		t.Fatalf("first view: %d %s", resp.StatusCode, b)
	}
	if resp, _ := get(t, ts, "/"+id+"/raw"); resp.StatusCode != 404 {
		t.Fatalf("second view should 404, got %d", resp.StatusCode)
	}
	if s.store.FileExists(id) {
		t.Fatal("burned file still exists")
	}
	// paste HTML view burns too
	out = mustUpload(t, ts, part{"file", "b.txt", "paste"}, part{"burn", "", "1"})
	if resp, b := get(t, ts, "/"+out.Files[0].ID, "Accept", "text/html"); resp.StatusCode != 200 || !strings.Contains(string(b), "Burn after reading") {
		t.Fatalf("paste view: %d", resp.StatusCode)
	}
	if s.store.FileExists(out.Files[0].ID) {
		t.Fatal("paste should be burned after HTML view")
	}
}

func TestPatchRenameAndCollection(t *testing.T) {
	_, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "a.txt", "a"}, part{"file", "b.txt", "b"}, part{"file", "c.txt", "c"})
	a, b, c := out.Files[0].ID, out.Files[1].ID, out.Files[2].ID

	resp, body := apiJSON(t, ts, "PATCH", "/api/"+a, map[string]any{"name": "renamed.md", "password": "pw", "expires": "1w"})
	var fr struct {
		Type string   `json:"type"`
		File FileView `json:"file"`
	}
	json.Unmarshal(body, &fr)
	if resp.StatusCode != 200 || fr.Type != "file" || fr.File.Name != "renamed.md" || !fr.File.Protected || fr.File.Expires == 0 {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	_, body = apiJSON(t, ts, "PATCH", "/api/"+a, map[string]any{"password": "", "expires": "never"})
	json.Unmarshal(body, &fr)
	if fr.File.Protected || fr.File.Expires != 0 || fr.File.Name != "renamed.md" {
		t.Fatalf("password remove: %s", body)
	}

	resp, body = apiJSON(t, ts, "POST", "/api/collection", map[string]any{"ids": []string{a, b}, "name": "Col"})
	var cv CollectionView
	json.Unmarshal(body, &cv)
	if resp.StatusCode != 201 || cv.Name != "Col" || len(cv.Files) != 2 {
		t.Fatalf("create collection: %d %s", resp.StatusCode, body)
	}
	resp, body = apiJSON(t, ts, "PATCH", "/api/"+cv.ID, map[string]any{"name": "Trip", "add": []string{c}, "remove": []string{a}})
	var cr struct {
		Type       string         `json:"type"`
		Collection CollectionView `json:"collection"`
		Files      []FileView     `json:"files"`
	}
	json.Unmarshal(body, &cr)
	if resp.StatusCode != 200 || cr.Type != "collection" || cr.Collection.Name != "Trip" || len(cr.Files) != 2 ||
		cr.Collection.Files[0] != b || cr.Collection.Files[1] != c {
		t.Fatalf("patch collection: %d %s", resp.StatusCode, body)
	}
	if resp, body := apiJSON(t, ts, "PATCH", "/api/"+cv.ID, map[string]any{"add": []string{"nope"}}); resp.StatusCode != 400 || !strings.Contains(string(body), "error") {
		t.Fatalf("add missing file: %d %s", resp.StatusCode, body)
	}
	if resp, _ := apiJSON(t, ts, "PATCH", "/api/nope", map[string]any{"name": "x"}); resp.StatusCode != 404 {
		t.Fatal("patch unknown should 404")
	}
}

func TestGetAPIItem(t *testing.T) {
	_, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "a.txt", "a"}, part{"collection", "", "true"})
	_, body := apiJSON(t, ts, "GET", "/api/"+out.Files[0].ID, nil)
	if !strings.Contains(string(body), `"type":"file"`) || !strings.Contains(string(body), `"name":"a.txt"`) {
		t.Fatalf("file: %s", body)
	}
	_, body = apiJSON(t, ts, "GET", "/api/"+out.Collection.ID, nil)
	if !strings.Contains(string(body), `"type":"collection"`) || !strings.Contains(string(body), `"files":[{`) {
		t.Fatalf("collection: %s", body)
	}
	if resp, _ := apiJSON(t, ts, "GET", "/api/zzzzzz", nil); resp.StatusCode != 404 {
		t.Fatal("expected 404")
	}
	if resp, _ := get(t, ts, "/api/"+out.Files[0].ID); resp.StatusCode != 401 {
		t.Fatal("expected 401 without auth")
	}
}

func TestThumbnail(t *testing.T) {
	s, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "pic.png", pngBytes()})
	if !out.Files[0].Thumb {
		t.Fatal("png should have thumb")
	}
	resp, b := get(t, ts, "/"+out.Files[0].ID+"/thumb")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "jpeg" || cfg.Width != 400 || cfg.Height != 300 {
		t.Fatalf("thumb decode: %v %s %dx%d", err, format, cfg.Width, cfg.Height)
	}
	tp := s.store.ThumbPath(out.Files[0].Hash)
	if _, err := os.Stat(tp); err != nil {
		t.Fatal("thumb not cached")
	}
	apiJSON(t, ts, "DELETE", "/api/"+out.Files[0].ID, nil)
	if _, err := os.Stat(tp); !os.IsNotExist(err) {
		t.Fatal("thumb cache not removed with blob")
	}
	txt := mustUpload(t, ts, part{"file", "a.txt", "a"})
	if resp, _ := get(t, ts, "/"+txt.Files[0].ID+"/thumb"); resp.StatusCode != 404 {
		t.Fatal("text thumb should 404")
	}
	// protected: thumb needs password unless owner
	p := mustUpload(t, ts, part{"file", "p.png", pngBytes()}, part{"password", "", "pw"})
	if resp, _ := get(t, ts, "/"+p.Files[0].ID+"/thumb"); resp.StatusCode != 403 {
		t.Fatalf("protected thumb should 403, got %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, "/"+p.Files[0].ID+"/thumb", "Authorization", "Bearer "+testKey); resp.StatusCode != 200 {
		t.Fatal("owner should get protected thumb")
	}
}

func TestQRAndDownload(t *testing.T) {
	_, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "Grüße.txt", "a"})
	resp, _ := get(t, ts, "/"+out.Files[0].ID+"/qr.png")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("qr: %d", resp.StatusCode)
	}
	resp, _ = get(t, ts, "/"+out.Files[0].ID+"/raw?download=1")
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "filename*=utf-8''Gr%C3%BC%C3%9Fe.txt") {
		t.Fatalf("content-disposition: %q", cd)
	}
}

func TestWebCollectionIgnoresCollectionIDs(t *testing.T) {
	s, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "a.txt", "a"}, part{"file", "b.txt", "b"}, part{"collection", "", "true"})
	csrf := strings.Repeat("b", 64)
	form := url.Values{"csrf_token": {csrf}, "ids": {out.Files[0].ID, out.Collection.ID, out.Files[1].ID}, "name": {"Mixed"}}
	req, _ := http.NewRequest("POST", ts.URL+"/collection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "apikey="+testKey+"; csrf="+csrf)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", resp.StatusCode)
	}
	cols, _ := s.store.ListCollections()
	var found *Collection
	for _, c := range cols {
		if c.Name == "Mixed" {
			found = c
		}
	}
	if found == nil || len(found.Files) != 2 {
		t.Fatalf("mixed collection not created correctly: %+v", found)
	}
}

func TestIndexPersistsAndSecret(t *testing.T) {
	s, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "a.txt", "a"}, part{"file", "a2.txt", "a"})
	st2, _ := NewStore(s.store.dir)
	if !st2.FileExists(out.Files[0].ID) || st2.refs[out.Files[0].Hash] != 2 {
		t.Fatal("index not reloaded from disk")
	}
	sec, _ := os.ReadFile(filepath.Join(s.store.dir, "secret"))
	s2 := NewServer(Config{DataDir: s.store.dir}, st2)
	if !bytes.Equal(sec, s2.secret) {
		t.Fatal("secret not persisted")
	}
	if fi, _ := os.Stat(filepath.Join(s.store.dir, "secret")); fi.Mode().Perm() != 0600 {
		t.Fatal("secret should be 0600")
	}
	// deleting one of two refs keeps the blob
	s.store.DeleteFile(out.Files[0].ID)
	if _, err := os.Stat(s.store.FilePath(out.Files[0].Hash)); err != nil {
		t.Fatal("blob removed while still referenced")
	}
}

func TestPagesRender(t *testing.T) {
	_, ts := newTestServer(t)
	out := mustUpload(t, ts, part{"file", "pic.png", pngBytes()}, part{"file", "readme.md", "# Title\n\n<script>x</script>"},
		part{"collection", "", "true"}, part{"collection_name", "", "Album"})
	cookie := "apikey=" + testKey + "; csrf=" + strings.Repeat("c", 64)
	for _, p := range []string{"/", "/history", "/gallery", "/" + out.Files[0].ID, "/" + out.Files[1].ID, "/" + out.Files[1].ID + "/go",
		"/" + out.Collection.ID, "/" + out.Collection.ID + "/info", "/" + out.Files[0].ID + "/info", "/login"} {
		resp, b := get(t, ts, p, "Accept", "text/html", "Cookie", cookie)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, b)
		}
	}
	_, b := get(t, ts, "/"+out.Files[1].ID, "Accept", "text/html")
	if !strings.Contains(string(b), "<h1>Title</h1>") || strings.Contains(string(b), "<script>x</script>") {
		t.Fatal("markdown not rendered safely")
	}
	if !strings.Contains(string(b), `property="og:title" content="readme.md"`) {
		t.Fatal("missing og tags")
	}
	_, b = get(t, ts, "/"+out.Files[0].ID, "Accept", "text/html")
	if !strings.Contains(string(b), `og:image" content="`+ts.URL+"/"+out.Files[0].ID+`/thumb"`) {
		t.Fatal("missing og:image")
	}
	p := mustUpload(t, ts, part{"file", "secret.txt", "topsecret"}, part{"password", "", "pw"})
	_, b = get(t, ts, "/"+p.Files[0].ID, "Accept", "text/html")
	if strings.Contains(string(b), "topsecret") || !strings.Contains(string(b), "secret.txt") || strings.Contains(string(b), "og:description") {
		t.Fatal("password page should show name only")
	}
}

func TestLoginCookie(t *testing.T) {
	_, ts := newTestServer(t)
	csrf := strings.Repeat("d", 64)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, remember := range []bool{false, true} {
		form := url.Values{"csrf_token": {csrf}, "apikey": {testKey}, "next": {"//evil.com"}}
		if remember {
			form.Set("remember", "1")
		}
		req, _ := http.NewRequest("POST", ts.URL+"/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Cookie", "csrf="+csrf)
		req.Header.Set("X-Forwarded-Proto", "https")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); loc != "/" {
			t.Fatalf("unsafe next: %q", loc)
		}
		var c *http.Cookie
		for _, ck := range resp.Cookies() {
			if ck.Name == "apikey" {
				c = ck
			}
		}
		if c == nil || !c.Secure || (remember && c.MaxAge != 30*86400) || (!remember && c.MaxAge != 0) {
			t.Fatalf("cookie: %+v", c)
		}
	}
}

func TestRawSandboxCSP(t *testing.T) {
	_, ts := newTestServer(t)
	up := mustUpload(t, ts,
		part{"file", "x.html", "<script>alert(document.cookie)</script>"},
		part{"file", "x.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		part{"file", "x.png", pngBytes()})
	for i, want := range []bool{true, true, false} {
		resp, _ := get(t, ts, "/"+up.Files[i].ID+"/raw")
		csp := resp.Header.Get("Content-Security-Policy")
		if got := strings.Contains(csp, "sandbox"); got != want {
			t.Errorf("%s: sandbox=%v, want %v (csp %q)", up.Files[i].Name, got, want, csp)
		}
	}
}

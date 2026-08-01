package hf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHub serves the two hub endpoints the client uses, counting requests so a
// test can prove the cache is actually a cache.
type fakeHub struct {
	sha   string
	files map[string]string // repo-relative path -> contents
	hits  map[string]int
}

func newHub(files map[string]string) *fakeHub {
	return &fakeHub{sha: "abc123def456", files: files, hits: map[string]int{}}
}

func (h *fakeHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/revision/"):
			h.hits["revision"]++
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": h.sha})
		case strings.Contains(p, "/tree/"):
			h.hits["tree"]++
			// Everything after /tree/<sha>/ is the prefix being listed.
			i := strings.Index(p, "/tree/")
			prefix := strings.TrimPrefix(p[i+len("/tree/"):], h.sha)
			prefix = strings.Trim(prefix, "/")
			var out []treeEntry
			for f := range h.files {
				if prefix == "" || strings.HasPrefix(f, prefix+"/") {
					out = append(out, treeEntry{Type: "file", Path: f})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	})
	// /<org>/<name>/resolve/<sha>/<path...>
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		i := strings.Index(r.URL.Path, "/resolve/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path[i+len("/resolve/"):], h.sha+"/")
		body, ok := h.files[rel]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.hits["get:"+rel]++
		w.Header().Set("ETag", fmt.Sprintf("%q", "blob-"+strings.ReplaceAll(rel, "/", "_")))
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func client(t *testing.T, h *fakeHub) *Client {
	t.Helper()
	srv := h.server(t)
	return &Client{HTTP: srv.Client(), Cache: t.TempDir(), Endpoint: srv.URL}
}

// TestFileFetchesAndCaches: the first call downloads, the second does not.
func TestFileFetchesAndCaches(t *testing.T) {
	h := newHub(map[string]string{"model.onnx": "weights"})
	c := client(t, h)

	p, err := c.File(context.Background(), "org/name", "model.onnx")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "weights" {
		t.Fatalf("read %s = %q, %v", p, b, err)
	}

	if _, err := c.File(context.Background(), "org/name", "model.onnx"); err != nil {
		t.Fatal(err)
	}
	if h.hits["get:model.onnx"] != 1 {
		t.Errorf("downloaded %d times, want 1 — the cache is not caching", h.hits["get:model.onnx"])
	}
	if h.hits["revision"] != 1 {
		t.Errorf("resolved the revision %d times, want 1 (it is cached in refs/)", h.hits["revision"])
	}
}

// TestCacheLayoutMatchesHuggingfaceHub: the point of mirroring this layout is
// interop — another tool's download must satisfy oidio and vice versa. That only
// holds if the directory shape is right.
func TestCacheLayoutMatchesHuggingfaceHub(t *testing.T) {
	h := newHub(map[string]string{"model.onnx": "weights"})
	c := client(t, h)
	if _, err := c.File(context.Background(), "org/name", "model.onnx"); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(c.Cache, "models--org--name")
	for _, sub := range []string{"refs", "blobs", "snapshots"} {
		if _, err := os.Stat(filepath.Join(repo, sub)); err != nil {
			t.Errorf("missing %s/: %v", sub, err)
		}
	}
	ref, err := os.ReadFile(filepath.Join(repo, "refs", "main"))
	if err != nil || string(ref) != h.sha {
		t.Errorf("refs/main = %q, %v; want the commit sha", ref, err)
	}
	snap := filepath.Join(repo, "snapshots", h.sha, "model.onnx")
	if fi, err := os.Lstat(snap); err != nil {
		t.Errorf("no snapshot entry: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("snapshot entry is not a symlink into blobs/ (hub layout stores one blob, links per snapshot)")
	}
}

// TestFileUsesAnExistingCacheEntry: a file another tool already downloaded is
// used as-is, with no request and no second copy.
func TestFileUsesAnExistingCacheEntry(t *testing.T) {
	h := newHub(map[string]string{"model.onnx": "weights"})
	c := client(t, h)

	// Pre-seed the cache exactly as huggingface_hub would leave it.
	repo := filepath.Join(c.Cache, "models--org--name")
	if err := os.MkdirAll(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "refs", "main"), []byte(h.sha), 0o644); err != nil {
		t.Fatal(err)
	}
	snapDir := filepath.Join(repo, "snapshots", h.sha)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "model.onnx"), []byte("someone else's copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := c.File(context.Background(), "org/name", "model.onnx")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "someone else's copy" {
		t.Errorf("re-downloaded over an existing cache entry: %q", b)
	}
	if h.hits["get:model.onnx"] != 0 {
		t.Error("hit the network for a file already in the cache")
	}
}

// TestDirFetchesWholeSubtree: some inputs are directories (Kokoro's
// espeak-ng-data), which a file-at-a-time API cannot express.
func TestDirFetchesWholeSubtree(t *testing.T) {
	h := newHub(map[string]string{
		"espeak-ng-data/phontab":          "a",
		"espeak-ng-data/lang/en":          "b",
		"model.onnx":                      "c",
		"not-espeak/should-not-be-pulled": "d",
	})
	c := client(t, h)

	dir, err := c.Dir(context.Background(), "org/name", "espeak-ng-data")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"phontab", "lang/en"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s from the fetched tree: %v", f, err)
		}
	}
	if h.hits["get:not-espeak/should-not-be-pulled"] != 0 {
		t.Error("pulled a file outside the requested subtree")
	}
	if h.hits["get:model.onnx"] != 0 {
		t.Error("pulled the whole repo instead of the subtree")
	}
}

// TestOfflineUsesCacheOnly: --offline must never reach the network, and must say
// so plainly rather than hanging or half-loading.
func TestOfflineUsesCacheOnly(t *testing.T) {
	h := newHub(map[string]string{"model.onnx": "weights"})
	c := client(t, h)
	c.Offline = true

	if _, err := c.File(context.Background(), "org/name", "model.onnx"); err == nil {
		t.Fatal("offline with a cold cache must fail")
	} else if !strings.Contains(err.Error(), "offline") {
		t.Errorf("err = %v, want it to name offline as the reason", err)
	}
	if h.hits["revision"] != 0 {
		t.Error("offline still hit the network")
	}

	// Warm it, then offline works from cache alone.
	c.Offline = false
	if _, err := c.File(context.Background(), "org/name", "model.onnx"); err != nil {
		t.Fatal(err)
	}
	c.Offline = true
	if _, err := c.File(context.Background(), "org/name", "model.onnx"); err != nil {
		t.Errorf("offline with a warm cache should succeed: %v", err)
	}
}

// TestPartialDownloadIsNotCached: an interrupted fetch must not leave a
// truncated file under the final name, or the next start loads a broken model
// and blames the model.
func TestPartialDownloadIsNotCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/revision/") {
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "sha1"})
			return
		}
		w.Header().Set("ETag", `"blob"`)
		w.Header().Set("Content-Length", "1000") // promise more than we send
		_, _ = w.Write([]byte("short"))
		// Hijack and kill the connection so the body is truncated mid-copy.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Cache: t.TempDir(), Endpoint: srv.URL}
	_, err := c.File(context.Background(), "org/name", "model.onnx")
	if err == nil {
		t.Fatal("a truncated download must be an error")
	}
	blobs := filepath.Join(c.Cache, "models--org--name", "blobs")
	entries, _ := os.ReadDir(blobs)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".dl-") {
			t.Errorf("a partial download was published as %q", e.Name())
		}
	}
}

// TestParseRef covers the bundle reference forms.
func TestParseRef(t *testing.T) {
	got, err := ParseRef("org/name")
	if err != nil || got.Repo != "org/name" || got.Revision != DefaultRevision {
		t.Errorf("ParseRef(org/name) = %+v, %v", got, err)
	}
	got, err = ParseRef("org/name@v2")
	if err != nil || got.Repo != "org/name" || got.Revision != "v2" {
		t.Errorf("ParseRef(org/name@v2) = %+v, %v", got, err)
	}
	for _, bad := range []string{"noslash", "a/b/c", "/leading", "trailing/"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) should fail", bad)
		}
	}
}

// TestCacheDirFollowsHFEnv: oidio must land in whatever cache the machine
// already uses, not a private one.
func TestCacheDirFollowsHFEnv(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", "/explicit/hub")
	if got := CacheDir(); got != "/explicit/hub" {
		t.Errorf("HF_HUB_CACHE ignored: %q", got)
	}
	t.Setenv("HF_HUB_CACHE", "")
	t.Setenv("HF_HOME", "/hfhome")
	if got := CacheDir(); got != filepath.Join("/hfhome", "hub") {
		t.Errorf("HF_HOME ignored: %q", got)
	}
	t.Setenv("HF_HOME", "")
	t.Setenv("HOME", "/somehome")
	if got := CacheDir(); got != filepath.Join("/somehome", ".cache", "huggingface", "hub") {
		t.Errorf("default cache = %q", got)
	}
}

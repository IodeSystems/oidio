// Package hf resolves model files out of the Hugging Face hub cache, fetching
// them on first use.
//
// oidio used to require every model file to be named by absolute path in its
// config, which meant a host could not run oidio without someone first
// downloading ~1.6 GB of ONNX by hand and then writing the paths down. The
// files it needs are all published on the hub, so the honest fix is to fetch
// them the way everything else on the box already does.
//
// It writes the SAME layout huggingface_hub and llama.cpp read
// (models--<org>--<name>/{refs,blobs,snapshots}), rather than a private
// directory, so a model pulled by oidio is already present for anything else
// that looks there — and vice versa: a file another tool downloaded is used as
// is, with no second copy. That interop is the whole reason to mirror a layout
// this fiddly instead of inventing a simpler one.
package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Endpoint is the hub. Overridable for tests and for a mirror.
const Endpoint = "https://huggingface.co"

// DefaultRevision is the branch resolved when a bundle names no revision.
const DefaultRevision = "main"

// CacheDir returns the hub cache root, following the same precedence
// huggingface_hub uses so oidio lands in whatever cache the machine already has:
// $HF_HUB_CACHE, else $HF_HOME/hub, else ~/.cache/huggingface/hub.
func CacheDir() string {
	if d := os.Getenv("HF_HUB_CACHE"); d != "" {
		return d
	}
	if d := os.Getenv("HF_HOME"); d != "" {
		return filepath.Join(d, "hub")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "huggingface", "hub")
	}
	return filepath.Join(home, ".cache", "huggingface", "hub")
}

// Client fetches repo files into the cache.
type Client struct {
	HTTP     *http.Client
	Cache    string               // defaults to CacheDir()
	Endpoint string               // defaults to Endpoint
	Logf     func(string, ...any) // optional progress
	Offline  bool                 // never hit the network; cache hits only
}

// New builds a Client with sensible defaults. The timeout is generous because a
// cold fetch pulls hundreds of megabytes over whatever link the host has.
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Minute}}
}

func (c *Client) cache() string {
	if c.Cache != "" {
		return c.Cache
	}
	return CacheDir()
}

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return Endpoint
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (c *Client) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

// repoDir is the cache directory for a repo: models--<org>--<name>.
func (c *Client) repoDir(repo string) string {
	return filepath.Join(c.cache(), "models--"+strings.ReplaceAll(repo, "/", "--"))
}

// Ref is a parsed bundle reference: "org/name" or "org/name@revision".
type Ref struct {
	Repo     string
	Revision string
}

// ParseRef splits an optional @revision off a repo id.
func ParseRef(s string) (Ref, error) {
	r := Ref{Repo: s, Revision: DefaultRevision}
	if i := strings.LastIndex(s, "@"); i > 0 {
		r.Repo, r.Revision = s[:i], s[i+1:]
	}
	if strings.Count(r.Repo, "/") != 1 || strings.HasPrefix(r.Repo, "/") || strings.HasSuffix(r.Repo, "/") {
		return Ref{}, fmt.Errorf("bundle %q is not a hub repo id (want org/name[@revision])", s)
	}
	return r, nil
}

// commit resolves a revision to its commit sha, preferring the cached ref so a
// warm start needs no network at all.
func (c *Client) commit(ctx context.Context, ref Ref) (string, error) {
	refFile := filepath.Join(c.repoDir(ref.Repo), "refs", ref.Revision)
	cached, cerr := os.ReadFile(refFile)
	if cerr == nil && len(cached) > 0 {
		return strings.TrimSpace(string(cached)), nil
	}
	if c.Offline {
		return "", fmt.Errorf("offline: %s@%s not in %s", ref.Repo, ref.Revision, c.cache())
	}

	url := fmt.Sprintf("%s/api/models/%s/revision/%s", c.endpoint(), ref.Repo, ref.Revision)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve %s@%s: %w", ref.Repo, ref.Revision, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The hub answers 401 for a repo that does not exist as well as one that
		// is private, so a typo in a bundle id reads as an auth failure. Say
		// what it actually means to someone who just mistyped.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("resolve %s@%s: %s — no such bundle, or it is private and needs a token",
				ref.Repo, ref.Revision, resp.Status)
		}
		return "", fmt.Errorf("resolve %s@%s: %s", ref.Repo, ref.Revision, resp.Status)
	}
	var meta struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("resolve %s@%s: %w", ref.Repo, ref.Revision, err)
	}
	if meta.SHA == "" {
		return "", fmt.Errorf("resolve %s@%s: no commit in response", ref.Repo, ref.Revision)
	}
	if err := writeFileAtomic(refFile, []byte(meta.SHA)); err != nil {
		// A cache we cannot write is a performance problem, not a correctness
		// one — the sha is already in hand.
		c.logf("hf: could not cache ref %s@%s: %v", ref.Repo, ref.Revision, err)
	}
	return meta.SHA, nil
}

// File ensures one repo file is present and returns its local path.
func (c *Client) File(ctx context.Context, bundle, rel string) (string, error) {
	ref, err := ParseRef(bundle)
	if err != nil {
		return "", err
	}
	sha, err := c.commit(ctx, ref)
	if err != nil {
		return "", err
	}
	return c.file(ctx, ref, sha, rel)
}

func (c *Client) file(ctx context.Context, ref Ref, sha, rel string) (string, error) {
	rel = path.Clean(strings.TrimPrefix(rel, "./"))
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s: %q is not a path inside the bundle", ref.Repo, rel)
	}
	local := filepath.Join(c.repoDir(ref.Repo), "snapshots", sha, filepath.FromSlash(rel))
	if _, err := os.Stat(local); err == nil {
		return local, nil // already have it (possibly from another tool)
	}
	if c.Offline {
		return "", fmt.Errorf("offline: %s/%s not in %s", ref.Repo, rel, c.cache())
	}

	url := fmt.Sprintf("%s/%s/resolve/%s/%s", c.endpoint(), ref.Repo, sha, rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s/%s: %w", ref.Repo, rel, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s/%s: %s", ref.Repo, rel, resp.Status)
	}

	// Blob name is the etag, exactly as huggingface_hub stores it: the git blob
	// sha1 for a small file, the sha256 oid for an LFS one. Sharing the blob
	// store is what lets two snapshots of the same unchanged file cost one copy.
	blobName := strings.Trim(strings.TrimPrefix(resp.Header.Get("ETag"), "W/"), `"`)
	if blobName == "" {
		blobName = sha + "-" + strings.ReplaceAll(rel, "/", "_")
	}
	blob := filepath.Join(c.repoDir(ref.Repo), "blobs", blobName)

	if _, err := os.Stat(blob); err != nil {
		c.logf("hf: fetching %s/%s", ref.Repo, rel)
		if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
			return "", err
		}
		tmp, err := os.CreateTemp(filepath.Dir(blob), ".dl-*")
		if err != nil {
			return "", err
		}
		_, cerr := io.Copy(tmp, resp.Body)
		closeErr := tmp.Close()
		if cerr != nil || closeErr != nil {
			_ = os.Remove(tmp.Name())
			return "", errors.Join(cerr, closeErr)
		}
		// Rename last: a partially written blob must never be visible under its
		// final name, or the next start treats a truncated model as cached.
		if err := os.Rename(tmp.Name(), blob); err != nil {
			_ = os.Remove(tmp.Name())
			return "", err
		}
	}

	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return "", err
	}
	linkTarget, err := filepath.Rel(filepath.Dir(local), blob)
	if err != nil {
		linkTarget = blob
	}
	if err := os.Symlink(linkTarget, local); err != nil && !errors.Is(err, os.ErrExist) {
		// A filesystem without symlinks still has to work: fall back to a copy.
		if err := copyFile(blob, local); err != nil {
			return "", err
		}
	}
	return local, nil
}

// treeEntry is one item in a repo listing.
type treeEntry struct {
	Type string `json:"type"` // file | directory
	Path string `json:"path"`
}

// Dir ensures every file under a repo subdirectory is present and returns the
// local directory.
//
// Needed because some bundles carry a directory as one input — Kokoro's
// espeak-ng-data is a tree of hundreds of files that sherpa takes as a single
// path — so a file-at-a-time API cannot express it.
func (c *Client) Dir(ctx context.Context, bundle, rel string) (string, error) {
	ref, err := ParseRef(bundle)
	if err != nil {
		return "", err
	}
	sha, err := c.commit(ctx, ref)
	if err != nil {
		return "", err
	}
	rel = path.Clean(strings.TrimPrefix(rel, "./"))
	local := filepath.Join(c.repoDir(ref.Repo), "snapshots", sha, filepath.FromSlash(rel))

	entries, err := c.tree(ctx, ref, sha, rel)
	if err != nil {
		// A complete-looking local tree is better than failing on a listing
		// error (rate limit, offline) when the files are already here.
		if st, serr := os.Stat(local); serr == nil && st.IsDir() {
			return local, nil
		}
		return "", err
	}
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		if _, err := c.file(ctx, ref, sha, e.Path); err != nil {
			return "", err
		}
	}
	return local, nil
}

func (c *Client) tree(ctx context.Context, ref Ref, sha, rel string) ([]treeEntry, error) {
	if c.Offline {
		return nil, fmt.Errorf("offline: cannot list %s/%s", ref.Repo, rel)
	}
	url := fmt.Sprintf("%s/api/models/%s/tree/%s/%s?recursive=1", c.endpoint(), ref.Repo, sha, rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s/%s: %w", ref.Repo, rel, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list %s/%s: %s", ref.Repo, rel, resp.Status)
	}
	var out []treeEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("list %s/%s: %w", ref.Repo, rel, err)
	}
	return out, nil
}

func writeFileAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	return os.Rename(tmp.Name(), p)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	return errors.Join(cerr, out.Close())
}

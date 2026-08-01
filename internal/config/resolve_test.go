package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFetcher records what was asked for and returns a predictable local path,
// so resolution can be tested without a hub.
type fakeFetcher struct {
	files []string // "bundle|rel"
	dirs  []string
}

func (f *fakeFetcher) File(_ context.Context, bundle, rel string) (string, error) {
	f.files = append(f.files, bundle+"|"+rel)
	return "/cache/" + bundle + "/" + rel, nil
}

func (f *fakeFetcher) Dir(_ context.Context, bundle, rel string) (string, error) {
	f.dirs = append(f.dirs, bundle+"|"+rel)
	return "/cache/" + bundle + "/" + rel, nil
}

// TestResolveBundleRelative: a bare path under a bundle is fetched from it.
func TestResolveBundleRelative(t *testing.T) {
	c := &Config{Models: map[string]ModelSpec{
		"stt": {Type: "whisper", Bundle: "org/whisper", Encoder: "e.onnx", Tokens: "t.txt"},
	}}
	f := &fakeFetcher{}
	if err := c.Resolve(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	m := c.Models["stt"]
	if m.Encoder != "/cache/org/whisper/e.onnx" || m.Tokens != "/cache/org/whisper/t.txt" {
		t.Errorf("resolved to %q / %q", m.Encoder, m.Tokens)
	}
}

// TestResolveLeavesLocalPathsAlone: absolute and ./relative paths are files on
// this machine, and must survive untouched — every pre-bundle config is written
// that way and has to keep working.
func TestResolveLeavesLocalPathsAlone(t *testing.T) {
	c := &Config{Models: map[string]ModelSpec{
		"a": {Bundle: "org/x", Encoder: "/abs/e.onnx", Decoder: "./rel/d.onnx", Joiner: "../up/j.onnx"},
		// No bundle at all: a bare relative path is a local path, as before.
		"b": {Encoder: "plain.onnx"},
	}}
	f := &fakeFetcher{}
	if err := c.Resolve(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	a := c.Models["a"]
	if a.Encoder != "/abs/e.onnx" || a.Decoder != "./rel/d.onnx" || a.Joiner != "../up/j.onnx" {
		t.Errorf("local paths were rewritten: %+v", a)
	}
	if got := c.Models["b"].Encoder; got != "plain.onnx" {
		t.Errorf("bundle-less relative path rewritten to %q", got)
	}
	if len(f.files) != 0 {
		t.Errorf("fetched %v for a config that needs no fetching", f.files)
	}
}

// TestResolveCrossBundle: an input published in a DIFFERENT repo from the rest
// of the model carries its own bundle. Diarization needs this — segmentation
// and embedding ship separately from the recogniser they run with.
func TestResolveCrossBundle(t *testing.T) {
	c := &Config{Models: map[string]ModelSpec{
		"d": {
			Bundle:       "org/parakeet",
			Encoder:      "encoder.int8.onnx",
			Segmentation: "org/pyannote:model.onnx",
			Embedding:    "org/wespeaker:model.int8.onnx",
		},
	}}
	f := &fakeFetcher{}
	if err := c.Resolve(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	m := c.Models["d"]
	if m.Encoder != "/cache/org/parakeet/encoder.int8.onnx" {
		t.Errorf("own-bundle path = %q", m.Encoder)
	}
	if m.Segmentation != "/cache/org/pyannote/model.onnx" {
		t.Errorf("cross-bundle segmentation = %q", m.Segmentation)
	}
	if m.Embedding != "/cache/org/wespeaker/model.int8.onnx" {
		t.Errorf("cross-bundle embedding = %q", m.Embedding)
	}
}

// TestResolveKokoroDirAndLexiconList: espeak-ng-data is a TREE fetched whole,
// and kokoro_lexicon is a comma-separated LIST, not one path.
func TestResolveKokoroDirAndLexiconList(t *testing.T) {
	c := &Config{Models: map[string]ModelSpec{
		"tts": {
			Type: "tts", Bundle: "org/kokoro",
			KokoroModel:   "model.int8.onnx",
			KokoroDataDir: "espeak-ng-data",
			KokoroLexicon: "lexicon-us-en.txt,lexicon-gb-en.txt",
		},
	}}
	f := &fakeFetcher{}
	if err := c.Resolve(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	m := c.Models["tts"]
	if len(f.dirs) != 1 || f.dirs[0] != "org/kokoro|espeak-ng-data" {
		t.Errorf("data dir not fetched as a directory: %v", f.dirs)
	}
	want := "/cache/org/kokoro/lexicon-us-en.txt,/cache/org/kokoro/lexicon-gb-en.txt"
	if m.KokoroLexicon != want {
		t.Errorf("lexicon list = %q, want %q", m.KokoroLexicon, want)
	}
}

// TestDefaultRosterResolves: the built-in roster must be internally consistent —
// every path it names resolves, and the four surfaces are all present. This is
// what "configless" rests on.
func TestDefaultRosterResolves(t *testing.T) {
	c := Default()
	for _, name := range []string{"stt", "stt-diarize", "tts", "realtime-stt"} {
		if _, ok := c.Models[name]; !ok {
			t.Errorf("default roster is missing %q", name)
		}
	}
	f := &fakeFetcher{}
	if err := c.Resolve(context.Background(), f); err != nil {
		t.Fatalf("default roster does not resolve: %v", err)
	}
	for name, m := range c.Models {
		if m.Bundle == "" {
			t.Errorf("%s: default has no bundle, so it cannot be fetched", name)
		}
		for label, p := range map[string]string{"encoder": m.Encoder, "tokens": m.Tokens} {
			if p != "" && !strings.HasPrefix(p, "/cache/") {
				t.Errorf("%s: %s did not resolve through a bundle: %q", name, label, p)
			}
		}
	}
	// Diarization's extra models come from their own repos.
	d := c.Models["stt-diarize"]
	if !strings.HasPrefix(d.Segmentation, "/cache/") || !strings.HasPrefix(d.Embedding, "/cache/") {
		t.Errorf("diarize aux models unresolved: seg=%q emb=%q", d.Segmentation, d.Embedding)
	}
}

// TestDiscoverPrefersExplicitAndFailsLoudly: a named config that does not exist
// is an error, never a silent fallback — a caller that passed --config and got
// the defaults would be debugging the wrong machine.
func TestDiscoverPrefersExplicitAndFailsLoudly(t *testing.T) {
	if _, _, err := Discover(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("a missing explicit config must be an error")
	}
	p := write(t, "here.yaml", "models: {}\n")
	got, found, err := Discover(p)
	if err != nil || !found || got != p {
		t.Errorf("Discover(%q) = (%q, %v, %v)", p, got, found, err)
	}
}

// TestLoadDiscoveredMergesDefaults: a config naming one model overrides that one
// and keeps the rest, which is what makes a two-line file worth writing.
func TestLoadDiscoveredMergesDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "oidio.yaml")
	if err := os.WriteFile(p, []byte("addr: \":9999\"\nmodels:\n  stt:\n    type: whisper\n    bundle: me/mine\n    encoder: e.onnx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, src, err := LoadDiscovered(p)
	if err != nil {
		t.Fatal(err)
	}
	if src != p {
		t.Errorf("source = %q", src)
	}
	if c.Addr != ":9999" {
		t.Errorf("addr = %q", c.Addr)
	}
	if got := c.Models["stt"].Bundle; got != "me/mine" {
		t.Errorf("override not applied: stt bundle = %q", got)
	}
	if len(c.Models) != 4 {
		t.Errorf("merge dropped the other defaults: %d models", len(c.Models))
	}
	if c.Models["tts"].Bundle != bundleKokoro {
		t.Error("tts default was lost")
	}
}

// TestLoadDiscoveredDefaultsOff: `defaults: false` serves exactly what is
// written, for a host that must not quietly start a model it never asked for.
func TestLoadDiscoveredDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "oidio.yaml")
	if err := os.WriteFile(p, []byte("defaults: false\nmodels:\n  only:\n    type: whisper\n    encoder: /e.onnx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _, err := LoadDiscovered(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models) != 1 || c.Models["only"].Encoder != "/e.onnx" {
		t.Errorf("defaults were merged despite defaults:false: %+v", c.Models)
	}

	// ...and disabling them while configuring nothing is a refusal, not an
	// empty server that answers "unknown model" to everything.
	q := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(q, []byte("defaults: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadDiscovered(q); err == nil {
		t.Error("defaults:false with no models must be an error")
	}
}

// TestLoadDiscoveredNoConfig: with nothing to find, the roster IS the config.
func TestLoadDiscoveredNoConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir) // no ~/.oidio/config.yml either

	c, src, err := LoadDiscovered("")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models) != 4 {
		t.Errorf("configless start served %d models, want the 4 defaults", len(c.Models))
	}
	if !strings.Contains(src, "built-in") {
		t.Errorf("source = %q, want it to say the defaults were used", src)
	}
}

// TestDiscoverFindsUserHomeConfig: ~/.oidio/config.yml is the host config, kept
// separate from the CLI's ~/.oidio.yml on purpose (see user.go).
func TestDiscoverFindsUserHomeConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // no ./oidio.yaml here
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".oidio"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".oidio", "config.yml")
	if err := os.WriteFile(want, []byte("addr: \":1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got, found, err := Discover("")
	if err != nil || !found || got != want {
		t.Fatalf("Discover() = (%q, %v, %v), want %q", got, found, err, want)
	}
}

package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Fetcher turns a bundle-relative path into a local one, fetching if needed.
// Satisfied by *hf.Client; an interface so config does not depend on the hub
// and so tests can resolve without a network.
type Fetcher interface {
	File(ctx context.Context, bundle, rel string) (string, error)
	Dir(ctx context.Context, bundle, rel string) (string, error)
}

// isLocalPath reports whether a value names a file on THIS machine rather than
// one inside a bundle.
//
// Absolute paths and explicitly-relative ones (./ ../ ~) are local. A bare
// relative path is bundle-relative when the model declares a bundle, and local
// otherwise — which is what keeps every pre-bundle config working unchanged.
func isLocalPath(v string) bool {
	return filepath.IsAbs(v) ||
		strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") ||
		strings.HasPrefix(v, "~/") || v == "~"
}

// splitBundleRef splits an "org/name[@rev]:path" override, reporting whether the
// value carried its own bundle. Split on the FIRST colon: repo ids and revisions
// cannot contain one, so anything after it is the path.
func splitBundleRef(v string) (bundle, rel string, ok bool) {
	i := strings.Index(v, ":")
	if i <= 0 {
		return "", "", false
	}
	b, r := v[:i], v[i+1:]
	// A Windows drive letter ("C:\...") is not a bundle. Neither is anything
	// without the org/name shape.
	if r == "" || !strings.Contains(b, "/") {
		return "", "", false
	}
	return b, r, true
}

// resolveOne turns one path field into a local path.
func resolveOne(ctx context.Context, f Fetcher, bundle, v string, dir bool) (string, error) {
	if v == "" || isLocalPath(v) {
		return v, nil
	}
	b, rel := bundle, v
	if ob, orel, ok := splitBundleRef(v); ok {
		b, rel = ob, orel
	}
	if b == "" {
		return v, nil // no bundle in play: a plain relative path, as before
	}
	if f == nil {
		return "", fmt.Errorf("%q needs bundle %q but no fetcher is configured", rel, b)
	}
	if dir {
		return f.Dir(ctx, b, rel)
	}
	return f.File(ctx, b, rel)
}

// Resolve rewrites every model's path fields to local files, fetching whatever
// the bundles reference and is not already cached.
//
// Called once at startup, before the engines open anything: sherpa takes real
// filesystem paths, so the indirection has to be gone by the time a model
// loads.
func (c *Config) Resolve(ctx context.Context, f Fetcher) error {
	for name, m := range c.Models {
		// Comma-separated list, not a single path — resolve each part.
		lex, err := resolveList(ctx, f, m.Bundle, m.KokoroLexicon)
		if err != nil {
			return fmt.Errorf("model %s: kokoro_lexicon: %w", name, err)
		}
		m.KokoroLexicon = lex

		for _, fld := range []struct {
			label string
			p     *string
			dir   bool
		}{
			{"encoder", &m.Encoder, false},
			{"decoder", &m.Decoder, false},
			{"joiner", &m.Joiner, false},
			{"tokens", &m.Tokens, false},
			{"segmentation", &m.Segmentation, false},
			{"embedding", &m.Embedding, false},
			{"kokoro_model", &m.KokoroModel, false},
			{"kokoro_voices", &m.KokoroVoices, false},
			{"kokoro_tokens", &m.KokoroTokens, false},
			// espeak-ng-data is a TREE that sherpa takes as one path, so the
			// whole subdirectory has to come down, not a single file.
			{"kokoro_data_dir", &m.KokoroDataDir, true},
		} {
			got, err := resolveOne(ctx, f, m.Bundle, *fld.p, fld.dir)
			if err != nil {
				return fmt.Errorf("model %s: %s: %w", name, fld.label, err)
			}
			*fld.p = got
		}
		c.Models[name] = m
	}
	return nil
}

func resolveList(ctx context.Context, f Fetcher, bundle, v string) (string, error) {
	if v == "" {
		return "", nil
	}
	parts := strings.Split(v, ",")
	for i, p := range parts {
		got, err := resolveOne(ctx, f, bundle, strings.TrimSpace(p), false)
		if err != nil {
			return "", err
		}
		parts[i] = got
	}
	return strings.Join(parts, ","), nil
}

// Discover returns the config file oidio should read, and whether it found one.
//
// Order: an explicit path (from --config or $OIDIO_CONFIG) wins and MUST exist —
// a caller that named a file and got silently ignored would be debugging the
// wrong thing. Otherwise ./oidio.yaml, then ~/.oidio/config.yml, then nothing,
// which means the built-in roster.
//
// ~/.oidio/config.yml, not the existing ~/.oidio.yml: that file is the CLI's
// personal defaults (an operator's own endpoint and key), and this is host
// deployment state. Merging them would put a developer's endpoint into a config
// that ships with a machine — see config/user.go.
func Discover(explicit string) (string, bool, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", false, fmt.Errorf("config %s: %w", explicit, err)
		}
		return explicit, true, nil
	}
	candidates := []string{"oidio.yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".oidio", "config.yml"),
			filepath.Join(home, ".oidio", "config.yaml"),
		)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, true, nil
		}
	}
	return "", false, nil
}

// LoadDiscovered resolves the config to use, merging the built-in roster under
// whatever file is found. It returns the config and a human description of where
// it came from, for the startup log — "which models am I even serving" should
// never require guessing.
func LoadDiscovered(explicit string) (*Config, string, error) {
	path, found, err := Discover(explicit)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return Default(), "built-in defaults", nil
	}
	c, err := parse(path)
	if err != nil {
		return nil, "", err
	}
	if c.Defaults != nil && !*c.Defaults {
		if len(c.Models) == 0 {
			return nil, "", fmt.Errorf("%s: defaults are disabled and no models are configured", path)
		}
		return c, path + " (defaults disabled)", nil
	}
	merged := Default()
	merged.Addr = c.Addr
	merged.Defaults = c.Defaults
	for name, m := range c.Models {
		merged.Models[name] = m
	}
	return merged, path, nil
}

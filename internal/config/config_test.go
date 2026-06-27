package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	c, err := Load(write(t, "good.yaml", "addr: \":9\"\nmodels:\n  stt:\n    type: transducer\n    encoder: e.onnx\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":9" || len(c.Models) != 1 || c.Models["stt"].Type != "transducer" || c.Models["stt"].Encoder != "e.onnx" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(write(t, "empty.yaml", "addr: \":9\"\n")); err == nil {
		t.Error("config with no models should error")
	}
	if _, err := Load(write(t, "bad.yaml", "models: [this is not a map")); err == nil {
		t.Error("malformed yaml should error")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("missing file should error")
	}
}

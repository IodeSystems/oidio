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

func TestLoadRealtimeEndpointRules(t *testing.T) {
	c, err := Load(write(t, "rt.yaml",
		"addr: \":9\"\nmodels:\n  realtime-stt:\n    type: realtime\n    encoder: e.onnx\n    decoder: d.onnx\n    joiner: j.onnx\n    tokens: t.txt\n    rule1_silence: 3.0\n    rule2_silence: 1.4\n    rule3_min_utterance: 25\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Models["realtime-stt"]
	if m.Rule1Silence != 3.0 || m.Rule2Silence != 1.4 || m.Rule3MinUtterance != 25 {
		t.Fatalf("endpoint rules not parsed: %+v", m)
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

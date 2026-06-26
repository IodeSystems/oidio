// Package config is oidio's declarative model registry: a YAML file mapping an
// OpenAI `model` name to the engine and model files that serve it. One oidio
// process can host several models (transcribe, diarize, tts, realtime) and
// dispatches on the request's `model` field.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Addr   string               `yaml:"addr"`
	Models map[string]ModelSpec `yaml:"models"`
}

// ModelSpec is one served model. Type selects the engine; the remaining fields
// are the model files that engine needs. Only `transducer` is wired in this
// build; `diarize`/`tts`/`realtime` are recognized so the surface is complete
// and their handlers report "not implemented" rather than "unknown model".
type ModelSpec struct {
	Type string `yaml:"type"` // transducer | diarize | tts | realtime

	// Offline transducer (type: transducer, and the ASR half of type: diarize).
	Encoder string `yaml:"encoder"`
	Decoder string `yaml:"decoder"`
	Joiner  string `yaml:"joiner"`
	Tokens  string `yaml:"tokens"`

	// Diarization (type: diarize) — pyannote segmentation + a speaker-embedding
	// model. ClusterThreshold tunes the speaker count (lower → more speakers);
	// NumClusters>0 forces a known count. Identity is stateless: the request's
	// speaker_confidence controls known-speaker matching, not these.
	Segmentation     string  `yaml:"segmentation"`
	Embedding        string  `yaml:"embedding"`
	ClusterThreshold float32 `yaml:"cluster_threshold"` // default 0.7
	NumClusters      int     `yaml:"num_clusters"`      // 0 = auto (use threshold)
	MinDurationOn    float32 `yaml:"min_duration_on"`
	MinDurationOff   float32 `yaml:"min_duration_off"`

	NumThreads int    `yaml:"num_threads"`
	Language   string `yaml:"language"` // label reported in verbose_json (default "en")
}

// Load reads and validates the config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.Models) == 0 {
		return nil, fmt.Errorf("%s: no models configured", path)
	}
	return &c, nil
}

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

	// TTS (type: tts) — a sherpa-onnx Kokoro model. Voices in voices.bin are
	// indexed by speaker id; `voices` maps an OpenAI `voice` name → that id, and
	// `default_voice` (a name or a bare integer) is used when the request omits or
	// doesn't match one.
	KokoroModel   string         `yaml:"kokoro_model"`
	KokoroVoices  string         `yaml:"kokoro_voices"`
	KokoroTokens  string         `yaml:"kokoro_tokens"`
	KokoroDataDir string         `yaml:"kokoro_data_dir"` // espeak-ng-data
	KokoroLexicon string         `yaml:"kokoro_lexicon"`  // comma-separated lexicon files
	KokoroLang    string         `yaml:"kokoro_lang"`     // optional (e.g. "en-us")
	Voices        map[string]int `yaml:"voices"`
	DefaultVoice  string         `yaml:"default_voice"`

	// Realtime (type: realtime) — a STREAMING transducer (reuses encoder/decoder/
	// joiner/tokens above, pointed at a streaming model). InputSampleRate is the
	// PCM rate the client sends (OpenAI Realtime uses 24000; sherpa resamples).
	InputSampleRate int `yaml:"input_sample_rate"` // default 24000
	// Endpoint rules (sherpa-onnx): an utterance ends when ANY fires. Rule2 is the
	// usual knob — raise it so mid-thought pauses don't cut a turn short; lower it
	// for snappier endpointing. All in seconds.
	Rule1Silence      float32 `yaml:"rule1_silence"`       // trailing silence with no speech yet (default 2.4)
	Rule2Silence      float32 `yaml:"rule2_silence"`       // trailing silence after speech = end of utterance (default 0.8)
	Rule3MinUtterance float32 `yaml:"rule3_min_utterance"` // force an endpoint past this utterance length (default 20)

	NumThreads int    `yaml:"num_threads"`
	Language   string `yaml:"language"` // label reported in verbose_json (default "en")

	// Nice raises the OS scheduling niceness of this model's onnxruntime worker
	// pool (Linux only; higher = lower CPU priority, 0 = default). Wired for the
	// CPU-bound engines (diarize, stt/whisper, realtime), whose inference would
	// otherwise saturate every core and starve the HTTP/WS server into 503s — a
	// niced pool lets request-serving goroutines (nice 0) preempt it. Combined
	// with the automatic thread cap (num_threads is capped to GOMAXPROCS-1 to
	// reserve a core for the server), it keeps oidio responsive under load.
	Nice int `yaml:"nice"`

	// SpokenPunctuation rewrites dictated punctuation words in the final transcript
	// — "question mark" → "?", "exclamation point/mark" → "!". Opt-in (a dictation
	// nicety, not raw transcription). Applies to batch (stt/whisper) and realtime.
	SpokenPunctuation bool `yaml:"spoken_punctuation"`
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

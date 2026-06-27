package engine

import (
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/iodesystems/oidio/internal/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// TTS wraps a sherpa-onnx Kokoro model. A mutex serializes Generate — the shared
// engine's cgo thread-safety is unverified, and synthesis is CPU-bound, so
// serializing costs nothing on a busy box while guaranteeing correctness.
type TTS struct {
	mu          sync.Mutex
	tts         *sherpa.OfflineTts
	rate        int
	voices      map[string]int
	defaultSid  int
	numSpeakers int
}

func NewTTS(spec config.ModelSpec) (*TTS, error) {
	if spec.KokoroModel == "" || spec.KokoroVoices == "" || spec.KokoroTokens == "" {
		return nil, fmt.Errorf("tts (kokoro) needs kokoro_model, kokoro_voices, kokoro_tokens")
	}
	c := sherpa.OfflineTtsConfig{}
	c.Model.Kokoro.Model = spec.KokoroModel
	c.Model.Kokoro.Voices = spec.KokoroVoices
	c.Model.Kokoro.Tokens = spec.KokoroTokens
	c.Model.Kokoro.DataDir = spec.KokoroDataDir
	c.Model.Kokoro.Lexicon = spec.KokoroLexicon
	c.Model.Kokoro.Lang = spec.KokoroLang
	c.Model.NumThreads = orDefault(spec.NumThreads, 2)
	c.Model.Provider = "cpu"

	t := sherpa.NewOfflineTts(&c)
	if t == nil {
		return nil, fmt.Errorf("failed to init kokoro TTS (check model paths)")
	}
	e := &TTS{
		tts:         t,
		rate:        t.SampleRate(),
		voices:      spec.Voices,
		numSpeakers: t.NumSpeakers(),
	}
	e.defaultSid = e.resolve(spec.DefaultVoice)
	return e, nil
}

// Voice resolves an OpenAI `voice` to a speaker id: a bare integer, else a name
// in the config map, else the configured default.
func (t *TTS) Voice(name string) int {
	if name == "" {
		return t.defaultSid
	}
	if sid := t.resolve(name); sid >= 0 {
		return sid
	}
	return t.defaultSid
}

// resolve returns a sid for a name/integer, or -1 if unknown.
func (t *TTS) resolve(name string) int {
	if name == "" {
		return -1
	}
	if n, err := strconv.Atoi(name); err == nil {
		return n
	}
	if sid, ok := t.voices[name]; ok {
		return sid
	}
	return -1
}

func (t *TTS) SampleRate() int  { return t.rate }
func (t *TTS) NumSpeakers() int { return t.numSpeakers }

// VoiceNames lists the configured voice aliases (sorted), for the capabilities
// manifest. Callers may also pass a bare integer speaker id (0..NumSpeakers-1).
func (t *TTS) VoiceNames() []string {
	names := make([]string, 0, len(t.voices))
	for n := range t.voices {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Synthesize renders text to normalized float32 samples at SampleRate().
func (t *TTS) Synthesize(text string, sid int, speed float32) []float32 {
	if speed <= 0 {
		speed = 1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.tts.Generate(text, sid, speed)
	if a == nil {
		return nil
	}
	return a.Samples
}

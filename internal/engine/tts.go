package engine

import (
	"fmt"
	"strconv"

	"github.com/iodesystems/oidio/internal/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// TTS wraps a sherpa-onnx Kokoro model. Synthesis is serialized — the underlying
// engine is not safe for concurrent Generate calls.
type TTS struct {
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

// Synthesize renders text to normalized float32 samples at SampleRate().
func (t *TTS) Synthesize(text string, sid int, speed float32) []float32 {
	if speed <= 0 {
		speed = 1
	}
	a := t.tts.Generate(text, sid, speed)
	if a == nil {
		return nil
	}
	return a.Samples
}

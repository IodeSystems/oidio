// Package engine wraps sherpa-onnx-go. STT is the offline (batch) recognizer.
package engine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/iodesystems/oidio/internal/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// STT is an offline transducer recognizer. The sherpa recognizer is shared, and
// its cgo thread-safety is unverified, so a mutex serializes decodes — cheap for
// CPU-bound inference, and concurrency is already bounded upstream (corrallm
// slots). Throughput is unaffected on a saturated box; correctness is guaranteed.
type STT struct {
	mu   sync.Mutex
	rec  *sherpa.OfflineRecognizer
	lang string
}

// Result is one transcription: the text plus per-token pieces and timestamps
// (seconds), which feed verbose_json segments and the SSE token deltas.
type Result struct {
	Text       string
	Tokens     []string
	Timestamps []float32
}

// NewSTT builds an offline recognizer from a model spec. type=whisper builds a
// sherpa Whisper recognizer (cased + punctuated output); anything else builds a
// transducer (encoder/decoder/joiner/tokens, normalized upper-case output).
func NewSTT(spec config.ModelSpec) (*STT, error) {
	lang := spec.Language
	if lang == "" {
		lang = "en"
	}
	c := sherpa.OfflineRecognizerConfig{}
	c.FeatConfig.SampleRate = 16000
	c.FeatConfig.FeatureDim = 80
	c.ModelConfig.Tokens = spec.Tokens
	c.ModelConfig.NumThreads = orDefault(spec.NumThreads, 4)
	c.ModelConfig.Provider = "cpu"
	c.DecodingMethod = "greedy_search"

	switch spec.Type {
	case "whisper":
		if spec.Encoder == "" || spec.Decoder == "" || spec.Tokens == "" {
			return nil, fmt.Errorf("whisper needs encoder, decoder, and tokens")
		}
		c.ModelConfig.Whisper.Encoder = spec.Encoder
		c.ModelConfig.Whisper.Decoder = spec.Decoder
		c.ModelConfig.Whisper.Language = lang
		c.ModelConfig.Whisper.Task = "transcribe"
	default: // transducer
		if spec.Encoder == "" || spec.Decoder == "" || spec.Joiner == "" || spec.Tokens == "" {
			return nil, fmt.Errorf("transducer needs encoder, decoder, joiner, and tokens")
		}
		c.ModelConfig.Transducer.Encoder = spec.Encoder
		c.ModelConfig.Transducer.Decoder = spec.Decoder
		c.ModelConfig.Transducer.Joiner = spec.Joiner
	}

	rec := sherpa.NewOfflineRecognizer(&c)
	if rec == nil {
		return nil, fmt.Errorf("failed to init recognizer (check model paths)")
	}
	return &STT{rec: rec, lang: lang}, nil
}

// Language is the label reported in verbose_json responses.
func (s *STT) Language() string { return s.lang }

// Transcribe decodes the whole waveform and returns the result.
func (s *STT) Transcribe(samples []float32, sampleRate int) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := sherpa.NewOfflineStream(s.rec)
	defer sherpa.DeleteOfflineStream(st)
	st.AcceptWaveform(sampleRate, samples)
	s.rec.Decode(st)
	r := st.GetResult()
	return Result{
		Text:       strings.TrimSpace(r.Text),
		Tokens:     r.Tokens,
		Timestamps: r.Timestamps,
	}
}

// Close releases the recognizer's native resources.
func (s *STT) Close() { sherpa.DeleteOfflineRecognizer(s.rec) }

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

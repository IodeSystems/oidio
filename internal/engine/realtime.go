package engine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/iodesystems/oidio/internal/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Realtime is a streaming transducer for live transcription. The recognizer is
// shared; each WS session gets its own OnlineStream. Decode/result calls are
// serialized through a mutex (one onnx session, many streams).
type Realtime struct {
	rec    *sherpa.OnlineRecognizer
	mu     sync.Mutex
	inRate int
}

// RTEvent is one thing to send the client: an incremental Delta and/or a final
// Completed transcript (on a silence endpoint or commit).
type RTEvent struct {
	Delta     string
	Completed string
}

func NewRealtime(spec config.ModelSpec) (*Realtime, error) {
	if spec.Encoder == "" || spec.Decoder == "" || spec.Joiner == "" || spec.Tokens == "" {
		return nil, fmt.Errorf("realtime needs a STREAMING encoder/decoder/joiner/tokens")
	}
	c := sherpa.OnlineRecognizerConfig{}
	c.FeatConfig = sherpa.FeatureConfig{SampleRate: 16000, FeatureDim: 80}
	c.ModelConfig.Transducer.Encoder = spec.Encoder
	c.ModelConfig.Transducer.Decoder = spec.Decoder
	c.ModelConfig.Transducer.Joiner = spec.Joiner
	c.ModelConfig.Tokens = spec.Tokens
	c.ModelConfig.NumThreads = orDefault(spec.NumThreads, 2)
	c.ModelConfig.Provider = "cpu"
	c.DecodingMethod = "greedy_search"
	c.EnableEndpoint = 1
	c.Rule1MinTrailingSilence = orDefaultF(spec.Rule1Silence, 2.4)
	c.Rule2MinTrailingSilence = orDefaultF(spec.Rule2Silence, 0.8)
	c.Rule3MinUtteranceLength = orDefaultF(spec.Rule3MinUtterance, 20)

	rec := sherpa.NewOnlineRecognizer(&c)
	if rec == nil {
		return nil, fmt.Errorf("failed to init online recognizer (check streaming model paths)")
	}
	inRate := spec.InputSampleRate
	if inRate <= 0 {
		inRate = 24000
	}
	return &Realtime{rec: rec, inRate: inRate}, nil
}

// RTSession is one live connection's decode state.
type RTSession struct {
	e      *Realtime
	stream *sherpa.OnlineStream
	last   string
}

func (e *Realtime) NewSession() *RTSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	return &RTSession{e: e, stream: sherpa.NewOnlineStream(e.rec)}
}

func (s *RTSession) Close() {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	sherpa.DeleteOnlineStream(s.stream)
}

// Accept feeds a chunk of audio (at the configured input rate) and returns any
// deltas / endpoint completions it produced.
func (s *RTSession) Accept(samples []float32) []RTEvent {
	return s.AcceptRate(samples, s.e.inRate)
}

// AcceptRate is Accept with an explicit sample rate — used by the WebRTC path,
// where Opus decodes at 48 kHz regardless of the configured input rate.
func (s *RTSession) AcceptRate(samples []float32, rate int) []RTEvent {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	s.stream.AcceptWaveform(rate, samples)
	return s.drain(false)
}

// Finalize flushes the buffered audio (commit / end of stream) and emits the
// final transcript.
func (s *RTSession) Finalize() []RTEvent {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	pad := make([]float32, s.e.inRate*6/10) // 0.6s trailing silence to trip the endpoint
	s.stream.AcceptWaveform(s.e.inRate, pad)
	s.stream.InputFinished()
	return s.drain(true)
}

func (s *RTSession) drain(final bool) []RTEvent {
	var ev []RTEvent
	for s.e.rec.IsReady(s.stream) {
		s.e.rec.Decode(s.stream)
	}
	text := strings.TrimSpace(s.e.rec.GetResult(s.stream).Text)
	if text != s.last {
		delta := text
		if strings.HasPrefix(text, s.last) { // transducer text grows monotonically
			delta = text[len(s.last):]
		}
		if strings.TrimSpace(delta) != "" {
			ev = append(ev, RTEvent{Delta: delta})
		}
		s.last = text
	}
	if final || s.e.rec.IsEndpoint(s.stream) {
		if text != "" {
			ev = append(ev, RTEvent{Completed: text})
		}
		s.e.rec.Reset(s.stream)
		s.last = ""
	}
	return ev
}

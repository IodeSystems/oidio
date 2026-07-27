// Package server exposes the OpenAI audio API over sherpa-onnx-go. One process
// hosts the models in the config and dispatches on the request's `model` field.
package server

import (
	"fmt"
	"net/http"

	"github.com/iodesystems/oidio/internal/config"
	"github.com/iodesystems/oidio/internal/engine"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// SherpaVersion reports the linked sherpa-onnx version.
func SherpaVersion() string { return sherpa.GetVersion() }

type model struct {
	name string
	typ  string
	stt  *engine.STT // non-nil for type: transducer
	// textModel is another model's name whose recogniser transcribes this
	// model's diarized turns (type: diarize only). Resolved after every model is
	// loaded, since it may name one declared later in the file.
	textModel string
	diar      *engine.Diarizer // non-nil for type: diarize
	tts       *engine.TTS      // non-nil for type: tts
	rt        *engine.Realtime // non-nil for type: realtime
}

type Server struct {
	addr   string
	models map[string]*model
	mux    *http.ServeMux
}

// New loads every configured model and wires the routes.
func New(cfg *config.Config) (*Server, error) {
	s := &Server{addr: cfg.Addr, models: map[string]*model{}}
	for name, spec := range cfg.Models {
		m := &model{name: name, typ: spec.Type}
		switch spec.Type {
		case "transducer", "whisper":
			stt, err := engine.NewSTT(spec)
			if err != nil {
				return nil, fmt.Errorf("model %q: %w", name, err)
			}
			m.stt = stt
		case "diarize":
			diar, err := engine.NewDiarizer(spec)
			if err != nil {
				return nil, fmt.Errorf("model %q: %w", name, err)
			}
			m.diar = diar
			m.textModel = spec.TextModel
		case "tts":
			tts, err := engine.NewTTS(spec)
			if err != nil {
				return nil, fmt.Errorf("model %q: %w", name, err)
			}
			m.tts = tts
		case "realtime":
			rt, err := engine.NewRealtime(spec)
			if err != nil {
				return nil, fmt.Errorf("model %q: %w", name, err)
			}
			m.rt = rt
		default:
			return nil, fmt.Errorf("model %q: unknown type %q", name, spec.Type)
		}
		s.models[name] = m
	}
	// Resolved after the load loop: a text_model may name a model declared later
	// in the file, and a dangling reference must fail at startup rather than
	// silently falling back to the transducer on every request.
	for name, m := range s.models {
		if m.textModel == "" {
			continue
		}
		tm, ok := s.models[m.textModel]
		if !ok {
			return nil, fmt.Errorf("model %q: text_model %q is not a configured model", name, m.textModel)
		}
		if tm.stt == nil {
			return nil, fmt.Errorf("model %q: text_model %q is type %q, which has no recogniser", name, m.textModel, tm.typ)
		}
	}
	s.routes()
	return s, nil
}

// textRecogniser returns the recogniser that should transcribe m's turns, or nil
// to use m's own transducer. Validated at startup, so a miss here means the
// model simply did not ask for a blend.
func (s *Server) textRecogniser(m *model) *engine.STT {
	if m.textModel == "" {
		return nil
	}
	if tm, ok := s.models[m.textModel]; ok {
		return tm.stt
	}
	return nil
}

func (s *Server) routes() {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("POST /v1/audio/transcriptions", s.handleTranscriptions)
	s.mux.HandleFunc("POST /v1/audio/translations", s.handleTranscriptions)
	s.mux.HandleFunc("POST /v1/audio/speech", s.handleSpeech)
	s.mux.HandleFunc("GET /v1/realtime", s.handleRealtime)        // WebSocket transport
	s.mux.HandleFunc("POST /v1/realtime", s.handleRealtimeWebRTC) // WebRTC transport (SDP offer)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("GET /v1/capabilities", s.handleCapabilities)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
}

func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.addr, s.mux)
}

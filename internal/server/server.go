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
	stt  *engine.STT      // non-nil for type: transducer
	diar *engine.Diarizer // non-nil for type: diarize
	tts  *engine.TTS      // non-nil for type: tts
	rt   *engine.Realtime // non-nil for type: realtime
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
	s.routes()
	return s, nil
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

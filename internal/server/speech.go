package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/iodesystems/oidio/internal/audio"
)

// speechRequest is the OpenAI /v1/audio/speech body.
type speechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float32 `json:"speed"`
}

// handleSpeech serves POST /v1/audio/speech (TTS): JSON in, binary audio out.
func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
	var req speechRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error")
		return
	}
	m := s.models[req.Model]
	if m == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("model %q not found", req.Model), "invalid_request_error")
		return
	}
	if m.tts == nil {
		writeErr(w, http.StatusNotImplemented,
			fmt.Sprintf("model %q (type %s) does not synthesize speech", req.Model, m.typ), "invalid_request_error")
		return
	}
	if req.Input == "" {
		writeErr(w, http.StatusBadRequest, "missing 'input'", "invalid_request_error")
		return
	}
	format, ok := audio.LookupFormat(req.ResponseFormat)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unsupported response_format "+req.ResponseFormat, "invalid_request_error")
		return
	}

	samples := m.tts.Synthesize(req.Input, m.tts.Voice(req.Voice), req.Speed)
	if len(samples) == 0 {
		writeErr(w, http.StatusInternalServerError, "synthesis produced no audio", "server_error")
		return
	}
	out, err := audio.Encode(r.Context(), samples, m.tts.SampleRate(), format)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", format.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(len(out)))
	_, _ = w.Write(out)
}

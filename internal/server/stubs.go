package server

import "net/http"

// handleSpeech — POST /v1/audio/speech (TTS). Engine exists in sherpa-onnx-go
// (OfflineTts + Kokoro); handler lands in a follow-up slice.
func (s *Server) handleSpeech(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, http.StatusNotImplemented, "TTS (/v1/audio/speech) not yet implemented", "not_implemented")
}

// handleRealtime — GET /v1/realtime (live STT over WebSocket, OpenAI Realtime
// transcription schema). Follow-up slice.
func (s *Server) handleRealtime(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, http.StatusNotImplemented, "realtime WS (/v1/realtime) not yet implemented", "not_implemented")
}

// handleModels — GET /v1/models (OpenAI catalog shape).
func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0, len(s.models))
	for name, m := range s.models {
		data = append(data, map[string]any{
			"id": name, "object": "model", "owned_by": "oidio", "type": m.typ,
		})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

// handleHealth — GET /health, /healthz.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"status": "ok", "sherpa_onnx": SherpaVersion()})
}

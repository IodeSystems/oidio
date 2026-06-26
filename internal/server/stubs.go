package server

import "net/http"

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

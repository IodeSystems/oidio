package server

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/iodesystems/oidio/internal/engine"
)

// corrallm fronts auth and origin; this server trusts its proxy.
var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// handleRealtime serves GET /v1/realtime — live transcription over WebSocket in
// the OpenAI Realtime transcription schema. Single read loop per connection;
// decode happens inline, so writes are never concurrent.
func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("model")
	m := s.models[name]
	if m == nil || m.rt == nil {
		writeErr(w, http.StatusNotFound,
			fmt.Sprintf("model %q has no realtime transcription", name), "invalid_request_error")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sess := m.rt.NewSession()
	defer sess.Close()

	send := func(v any) error { return conn.WriteJSON(v) }
	_ = send(map[string]any{"type": "session.created", "session": map[string]any{"object": "realtime.transcription_session"}})

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "session.update":
			_ = send(map[string]any{"type": "session.updated", "session": map[string]any{"object": "realtime.transcription_session"}})
		case "input_audio_buffer.append":
			pcm, err := base64.StdEncoding.DecodeString(msg.Audio)
			if err != nil {
				continue
			}
			emit(send, sess.Accept(pcm16ToF32(pcm)))
		case "input_audio_buffer.commit":
			emit(send, sess.Finalize())
		case "input_audio_buffer.clear":
			// no buffered state to clear beyond the stream; ignore
		}
	}
}

func emit(send func(any) error, events []engine.RTEvent) {
	for _, e := range events {
		if e.Delta != "" {
			_ = send(map[string]any{"type": "conversation.item.input_audio_transcription.delta", "delta": e.Delta})
		}
		if e.Completed != "" {
			_ = send(map[string]any{"type": "conversation.item.input_audio_transcription.completed", "transcript": e.Completed})
		}
	}
}

func pcm16ToF32(b []byte) []float32 {
	n := len(b) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(int16(binary.LittleEndian.Uint16(b[i*2:]))) / 32768.0
	}
	return out
}

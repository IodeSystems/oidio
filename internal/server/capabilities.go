package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// capabilityOf maps an engine type to the OpenAI-style capability (delivery
// surface). Mirrors corrallm so a client/LLM sees consistent names.
func capabilityOf(typ string) string {
	switch typ {
	case "transducer", "diarize":
		return "audio.stt"
	case "tts":
		return "audio.tts"
	case "realtime":
		return "audio.realtime"
	default:
		return "unknown"
	}
}

// handleCapabilities returns a self-describing manifest: the models oidio hosts,
// which surface each serves, and example requests. Public (no key) and JSON.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	scheme, ws := "http", "ws"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme, ws = "https", "wss"
	}
	base := scheme + "://" + r.Host
	wsBase := ws + "://" + r.Host

	names := make([]string, 0, len(s.models))
	for n := range s.models {
		names = append(names, n)
	}
	sort.Strings(names)

	byCap := map[string][]string{}
	models := make([]map[string]any, 0, len(s.models))
	for _, name := range names {
		m := s.models[name]
		c := capabilityOf(m.typ)
		byCap[c] = append(byCap[c], name)
		md := map[string]any{"id": name, "type": m.typ, "capability": c}
		switch {
		case m.diar != nil:
			md["diarization"] = true
		case m.tts != nil:
			md["voices"] = m.tts.VoiceNames()
			md["formats"] = []string{"mp3", "opus", "aac", "flac", "wav", "pcm"}
		case m.rt != nil:
			md["transports"] = []string{"websocket", "webrtc"}
		}
		models = append(models, md)
	}

	first := func(cap, fallback string) string {
		if v := byCap[cap]; len(v) > 0 {
			return v[0]
		}
		return fallback
	}
	sttM := first("audio.stt", "<stt-model>")
	ttsM := first("audio.tts", "<tts-model>")
	rtM := first("audio.realtime", "<realtime-model>")

	endpoints := []map[string]any{
		{
			"path": "/v1/audio/transcriptions", "method": "POST", "capability": "audio.stt",
			"description": "Speech-to-text (Whisper-compatible). multipart/form-data: model + file. response_format json/verbose_json/text/srt/vtt; stream=true → SSE deltas. A diarize model adds speaker UUIDs on verbose_json segments plus a stateless `speakers` array (uuid + voiceprint + similarity); request args speaker_confidence, known_speakers.",
			"models":      byCap["audio.stt"],
			"example":     fmt.Sprintf("curl -sS %s/v1/audio/transcriptions -F model=%s -F file=@speech.wav", base, sttM),
		},
		{
			"path": "/v1/audio/translations", "method": "POST", "capability": "audio.stt",
			"description": "Speech-to-English translation; same shape as transcriptions.",
			"models":      byCap["audio.stt"],
		},
		{
			"path": "/v1/audio/speech", "method": "POST", "capability": "audio.tts",
			"description": "Text-to-speech. JSON {model,input,voice,response_format,speed} → audio bytes.",
			"models":      byCap["audio.tts"],
			"example":     fmt.Sprintf(`curl -sS %s/v1/audio/speech -H 'Content-Type: application/json' -d '{"model":"%s","input":"hello","voice":"af_heart","response_format":"mp3"}' --output speech.mp3`, base, ttsM),
		},
		{
			"path": "/v1/realtime", "method": "GET (WebSocket) | POST (WebRTC SDP)", "capability": "audio.realtime",
			"description": "Live transcription, OpenAI Realtime transcription schema. WebSocket: stream base64 PCM16 via input_audio_buffer.append, receive …delta/…completed. WebRTC: POST an application/sdp offer → SDP answer; Opus mic track + an `oai-events` data channel.",
			"models":      byCap["audio.realtime"],
			"example": map[string]any{
				"websocket": fmt.Sprintf("%s/v1/realtime?model=%s&intent=transcription", wsBase, rtM),
				"webrtc":    fmt.Sprintf("curl -sS %s/v1/realtime?model=%s -H 'Content-Type: application/sdp' --data-binary @offer.sdp", base, rtM),
			},
		},
		{"path": "/v1/models", "method": "GET", "description": "Model catalog.", "example": fmt.Sprintf("curl -sS %s/v1/models", base)},
		{"path": "/v1/capabilities", "method": "GET", "description": "This manifest."},
		{"path": "/health", "method": "GET", "description": "Liveness."},
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"service":              "oidio",
		"description":          "OpenAI-compatible audio server (STT, diarization, TTS, realtime) on sherpa-onnx. Point any OpenAI client at this base URL.",
		"base_url":             base,
		"openai_compatible":    true,
		"sherpa_onnx":          SherpaVersion(),
		"models":               models,
		"models_by_capability": byCap,
		"endpoints":            endpoints,
	})
}

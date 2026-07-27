package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/iodesystems/oidio/internal/audio"
	"github.com/iodesystems/oidio/internal/engine"
)

// handleTranscriptions serves POST /v1/audio/transcriptions and /translations.
// Standard OpenAI multipart: model + file (+ response_format, stream, language).
func (s *Server) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data: "+err.Error(), "invalid_request_error")
		return
	}
	name := r.FormValue("model")
	m := s.models[name]
	if m == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("model %q not found", name), "invalid_request_error")
		return
	}
	if m.stt == nil && m.diar == nil {
		writeErr(w, http.StatusNotImplemented,
			fmt.Sprintf("model %q (type %s) has no batch transcription", name, m.typ), "invalid_request_error")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing form field 'file'", "invalid_request_error")
		return
	}
	defer file.Close()

	samples, err := audio.DecodePCM(r.Context(), file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	format := r.FormValue("response_format")
	if m.diar != nil {
		// Diarization needs the whole clip; streaming doesn't apply.
		s.handleDiarize(w, r, m, samples, format)
		return
	}

	res := m.stt.Transcribe(samples, audio.SampleRate)
	duration := float64(len(samples)) / float64(audio.SampleRate)

	if r.FormValue("stream") == "true" {
		s.streamTranscript(w, res)
		return
	}
	switch r.FormValue("response_format") {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, res.Text)
	case "verbose_json":
		writeJSON(w, verboseJSON(res, m.stt.Language(), duration))
	case "srt":
		w.Header().Set("Content-Type", "application/x-subrip")
		fmt.Fprint(w, fmt.Sprintf("1\n%s --> %s\n%s\n", srtTime(0), srtTime(duration), res.Text))
	case "vtt":
		w.Header().Set("Content-Type", "text/vtt")
		fmt.Fprint(w, fmt.Sprintf("WEBVTT\n\n%s --> %s\n%s\n", vttTime(0), vttTime(duration), res.Text))
	default: // "json" / ""
		writeJSON(w, map[string]any{"text": res.Text})
	}
}

// segment mirrors OpenAI's verbose_json segment object, plus an additive
// `speaker` field — a per-request stable speaker UUID on the diarize path, empty
// on the plain STT path. See docs/PLAN.md.
type segment struct {
	ID      int     `json:"id"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
	// Overlap marks a span that runs concurrently with another speaker's — the
	// crosstalk the pyannote segmentation model detects. Emitted so a caller can
	// route these to human review instead of trusting the attribution: an
	// interjection inside someone else's turn is where misattribution happens.
	Overlap bool `json:"overlap,omitempty"`
}

func verboseJSON(res engine.Result, lang string, dur float64) map[string]any {
	return map[string]any{
		"task":     "transcribe",
		"language": lang,
		"duration": dur,
		"text":     res.Text,
		"segments": []segment{{ID: 0, Start: 0, End: dur, Text: res.Text}},
	}
}

// streamTranscript emits OpenAI's streaming-transcription SSE events. The offline
// recognizer decodes the whole clip, so we replay its tokens as text deltas — the
// wire shape clients expect (transcript.text.delta…, then transcript.text.done).
func (s *Server) streamTranscript(w http.ResponseWriter, res engine.Result) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported", "server_error")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	for _, tok := range res.Tokens {
		send(map[string]any{"type": "transcript.text.delta", "delta": strings.ReplaceAll(tok, "▁", " ")})
	}
	send(map[string]any{"type": "transcript.text.done", "text": res.Text})
}

func srtTime(s float64) string { return clock(s, ",") }
func vttTime(s float64) string { return clock(s, ".") }

func clock(sec float64, frac string) string {
	ms := int(sec*1000 + 0.5)
	h, ms := ms/3600000, ms%3600000
	m, ms := ms/60000, ms%60000
	s, ms := ms/1000, ms%1000
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, frac, ms)
}

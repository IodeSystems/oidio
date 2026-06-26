package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// knownSpeaker is a voiceprint the caller already holds (from a prior response),
// passed back so UUIDs stay stable across requests. oidio stores nothing.
type knownSpeaker struct {
	UUID      string    `json:"uuid"`
	Embedding []float32 `json:"embedding"`
}

type speakerOut struct {
	UUID       string             `json:"uuid"`
	Embedding  []float32          `json:"embedding"`
	Similarity map[string]float64 `json:"similarity"`
}

// handleDiarize serves the diarize model type: speaker-labeled transcription with
// stateless identity. Each speaker gets a UUID (matched to a caller-supplied
// known_speaker above speaker_confidence, else freshly minted); the response
// carries each speaker's voiceprint and its cosine similarity to the others, so
// the caller owns persistence and any UUID↔name mapping.
func (s *Server) handleDiarize(w http.ResponseWriter, r *http.Request, m *model, samples []float32, format string) {
	conf := float32(0.5)
	if v := r.FormValue("speaker_confidence"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			conf = float32(f)
		}
	}
	var known []knownSpeaker
	if v := r.FormValue("known_speakers"); v != "" {
		if err := json.Unmarshal([]byte(v), &known); err != nil {
			writeErr(w, http.StatusBadRequest, "known_speakers must be JSON [{uuid,embedding}]: "+err.Error(), "invalid_request_error")
			return
		}
	}

	res := m.diar.Process(samples)

	// local cluster id → UUID: reuse the best known speaker above threshold, else new.
	uuidOf := make(map[int]string, len(res.Speakers))
	embOf := make(map[int][]float32, len(res.Speakers))
	for _, v := range res.Speakers {
		id, best := "", conf
		for _, k := range known {
			if c := cosine(v.Embedding, k.Embedding); c >= best {
				best, id = c, k.UUID
			}
		}
		if id == "" {
			id = uuid.NewString()
		}
		uuidOf[v.Local] = id
		embOf[v.Local] = v.Embedding
	}

	speakers := make([]speakerOut, 0, len(res.Speakers))
	for _, v := range res.Speakers {
		sim := map[string]float64{}
		for _, o := range res.Speakers {
			if o.Local != v.Local {
				sim[uuidOf[o.Local]] = round(float64(cosine(v.Embedding, o.Embedding)), 4)
			}
		}
		speakers = append(speakers, speakerOut{UUID: uuidOf[v.Local], Embedding: v.Embedding, Similarity: sim})
	}

	segs := make([]segment, 0, len(res.Segments))
	for i, sg := range res.Segments {
		segs = append(segs, segment{
			ID: i, Start: round(sg.Start, 3), End: round(sg.End, 3), Text: sg.Text, Speaker: uuidOf[sg.Speaker],
		})
	}

	if format == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, res.Text)
		return
	}
	// json and verbose_json both carry the additive speaker data; a plain OpenAI
	// client just reads .text and ignores segments/speakers.
	writeJSON(w, map[string]any{
		"task":     "transcribe",
		"language": m.diar.Language(),
		"duration": res.Duration,
		"text":     res.Text,
		"segments": segs,
		"speakers": speakers,
	})
}

func cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / float32(math.Sqrt(float64(na))*math.Sqrt(float64(nb)))
}

func round(f float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(f*p) / p
}

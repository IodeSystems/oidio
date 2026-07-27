package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/iodesystems/oidio/internal/engine"
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

	// How the voiceprint was built. `blended` means the crosstalk-free sample was
	// too thin and overlapped audio was used, so this print describes more than
	// one voice and should not be trusted for cross-recording matching.
	CleanSeconds float64 `json:"clean_seconds"`
	TotalSeconds float64 `json:"total_seconds"`
	Blended      bool    `json:"blended,omitempty"`
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
	// Fragment handling. A 0.3s interjection carries too little signal for a
	// 512-dim voiceprint (the embedder wants seconds), so it fails to match its
	// own speaker's cluster and mints a phantom one instead — which is why a
	// four-person hearing can come back with eight speakers.
	//
	// Assignment is a far easier problem than discovery, so when the caller
	// supplies known voiceprints a fragment gets a SECOND pass at a lower bar:
	// it only has to resemble a speaker we already know, not found a cluster.
	minDur := 3.0
	if v := r.FormValue("speaker_min_duration"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			minDur = f
		}
	}
	rescue := float32(0.35)
	if v := r.FormValue("speaker_rescue_confidence"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			rescue = float32(f)
		}
	}
	// Opt-in: a fragment matching nothing is left UNATTRIBUTED rather than
	// minting a speaker who does not exist. Off by default so existing callers
	// see no change; on, it is the honest answer for a legal transcript, where a
	// wrong attribution is worse than an absent one.
	unattributed := r.FormValue("unattributed_fragments") == "true"

	var opts engine.DiarOpts
	opts.ClusterThreshold = formFloat32(r, "cluster_threshold")
	// absent → model default; present (including 0, which disables) → override
	if v := r.FormValue("speaker_merge_threshold"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			m := float32(f)
			opts.MergeThreshold = &m
		}
	}
	if v := r.FormValue("num_clusters"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.NumClusters = n
		}
	}
	var known []knownSpeaker
	if v := r.FormValue("known_speakers"); v != "" {
		if err := json.Unmarshal([]byte(v), &known); err != nil {
			writeErr(w, http.StatusBadRequest, "known_speakers must be JSON [{uuid,embedding}]: "+err.Error(), "invalid_request_error")
			return
		}
	}

	res := m.diar.Process(samples, opts)

	// Total speech per detected cluster — what separates a person from a
	// fragment. Measured from the segments, since the voiceprint alone says
	// nothing about how much audio produced it.
	spoken := map[int]float64{}
	for _, sg := range res.Segments {
		spoken[sg.Speaker] += sg.End - sg.Start
	}
	uuidOf, speakers := resolveSpeakersFrag(res.Speakers, known, conf, uuid.NewString,
		spoken, minDur, rescue, unattributed)

	segs := make([]segment, 0, len(res.Segments))
	for i, sg := range res.Segments {
		segs = append(segs, segment{
			ID: i, Start: round(sg.Start, 3), End: round(sg.End, 3), Text: sg.Text, Speaker: uuidOf[sg.Speaker],
		})
	}
	markOverlaps(segs)

	// Blend: keep diarization's turns and speakers, but take the WORDS from a
	// better recogniser. See ModelSpec.TextModel — the transducer here is
	// uppercase and unpunctuated, Whisper is neither, and a speaker turn is
	// already the short span Whisper wants.
	if rec := s.textRecogniser(m); rec != nil {
		retranscribe(segs, rec, samples)
		res.Text = joinSegments(segs)
	}

	switch format {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, res.Text)
		return
	case "srt":
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		fmt.Fprint(w, diarCues(segs, false))
		return
	case "vtt":
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		fmt.Fprint(w, diarCues(segs, true))
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

// resolveSpeakers is the stateless-identity core: map each detected speaker's
// local cluster id to a UUID — reusing a caller-supplied known speaker's UUID
// when cosine similarity ≥ conf, else minting one via newID — and build the
// speakers[] array with each speaker's cosine similarity to the others. Pure and
// deterministic given newID, so it's unit-tested without any models.
// resolveSpeakers is the original contract: every detected cluster is a speaker.
// minDur 0 means nothing qualifies as a fragment, so no rescue and no
// suppression — identical behavior to before fragment handling existed.
func resolveSpeakers(detected []engine.SpeakerVoice, known []knownSpeaker, conf float32, newID func() string) (map[int]string, []speakerOut) {
	return resolveSpeakersFrag(detected, known, conf, newID, nil, 0, conf, false)
}

// resolveSpeakersFrag adds fragment handling: a cluster with less than minDur of
// speech did not earn the right to be a new speaker. It gets a second matching
// pass against KNOWN voiceprints at the lower `rescue` bar — recognising a voice
// you already hold needs far less signal than discovering one — and, when
// unattributed is set, is left with no speaker rather than inventing one.
func resolveSpeakersFrag(detected []engine.SpeakerVoice, known []knownSpeaker, conf float32, newID func() string,
	spoken map[int]float64, minDur float64, rescue float32, unattributed bool) (map[int]string, []speakerOut) {

	uuidOf := make(map[int]string, len(detected))
	rescued := make(map[int]bool, len(detected))
	for _, v := range detected {
		id, best := "", conf
		for _, k := range known {
			if c := engine.Cosine(v.Embedding, k.Embedding); c >= best {
				best, id = c, k.UUID
			}
		}
		if id != "" {
			uuidOf[v.Local] = id
			continue
		}
		// Too little speech to have founded a cluster honestly.
		if spoken[v.Local] < minDur {
			if rescue < conf {
				bestR := rescue
				for _, k := range known {
					if c := engine.Cosine(v.Embedding, k.Embedding); c >= bestR {
						bestR, id = c, k.UUID
					}
				}
			}
			if id != "" {
				uuidOf[v.Local] = id
				rescued[v.Local] = true
				continue
			}
			if unattributed {
				uuidOf[v.Local] = "" // no speaker rather than an invented one
				continue
			}
		}
		uuidOf[v.Local] = newID()
	}

	speakers := make([]speakerOut, 0, len(detected))
	seen := map[string]bool{}
	for _, v := range detected {
		id := uuidOf[v.Local]
		// An unattributed fragment is not a speaker, and a rescued one is already
		// represented by the known speaker it merged into — emitting either again
		// would hand the caller a duplicate to persist.
		if id == "" || rescued[v.Local] || seen[id] {
			continue
		}
		seen[id] = true
		sim := map[string]float64{}
		for _, o := range detected {
			if oid := uuidOf[o.Local]; o.Local != v.Local && oid != "" && oid != id {
				sim[oid] = round(float64(engine.Cosine(v.Embedding, o.Embedding)), 4)
			}
		}
		speakers = append(speakers, speakerOut{
			UUID: id, Embedding: v.Embedding, Similarity: sim,
			CleanSeconds: round(v.CleanSeconds, 1), TotalSeconds: round(v.TotalSeconds, 1),
			Blended: v.Blended,
		})
	}
	return uuidOf, speakers
}

// diarSampleRate is the rate every decoded stream is resampled to before it
// reaches an engine; spans are cut in these samples.
const diarSampleRate = 16000

// spanOf returns samples[start,end) in seconds, clamped to the buffer.
func spanOf(samples []float32, start, end float64) []float32 {
	a, b := int(start*diarSampleRate), int(end*diarSampleRate)
	if a < 0 {
		a = 0
	}
	if b > len(samples) {
		b = len(samples)
	}
	if a >= b {
		return nil
	}
	return samples[a:b]
}

// whisperWindow is the receptive field of a Whisper encoder. Audio beyond it in
// a single call is silently ignored — not truncated with an error — which is how
// a 12-minute hearing came back as 75 words under a segment claiming to span the
// whole file. A turn longer than this is split rather than trusted.
const whisperWindow = 30.0

// whisperOverlap re-reads a little of the previous chunk so a word split across
// the boundary is still seen whole by one of them.
const whisperOverlap = 0.5

// retranscribe replaces each turn's text using rec, leaving times and speakers
// untouched. A turn that yields nothing keeps its original text: the transducer's
// ALL-CAPS guess is worse than Whisper's, but both beat an empty segment.
//
// Superseded by blendWords, which assigns individual WORDS to speakers instead of
// re-decoding each turn. Kept only for models whose text recogniser cannot supply
// the timestamps blendWords needs to align against.
//
// Re-decoding per turn cannot represent crosstalk: two concurrent turns are each
// handed the same audio, and the recogniser returns the dominant voice for both,
// putting identical words in two mouths. Subtracting the overlap instead deletes
// far more than it dedupes — measured at 26% of a hearing's words — because span
// boundaries over-claim. Only a word-level assignment avoids both.
func retranscribe(segs []segment, rec *engine.STT, samples []float32) {
	for i := range segs {
		if txt := transcribeSpan(rec, samples, segs[i].Start, segs[i].End); txt != "" {
			segs[i].Text = txt
		}
	}
}

// transcribeSpan runs rec over [start,end), chunked to the recogniser's window.
func transcribeSpan(rec *engine.STT, samples []float32, start, end float64) string {
	var parts []string
	for t := start; t < end; t += whisperWindow - whisperOverlap {
		hi := t + whisperWindow
		if hi > end {
			hi = end
		}
		seg := spanOf(samples, t, hi)
		// Sub-quarter-second clips make the recogniser return nothing useful and
		// have made it return a nil result outright.
		if len(seg) < diarSampleRate/4 {
			break
		}
		if txt := strings.TrimSpace(rec.Transcribe(seg, diarSampleRate).Text); txt != "" {
			parts = append(parts, txt)
		}
		if hi >= end {
			break
		}
	}
	return strings.Join(parts, " ")
}

// joinSegments rebuilds the whole-file transcript after a blend, so `text` and
// `segments` cannot disagree about what was said.
func joinSegments(segs []segment) string {
	parts := make([]string, 0, len(segs))
	for _, sg := range segs {
		if t := strings.TrimSpace(sg.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// markOverlaps flags every segment that runs concurrently with a DIFFERENT
// speaker's. Same-speaker adjacency is not overlap, just segmentation.
func markOverlaps(segs []segment) {
	for i := range segs {
		for j := range segs {
			if i == j || segs[i].Speaker == segs[j].Speaker {
				continue
			}
			if segs[i].Start < segs[j].End && segs[j].Start < segs[i].End {
				segs[i].Overlap = true
				break
			}
		}
	}
}

// speakerLabels maps each speaker UUID to a short readable label ("Speaker 1",
// …) numbered by first appearance. Subtitle cues need something a human can read;
// the JSON responses still carry the UUIDs.
func speakerLabels(segs []segment) map[string]string {
	labels := map[string]string{}
	for _, sg := range segs {
		if sg.Speaker != "" && labels[sg.Speaker] == "" {
			labels[sg.Speaker] = fmt.Sprintf("Speaker %d", len(labels)+1)
		}
	}
	return labels
}

// diarCues renders speaker-labeled segments as SRT (or WebVTT when vtt is set),
// one cue per speaker turn.
//
// Segment times come from ASR token timestamps, which mark where each token
// STARTS — so a turn's End is the onset of its last word, not where the audio
// stops, and a one-token turn has End == Start. Cues are therefore stretched to
// the next turn's start, capped at cueExtend so a cue doesn't span a long
// silence, and floored to cueMin so no cue is zero-length.
func diarCues(segs []segment, vtt bool) string {
	const (
		cueExtend = 2.0 // max seconds a cue may be stretched past its last token
		cueMin    = 0.5 // shortest cue we'll emit
	)
	labels := speakerLabels(segs)
	tf := srtTime
	var b strings.Builder
	if vtt {
		tf = vttTime
		b.WriteString("WEBVTT\n\n")
	}
	n := 0
	for i, sg := range segs {
		if sg.Text == "" {
			continue
		}
		end := sg.End + cueExtend
		if i+1 < len(segs) && segs[i+1].Start < end {
			end = segs[i+1].Start
		}
		if end < sg.Start+cueMin {
			end = sg.Start + cueMin
		}
		n++
		fmt.Fprintf(&b, "%d\n%s --> %s\n", n, tf(sg.Start), tf(end))
		if l := labels[sg.Speaker]; l != "" {
			fmt.Fprintf(&b, "%s: ", l)
		}
		fmt.Fprintf(&b, "%s\n\n", sg.Text)
	}
	return b.String()
}

// formFloat32 reads an optional float form value; 0 when absent or unparseable,
// which every consumer treats as "use the configured default".
func formFloat32(r *http.Request, key string) float32 {
	v := r.FormValue(key)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 32)
	if err != nil {
		return 0
	}
	return float32(f)
}

func round(f float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(f*p) / p
}

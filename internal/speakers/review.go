package speakers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/iodesystems/oidio/internal/audio"
	"github.com/iodesystems/oidio/internal/config"
	"github.com/iodesystems/oidio/internal/engine"
)

// Transcript is the diarize response as written by the server. Only the fields
// review needs are modelled; the rest round-trips untouched through Raw so
// applying a change cannot silently drop anything the server emitted.
type Transcript struct {
	Raw      map[string]any
	Segments []Segment
	Speakers map[string][]float32

	// Retained after Embed so split proposals can embed arbitrary sub-spans
	// without decoding the audio or loading the model a second time.
	pcm []float32
	emb *engine.Embedder
}

// audioRate is the rate every span is cut and embedded at, matching the decode.
const audioRate = 16000

// SpanEmbedder exposes the loaded audio and model as a function over time
// ranges, so the analysis can evaluate a cut without knowing about either.
// Returns nil before Embed has run.
func (t *Transcript) SpanEmbedder() SpanEmbedder {
	if t.emb == nil || len(t.pcm) == 0 {
		return nil
	}
	return func(start, end float64) []float32 {
		a, b := int(start*audioRate), int(end*audioRate)
		if a < 0 {
			a = 0
		}
		if b > len(t.pcm) {
			b = len(t.pcm)
		}
		if a >= b {
			return nil
		}
		return t.emb.Embed(t.pcm[a:b], audioRate)
	}
}

// LoadTranscript parses a verbose_json diarize result.
func LoadTranscript(path string) (*Transcript, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var typed struct {
		Segments []struct {
			ID      int     `json:"id"`
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			Text    string  `json:"text"`
			Speaker string  `json:"speaker"`
		} `json:"segments"`
		Speakers []struct {
			UUID      string    `json:"uuid"`
			Embedding []float32 `json:"embedding"`
		} `json:"speakers"`
	}
	if err := json.Unmarshal(b, &typed); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(typed.Segments) == 0 {
		return nil, fmt.Errorf("%s: no segments — is this a diarize result?", path)
	}
	t := &Transcript{Raw: raw, Speakers: map[string][]float32{}}
	for _, s := range typed.Segments {
		t.Segments = append(t.Segments, Segment{
			Index: s.ID, Start: s.Start, End: s.End, Text: s.Text, Speaker: s.Speaker,
		})
	}
	for _, s := range typed.Speakers {
		t.Speakers[s.UUID] = s.Embedding
	}
	return t, nil
}

// Embed fills in each segment's own voiceprint from the audio.
//
// Per-segment embeddings are not in the response and deliberately so — they
// would more than double it — so review re-derives them. That also means review
// needs the ORIGINAL audio, which is the honest requirement: deciding a passage
// was attributed to the wrong voice cannot be done from text.
func (t *Transcript) Embed(audioPath string, spec config.ModelSpec, minSeconds float64) error {
	f, err := os.Open(audioPath)
	if err != nil {
		return err
	}
	defer f.Close()
	pcm, err := audio.DecodePCM(context.Background(), f)
	if err != nil {
		return fmt.Errorf("decode %s: %w", audioPath, err)
	}
	emb, err := engine.NewEmbedder(spec)
	if err != nil {
		return err
	}
	t.pcm, t.emb = pcm, emb
	const rate = audioRate
	for i := range t.Segments {
		s := &t.Segments[i]
		if s.Duration() < minSeconds {
			continue
		}
		a, b := int(s.Start*rate), int(s.End*rate)
		if a < 0 {
			a = 0
		}
		if b > len(pcm) {
			b = len(pcm)
		}
		if a >= b {
			continue
		}
		s.Embedding = emb.Embed(pcm[a:b], rate)
	}
	return nil
}

// Apply rewrites segment speakers per the approved moves and joins, returning
// the updated document. It does NOT write to disk; the caller decides that.
func (t *Transcript) Apply(a Analysis) map[string]any {
	to := map[int]string{}
	for _, m := range a.Moves {
		to[m.Segment] = m.To
	}
	// A join is expressed as a rename so that a later reader sees one speaker,
	// not two that happen to be marked equivalent.
	rename := map[string]string{}
	for _, j := range a.Joins {
		rename[j.B] = j.A
	}
	resolve := func(id string) string {
		for i := 0; i < 8; i++ { // bounded: a join chain cannot cycle forever
			n, ok := rename[id]
			if !ok {
				return id
			}
			id = n
		}
		return id
	}

	segs, _ := t.Raw["segments"].([]any)
	for _, v := range segs {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(float64)
		cur, _ := m["speaker"].(string)
		if n, ok := to[int(id)]; ok {
			cur = n
		}
		m["speaker"] = resolve(cur)
	}

	// Splits are applied LAST and renumber every id, so they must not run before
	// the move/join lookups above — those are keyed by the original ids.
	if len(a.Splits) > 0 {
		bySeg := map[int]Split{}
		for _, sp := range a.Splits {
			bySeg[sp.Segment] = sp
		}
		grown := make([]any, 0, len(segs)+len(a.Splits))
		for _, v := range segs {
			m, ok := v.(map[string]any)
			if !ok {
				grown = append(grown, v)
				continue
			}
			id, _ := m["id"].(float64)
			sp, ok := bySeg[int(id)]
			if !ok {
				grown = append(grown, v)
				continue
			}
			left := cloneSeg(m)
			right := cloneSeg(m)
			left["end"] = sp.At
			left["text"] = sp.LeftText
			left["speaker"] = resolve(sp.Left)
			right["start"] = sp.At
			right["text"] = sp.RightText
			right["speaker"] = resolve(sp.Right)
			grown = append(grown, left, right)
		}
		for i, v := range grown {
			if m, ok := v.(map[string]any); ok {
				m["id"] = float64(i)
			}
		}
		t.Raw["segments"] = grown
		segs = grown
	}
	// Drop the speaker entries a join folded away, so speakers[] and segments[]
	// cannot disagree about who exists.
	if sp, ok := t.Raw["speakers"].([]any); ok {
		kept := make([]any, 0, len(sp))
		for _, v := range sp {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if u, _ := m["uuid"].(string); rename[u] != "" {
				continue
			}
			kept = append(kept, v)
		}
		t.Raw["speakers"] = kept
	}
	return t.Raw
}

// cloneSeg copies a segment map so the two halves of a split do not alias each
// other — mutating one would otherwise silently rewrite the other.
func cloneSeg(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Report writes the human-readable review. This is the product when --apply is
// not given, so it has to carry enough for a decision: what changes, how sure
// each signal is, and where the two disagree.
func Report(w io.Writer, a Analysis, agreeOnly bool) {
	fmt.Fprintf(w, "CLUSTERS (%d)\n", len(a.Clusters))
	for _, c := range a.Clusters {
		fmt.Fprintf(w, "  %s  %7.1fs  %d segments\n", short(c.UUID), c.Seconds, c.Count)
	}
	if a.Skipped > 0 {
		fmt.Fprintf(w, "\n%d segments (%.1fs) too short to judge — left untouched\n", a.Skipped, a.SkippedSecs)
	}

	fmt.Fprintf(w, "\nPROPOSED MOVES (%d)\n", len(a.Moves))
	if len(a.Moves) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, m := range a.Moves {
		mark, note := verdictMark(m.Semantic)
		if agreeOnly && m.Semantic.Contradicts() {
			continue
		}
		fmt.Fprintf(w, "\n  %s seg %d [%.1f-%.1fs]  %s -> %s   (proposed by %s)\n", mark, m.Segment, m.Start, m.End,
			short(m.From), short(m.To), m.Origin)
		fmt.Fprintf(w, "      voice: %.3f -> %.3f (margin %+.3f, confidence %.2f)%s\n",
			m.FromCos, m.ToCos, m.Margin, m.Acoustic, note)
		fmt.Fprintf(w, "      %q\n", truncate(oneLine(m.Text), 150))
	}

	fmt.Fprintf(w, "\nPROPOSED JOINS (%d)\n", len(a.Joins))
	if len(a.Joins) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, j := range a.Joins {
		mark, note := verdictMark(j.Semantic)
		if agreeOnly && j.Semantic.Contradicts() {
			continue
		}
		fmt.Fprintf(w, "  %s %s + %s  cos %.3f (confidence %.2f, proposed by %s)%s\n",
			mark, short(j.A), short(j.B), j.Cos, j.Acoustic, j.Origin, note)
	}

	fmt.Fprintf(w, "\nPROPOSED SPLITS (%d)\n", len(a.Splits))
	if len(a.Splits) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, sp := range a.Splits {
		mark, note := verdictMark(sp.Semantic)
		if agreeOnly && sp.Semantic.Contradicts() {
			continue
		}
		fmt.Fprintf(w, "\n  %s seg %d cut at %.1fs  -> %s | %s\n", mark, sp.Segment, sp.At,
			short(sp.Left), short(sp.Right))
		fmt.Fprintf(w, "      voice: whole %.3f -> halves %.3f / %.3f (gain %+.3f, confidence %.2f)%s\n",
			sp.Base, sp.LeftCos, sp.RightCos, sp.Gain, sp.Acoustic, note)
		fmt.Fprintf(w, "      L: %q\n", truncate(oneLine(sp.LeftText), 100))
		fmt.Fprintf(w, "      R: %q\n", truncate(oneLine(sp.RightText), 100))
	}

	if n := disagreements(a); n > 0 {
		fmt.Fprintf(w, "\n%d proposal(s) where voice and content DISAGREE — review these first.\n", n)
	}
}

// verdictMark renders the two signals' agreement. "✓✓" is both; "✓?" is acoustic
// evidence the reviewer could not corroborate; "✓✗" is a live disagreement.
func verdictMark(v *Verdict) (string, string) {
	switch {
	case v == nil:
		return "✓?", ""
	case v.Agrees():
		return "✓✓", fmt.Sprintf("\n      content: AGREES (%.2f) — %s", v.Confidence, v.Reason)
	case v.Contradicts():
		return "✓✗", fmt.Sprintf("\n      content: DISAGREES (%.2f) — %s", v.Confidence, v.Reason)
	default:
		return "✓~", fmt.Sprintf("\n      content: cannot tell (%.2f) — %s", v.Confidence, v.Reason)
	}
}

func disagreements(a Analysis) int {
	n := 0
	for _, m := range a.Moves {
		if m.Semantic.Contradicts() {
			n++
		}
	}
	for _, j := range a.Joins {
		if j.Semantic.Contradicts() {
			n++
		}
	}
	for _, sp := range a.Splits {
		if sp.Semantic.Contradicts() {
			n++
		}
	}
	return n
}

// Filter keeps only proposals both signals agree on. Used by --apply-agreed so
// that writing is never done on an unreviewed or contested proposal.
func Filter(a Analysis) Analysis {
	out := a
	out.Moves, out.Joins, out.Splits = nil, nil, nil
	for _, m := range a.Moves {
		if m.Semantic.Agrees() {
			out.Moves = append(out.Moves, m)
		}
	}
	for _, j := range a.Joins {
		if j.Semantic.Agrees() {
			out.Joins = append(out.Joins, j)
		}
	}
	for _, sp := range a.Splits {
		if sp.Semantic.Agrees() {
			out.Splits = append(out.Splits, sp)
		}
	}
	return out
}

// SortClusters keeps report order stable across runs.
func SortClusters(cs []Cluster) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Seconds > cs[j].Seconds })
}

func short(u string) string {
	if len(u) > 8 {
		return u[:8]
	}
	return u
}

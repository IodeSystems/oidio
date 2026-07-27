// Package speakers is the post-hoc review layer: it reads a diarize result and
// proposes corrections to WHO said what, without re-running diarization.
//
// Two failures need different operations, and conflating them makes both worse:
//
//   - Over-splitting — one person spread across several clusters, usually because
//     mic distance or volume moved their embedding. The fix is a JOIN.
//   - Contamination — one cluster holding several people, because segments landed
//     in the wrong cluster. The fix is a MOVE. Joining a contaminated cluster
//     merges two real people; on a real hearing the two clusters involved scored
//     0.62 cosine, so no join threshold could have been right.
//
// Both operate in speaker-embedding space rather than on transcript order:
// sequence says nothing about identity, and a mid-sentence speaker flip is
// exactly the case where order misleads.
package speakers

import (
	"math"
	"sort"
	"strings"
)

// Segment is one attributed run of text under review.
type Segment struct {
	Index     int
	Start     float64
	End       float64
	Text      string
	Speaker   string
	Embedding []float32 // nil when the segment was too short to embed
}

func (s Segment) Duration() float64 { return s.End - s.Start }

// Cluster is one detected speaker: its centroid and what it currently holds.
//
// The centroid is omitted from JSON — it is 512 floats per speaker, it made the
// diarize response itself half embeddings, and no consumer of a review needs the
// vector when it has the similarities computed from it.
type Cluster struct {
	UUID     string    `json:"uuid"`
	Centroid []float32 `json:"-"`
	Seconds  float64   `json:"seconds"`
	Count    int       `json:"segments"`
}

// Move proposes reattributing one segment to a different cluster.
type Move struct {
	Segment  int     `json:"segment"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Text     string  `json:"text"`
	From     string  `json:"from"`
	To       string  `json:"to"`
	FromCos  float64 `json:"from_cos"`
	ToCos    float64 `json:"to_cos"`
	Margin   float64 `json:"margin"`
	Acoustic float64 `json:"acoustic_confidence"`

	// Semantic is an optional second opinion from content rather than voice.
	// Nil when no reviewer ran.
	Semantic *Verdict `json:"semantic,omitempty"`

	// Origin names which signal PROPOSED this, as opposed to which judged it.
	// "voice" for the embedding pass, "content" for the reviewer. Without it a
	// content-origin proposal with weak acoustic support is indistinguishable
	// from an acoustic proposal the reviewer rejected, and they mean opposite
	// things.
	Origin string `json:"origin"`
}

// Split proposes cutting one segment in two because diarization missed a speaker
// change inside it.
//
// The third operation, and the only one that changes the segment BOUNDARIES
// rather than the labels on them. MOVE assumes the boundary is right and the
// label wrong; JOIN assumes the labels name one person. Neither can express "two
// people are inside this one turn", which is what diarization produces when it
// misses a change entirely.
type Split struct {
	Segment   int     `json:"segment"`
	At        float64 `json:"at"`
	Left      string  `json:"left"`
	Right     string  `json:"right"`
	LeftCos   float64 `json:"left_cos"`
	RightCos  float64 `json:"right_cos"`
	Base      float64 `json:"base_cos"`
	Gain      float64 `json:"gain"`
	LeftText  string  `json:"left_text"`
	RightText string  `json:"right_text"`
	Acoustic  float64 `json:"acoustic_confidence"`

	Semantic *Verdict `json:"semantic,omitempty"`
	Origin    string  `json:"origin"`
}

// Which signal proposed a correction, as opposed to which judged it.
const (
	OriginVoice   = "voice"
	OriginContent = "content"
)

// SpanEmbedder returns the voiceprint for an arbitrary time range, or nil when
// the range is too short to embed. Passed in rather than held, so the decision
// rules stay testable on synthetic voices with no audio and no models.
type SpanEmbedder func(start, end float64) []float32

// Join proposes that two clusters are one speaker.
type Join struct {
	A        string   `json:"a"`
	B        string   `json:"b"`
	Cos      float64  `json:"cos"`
	Acoustic float64  `json:"acoustic_confidence"`
	Semantic *Verdict `json:"semantic,omitempty"`
	Origin   string   `json:"origin"`
}

// Verdict is a reviewer's opinion, acoustic-independent.
//
// Status is three-way on purpose. Folding "the content contradicts this" into
// "the content cannot tell" makes the two indistinguishable in the output, and
// they call for opposite responses: a contradiction is evidence against the
// acoustic proposal, while insufficient content is no evidence at all and should
// leave the acoustic finding standing on its own. The first run of this reviewer
// returned "disagree" for all 36 proposals, several of them reading "too thin to
// definitively assign" — a signal that had been flattened into a rejection.
type Verdict struct {
	Status     string  `json:"status"` // agree | disagree | insufficient
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

func (v *Verdict) Agrees() bool       { return v != nil && v.Status == "agree" }
func (v *Verdict) Contradicts() bool  { return v != nil && v.Status == "disagree" }
func (v *Verdict) Insufficient() bool { return v != nil && v.Status == "insufficient" }

// Params tune what gets proposed. Zero values are not useful defaults; use
// DefaultParams.
type Params struct {
	// MinEmbedSeconds is the shortest segment whose own embedding is trusted.
	// Below it a segment describes a voice too thinly to move on that evidence —
	// the same floor the voiceprint fallback uses.
	MinEmbedSeconds float64

	// MinMargin is how much better the new cluster must score than the current
	// one. A move is a claim that diarization was WRONG, so a near-tie is not
	// enough; without this, segments oscillate between similar voices.
	MinMargin float64

	// JoinCos is the centroid similarity at which two clusters are proposed as
	// one speaker.
	JoinCos float64

	// Passes re-derives centroids from the surviving segments and re-proposes.
	// One pass matches against contaminated centroids — the very defect being
	// corrected — so the first round's moves clean the reference for the next.
	Passes int

	// MinSplitGain is how much better BOTH halves must be explained than the whole
	// segment before a cut is proposed. Same discipline as MinMargin: cutting is a
	// claim that diarization missed a speaker entirely, so a marginal improvement
	// is not enough — and unlike a move, a bad cut leaves a sentence in pieces.
	MinSplitGain float64

	// SplitStep is how finely candidate cut points are scanned. Without word
	// timestamps in the transcript there is nothing better to align to; when the
	// API exposes words[] this becomes "cut at the largest intra-segment pause".
	SplitStep float64
}

func DefaultParams() Params {
	return Params{
		MinEmbedSeconds: 2.0, MinMargin: 0.08, JoinCos: 0.85, Passes: 3,
		MinSplitGain: 0.08, SplitStep: 0.5,
	}
}

// Analysis is everything the review found.
type Analysis struct {
	Clusters []Cluster `json:"clusters"`
	Moves    []Move    `json:"moves"`
	Joins    []Join    `json:"joins"`
	Splits   []Split   `json:"splits"`

	// Skipped counts segments too short to judge. Reported rather than silently
	// ignored: a review that examined 60% of a file should not read like a clean
	// bill of health.
	Skipped     int     `json:"skipped_too_short"`
	SkippedSecs float64 `json:"skipped_seconds"`
}

// Analyze proposes moves, joins and splits. Pure apart from the supplied
// embedder: no audio, no models, no network, so the decision rules are testable
// on synthetic embeddings.
//
// embed may be nil, in which case no splits are proposed — deciding that a turn
// contains two people requires embedding parts of it, which text cannot support.
func Analyze(segs []Segment, p Params, embed SpanEmbedder) Analysis {
	if p.Passes < 1 {
		p.Passes = 1
	}
	assigned := make([]string, len(segs))
	for i, s := range segs {
		assigned[i] = s.Speaker
	}

	var a Analysis
	for pass := 0; pass < p.Passes; pass++ {
		cents := centroids(segs, assigned)
		moved := false
		for i, s := range segs {
			if len(s.Embedding) == 0 || s.Duration() < p.MinEmbedSeconds {
				continue
			}
			best, bestCos, curCos := "", -2.0, -2.0
			for _, c := range cents {
				sim := Cosine(s.Embedding, c.Centroid)
				if c.UUID == assigned[i] {
					curCos = sim
				}
				if sim > bestCos {
					best, bestCos = c.UUID, sim
				}
			}
			if best != "" && best != assigned[i] && bestCos-curCos >= p.MinMargin {
				assigned[i] = best
				moved = true
			}
		}
		if !moved {
			break
		}
	}

	// Report against the ORIGINAL attribution, not the last pass's intermediate
	// state: the reviewer needs to see the net change they are approving.
	final := centroids(segs, assigned)
	byUUID := map[string]Cluster{}
	for _, c := range final {
		byUUID[c.UUID] = c
	}
	for i, s := range segs {
		if len(s.Embedding) == 0 || s.Duration() < p.MinEmbedSeconds {
			a.Skipped++
			a.SkippedSecs += s.Duration()
			continue
		}
		if assigned[i] == s.Speaker {
			continue
		}
		to, from := byUUID[assigned[i]], byUUID[s.Speaker]
		toCos, fromCos := Cosine(s.Embedding, to.Centroid), Cosine(s.Embedding, from.Centroid)
		a.Moves = append(a.Moves, Move{
			Segment: s.Index, Start: s.Start, End: s.End, Text: s.Text,
			From: s.Speaker, To: assigned[i],
			FromCos: round(fromCos), ToCos: round(toCos), Margin: round(toCos - fromCos),
			Acoustic: round(confidence(toCos - fromCos)), Origin: OriginVoice,
		})
	}
	sort.Slice(a.Moves, func(i, j int) bool { return a.Moves[i].Margin > a.Moves[j].Margin })

	a.Clusters = final
	for i := 0; i < len(final); i++ {
		for j := i + 1; j < len(final); j++ {
			c := Cosine(final[i].Centroid, final[j].Centroid)
			if float64(c) >= p.JoinCos {
				a.Joins = append(a.Joins, Join{
					A: final[i].UUID, B: final[j].UUID,
					Cos: round(float64(c)), Acoustic: round(confidence(float64(c) - p.JoinCos)),
					Origin: OriginVoice,
				})
			}
		}
	}
	sort.Slice(a.Joins, func(i, j int) bool { return a.Joins[i].Cos > a.Joins[j].Cos })

	if embed != nil {
		a.Splits = proposeSplits(segs, final, p, embed)
	}
	return a
}

// proposeSplits looks for a cut that explains BOTH halves better than any single
// speaker explains the whole.
//
// The aggregate is min(left, right), not the mean: a cut is only justified when
// each side independently improves. Averaging lets one excellent half carry a
// worthless one, which is how a cut lands in the middle of a sentence.
func proposeSplits(segs []Segment, cents []Cluster, p Params, embed SpanEmbedder) []Split {
	var out []Split
	for _, s := range segs {
		// Both halves must clear the embedding floor, so a segment must be at
		// least twice it before a cut can be evaluated at all.
		if s.Duration() < 2*p.MinEmbedSeconds || len(s.Embedding) == 0 {
			continue
		}
		base, _ := bestCluster(s.Embedding, cents)
		var best Split
		bestGain := math.Inf(-1)
		found := false
		for t := s.Start + p.MinEmbedSeconds; t <= s.End-p.MinEmbedSeconds; t += p.SplitStep {
			le, re := embed(s.Start, t), embed(t, s.End)
			if len(le) == 0 || len(re) == 0 {
				continue
			}
			lCos, lID := bestCluster(le, cents)
			rCos, rID := bestCluster(re, cents)
			if lID == rID || lID == "" || rID == "" {
				continue // one speaker explains both halves; nothing to cut
			}
			// The two halves must be attributed to voices that are actually
			// DIFFERENT. Without this, a cut gets proposed between two ids the join
			// analysis considers the same person — incoherent on its face, and the
			// bulk of what this produced on a real hearing: four of six proposals
			// cut between a pair scoring 0.865, all of them mid-sentence.
			//
			// The underlying bias is that a shorter span embeds more cleanly, so
			// among enough centroids each half finds one it beats the whole
			// segment against. Requiring the two to be far apart is what makes the
			// gain evidence of a speaker change rather than of brevity.
			if Cosine(centroidOf(lID, cents), centroidOf(rID, cents)) >= p.JoinCos {
				continue
			}
			// Strictly greater, so a tie keeps the EARLIEST cut rather than
			// whichever the scan happened to reach last. Scan order is not
			// evidence, and on synthetic input every candidate can tie.
			gain := math.Min(lCos, rCos) - base
			if gain < p.MinSplitGain || gain <= bestGain {
				continue
			}
			bestGain = gain
			lt, rt := splitText(s.Text, (t-s.Start)/s.Duration())
			best = Split{
				Segment: s.Index, At: round(t), Left: lID, Right: rID,
				LeftCos: round(lCos), RightCos: round(rCos), Base: round(base),
				Gain: round(gain), LeftText: lt, RightText: rt,
				Acoustic: round(confidence(gain)), Origin: OriginVoice,
			}
			found = true
		}
		if found {
			out = append(out, best)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Gain > out[j].Gain })
	return out
}

func centroidOf(id string, cents []Cluster) []float32 {
	for _, c := range cents {
		if c.UUID == id {
			return c.Centroid
		}
	}
	return nil
}

func bestCluster(e []float32, cents []Cluster) (float64, string) {
	best, id := -2.0, ""
	for _, c := range cents {
		if sim := Cosine(e, c.Centroid); sim > best {
			best, id = sim, c.UUID
		}
	}
	return best, id
}

// splitText divides a segment's words at the given fraction of its duration.
//
// Approximate, and knowingly so: the transcript carries no word timestamps, so
// this assumes an even speaking rate. It affects only where the TEXT is cut, not
// where the speaker boundary is placed — the cut time comes from the audio. When
// the API exposes words[] this becomes exact.
func splitText(text string, frac float64) (string, string) {
	w := strings.Fields(text)
	if len(w) == 0 {
		return "", ""
	}
	n := int(math.Round(frac * float64(len(w))))
	if n < 1 {
		n = 1
	}
	if n > len(w)-1 {
		n = len(w) - 1
	}
	if len(w) == 1 {
		return w[0], ""
	}
	return strings.Join(w[:n], " "), strings.Join(w[n:], " ")
}

// centroids rebuilds each cluster's mean embedding from the segments currently
// assigned to it, weighted by duration — a 30-second turn describes a voice
// better than a 2-second one and should pull the centroid accordingly.
func centroids(segs []Segment, assigned []string) []Cluster {
	sum := map[string][]float64{}
	secs := map[string]float64{}
	count := map[string]int{}
	for i, s := range segs {
		id := assigned[i]
		if id == "" || len(s.Embedding) == 0 {
			continue
		}
		w := s.Duration()
		if sum[id] == nil {
			sum[id] = make([]float64, len(s.Embedding))
		}
		for k, v := range s.Embedding {
			sum[id][k] += float64(v) * w
		}
		secs[id] += w
		count[id]++
	}
	out := make([]Cluster, 0, len(sum))
	for id, v := range sum {
		c := make([]float32, len(v))
		for k := range v {
			c[k] = float32(v[k] / secs[id])
		}
		out = append(out, Cluster{UUID: id, Centroid: c, Seconds: secs[id], Count: count[id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seconds > out[j].Seconds })
	return out
}

// confidence maps a cosine margin onto [0,1). Deliberately gentle: a 0.1 margin
// is real evidence but not certainty, and the point of reporting a number is to
// let a reviewer triage, not to justify writing without one.
func confidence(margin float64) float64 {
	if margin <= 0 {
		return 0
	}
	return 1 - math.Exp(-margin*8)
}

// Cosine is the similarity two voiceprints are compared by.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func round(f float64) float64 { return math.Round(f*10000) / 10000 }

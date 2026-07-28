package verify

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

// Scoring a diarization result against what a person actually heard.
//
// Two numbers, not one, because a single DER hides which of two unrelated
// failures produced it. On three hearings from the same corpus, strict DER came
// out at 55.8%, 55.0% and 27.9% — and the two that matched did so for opposite
// reasons. One separated its voices well and was wrecked by over-splitting; the
// other over-split less but failed to separate at all, with a single cluster
// holding 1125 seconds spread near-evenly across four people. Reported as one
// figure, those look like the same result and call for opposite fixes.
//
// STRICT is the DER convention: one hypothesis cluster per truth speaker, so
// every extra cluster is error — which is what over-splitting IS. MERGED assigns
// each cluster to whichever speaker it overlaps most, ignoring over-splitting
// entirely, and answers a different question: did the embeddings SEPARATE these
// voices, whatever the clustering called them. The gap between the two is the
// cost of over-splitting, in points.

// ScoreResult is one scoring pass.
type ScoreResult struct {
	TruthSeconds float64
	TruthTurns   int
	TruthSpeakers int
	HypTurns     int
	HypClusters  int

	StrictDER float64
	MergedDER float64
	Confusion float64
	Missed    float64

	// Coverage is the share of AUDIO under each review state. Reported because a
	// DER is only as good as the truth behind it, and these are not
	// interchangeable: `confirmed` is a per-turn ruling, `affirmed` is a blanket
	// acceptance of the rest. Collapsing them answers "was the pass finished"
	// while destroying the answer to "was THIS turn looked at".
	ConfirmedPct float64
	AffirmedPct  float64
	UnclearPct   float64
	UntouchedPct float64
	Corrected    int
	AffirmedBy   string
	AffirmedAt   string
}

type interval struct {
	start, end float64
	who        string
}

// Score compares a hypothesis transcription against a truth file.
func Score(truthPath, hypPath string) (*ScoreResult, map[string]string, error) {
	tb, err := os.ReadFile(truthPath)
	if err != nil {
		return nil, nil, err
	}
	var tf TruthFile
	if err := json.Unmarshal(tb, &tf); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", truthPath, err)
	}
	hb, err := os.ReadFile(hypPath)
	if err != nil {
		return nil, nil, err
	}
	var hyp struct {
		Segments []Segment `json:"segments"`
	}
	if err := json.Unmarshal(hb, &hyp); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", hypPath, err)
	}
	if len(tf.Segments) == 0 || len(hyp.Segments) == 0 {
		return nil, nil, fmt.Errorf("need segments in both files")
	}

	T := toIntervals(tf.Segments)
	H := toIntervals(hyp.Segments)
	tspk, hspk := speakersOf(T), speakersOf(H)

	agree := map[[2]string]float64{}
	for _, h := range H {
		for _, t := range T {
			if o := overlap(h, t); o > 0 {
				agree[[2]string{h.who, t.who}] += o
			}
		}
	}

	res := &ScoreResult{
		TruthTurns: len(tf.Segments), TruthSpeakers: len(tspk),
		HypTurns: len(hyp.Segments), HypClusters: len(hspk),
		AffirmedBy: tf.AffirmedBy, AffirmedAt: tf.AffirmedAt,
	}
	for _, t := range T {
		res.TruthSeconds += t.end - t.start
	}
	for _, s := range tf.Segments {
		d := s.End - s.Start
		switch {
		case s.Unclear:
			res.UnclearPct += d
		case s.Confirmed:
			res.ConfirmedPct += d
		case s.Affirmed:
			res.AffirmedPct += d
		default:
			res.UntouchedPct += d
		}
		if s.Corrected {
			res.Corrected++
		}
	}
	if res.TruthSeconds > 0 {
		for _, p := range []*float64{&res.ConfirmedPct, &res.AffirmedPct, &res.UnclearPct, &res.UntouchedPct} {
			*p = 100 * *p / res.TruthSeconds
		}
	}

	strict := bestInjective(tspk, hspk, agree)
	merged := map[string]string{}
	for _, h := range hspk {
		best, at := -1.0, ""
		for _, t := range tspk {
			if v := agree[[2]string{h, t}]; v > best {
				best, at = v, t
			}
		}
		merged[h] = at
	}
	res.StrictDER, res.Confusion, res.Missed = der(T, H, strict)
	res.MergedDER, _, _ = der(T, H, merged)
	return res, strict, nil
}

func toIntervals(ss []Segment) []interval {
	out := make([]interval, 0, len(ss))
	for _, s := range ss {
		if s.End > s.Start && s.Speaker != "" {
			out = append(out, interval{s.Start, s.End, s.Speaker})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

func speakersOf(iv []interval) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range iv {
		if !seen[x.who] {
			seen[x.who] = true
			out = append(out, x.who)
		}
	}
	sort.Strings(out)
	return out
}

func overlap(a, b interval) float64 {
	return math.Max(0, math.Min(a.end, b.end)-math.Max(a.start, b.start))
}

// bestInjective picks one hypothesis cluster per truth speaker, maximising total
// agreement. Exhaustive: the counts here are single digits, and a greedy pass
// gets this wrong exactly when clusters are mixed — which is the case worth
// measuring.
func bestInjective(tspk, hspk []string, agree map[[2]string]float64) map[string]string {
	best, bestMap := -1.0, map[string]string{}
	var walk func(i int, used map[string]bool, cur map[string]string, sum float64)
	walk = func(i int, used map[string]bool, cur map[string]string, sum float64) {
		if i == len(tspk) {
			if sum > best {
				best = sum
				bestMap = map[string]string{}
				for k, v := range cur {
					bestMap[k] = v
				}
			}
			return
		}
		for _, h := range hspk {
			if used[h] {
				continue
			}
			used[h] = true
			cur[h] = tspk[i]
			walk(i+1, used, cur, sum+agree[[2]string{h, tspk[i]}])
			delete(cur, h)
			used[h] = false
		}
	}
	walk(0, map[string]bool{}, map[string]string{}, 0)
	return bestMap
}

// der walks the truth timeline at a fixed step. Frame-level rather than
// segment-level because the two segmentations do not share boundaries once a
// person has joined and split turns.
const derStep = 0.01

func der(T, H []interval, mapping map[string]string) (rate, confusion, missed float64) {
	at := func(iv []interval, t float64) string {
		for _, x := range iv {
			if x.start <= t && t < x.end {
				return x.who
			}
		}
		return ""
	}
	var total, ok float64
	end := 0.0
	for _, x := range T {
		if x.end > end {
			end = x.end
		}
	}
	for t := 0.0; t < end; t += derStep {
		tu := at(T, t)
		if tu == "" {
			continue
		}
		total += derStep
		switch hu := at(H, t); {
		case hu == "":
			missed += derStep
		case mapping[hu] == tu:
			ok += derStep
		default:
			confusion += derStep
		}
	}
	if total == 0 {
		return 0, 0, 0
	}
	return 100 * (confusion + missed) / total, confusion, missed
}

// Report writes the human-readable scoring summary.
func Report(w io.Writer, r *ScoreResult, names map[string]string) {
	fmt.Fprintf(w, "GROUND TRUTH  %.1fs, %d turns, %d speakers\n", r.TruthSeconds, r.TruthTurns, r.TruthSpeakers)
	fmt.Fprintf(w, "HYPOTHESIS    %d turns, %d clusters\n", r.HypTurns, r.HypClusters)
	fmt.Fprintf(w, "REVIEW        %.0f%% confirmed · %.0f%% affirmed · %.0f%% unclear · %.0f%% untouched\n",
		r.ConfirmedPct, r.AffirmedPct, r.UnclearPct, r.UntouchedPct)
	if r.AffirmedBy != "" || r.AffirmedAt != "" {
		at := r.AffirmedAt
		if len(at) > 10 {
			at = at[:10]
		}
		fmt.Fprintf(w, "              affirmed by %s%s\n", r.AffirmedBy, map[bool]string{true: " on " + at, false: ""}[at != ""])
	}
	if r.UntouchedPct > 0.5 {
		fmt.Fprintf(w, "              ⚠ %.0f%% of audio carries no ruling — scored below as if it were truth\n", r.UntouchedPct)
	}
	if r.Corrected == 0 {
		fmt.Fprintf(w, "WER           unmeasurable — no turn's text has been corrected\n")
	} else {
		fmt.Fprintf(w, "WER           %d turns corrected (only these are ground truth for wording)\n", r.Corrected)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "STRICT  DER %5.1f%%   one cluster per speaker — over-splitting counts as error\n", r.StrictDER)
	fmt.Fprintf(w, "MERGED  DER %5.1f%%   each cluster to its best speaker — separation only\n", r.MergedDER)
	fmt.Fprintf(w, "\ncost of over-splitting: %.1f points\n", r.StrictDER-r.MergedDER)
}

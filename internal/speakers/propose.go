package speakers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Content-originated proposals: the reviewer finds errors itself, and the
// embeddings are then asked whether they support them.
//
// The inverse of the judging path, and it exists because the judging path can
// only ever rule on a list the acoustic pass wrote. Anything the embeddings
// never flag is invisible to review — errors of OMISSION are systematic, not
// occasional, since one signal decides what the other is allowed to consider.
//
// Content is the better proposer for some of this. It knows a courtroom has one
// person who administers oaths and rules on objections and another who describes
// their own sewer; embeddings know only that two spans sound alike. On the split
// proposals it rejected every one the acoustic pass was confident about, and it
// was right each time.
//
// So both directions run, and every proposal carries BOTH signals regardless of
// which produced it: a content proposal with no acoustic support is as suspect
// as an acoustic proposal the reviewer rejects.

// maxContextSegments bounds the chronological transcript sent for proposing.
// A long hearing otherwise fills the window with the middle of testimony, which
// is where the least attribution ambiguity lives.
const maxContextSegments = 400

// Propose asks the reviewer to find misattributions on its own, then fills in
// the acoustic evidence for whatever it returns.
//
// Proposals naming an unknown segment or speaker id are dropped rather than
// repaired: a reviewer that invented an id did not understand the transcript,
// and guessing what it meant would launder that into a change.
func (r *Reviewer) Propose(ctx context.Context, segs []Segment, cents []Cluster, p Params) (Analysis, error) {
	var a Analysis
	prompt := proposePrompt(segs)
	raw, err := r.complete(ctx, prompt)
	if r.Dump != nil {
		dumpMu.Lock()
		fmt.Fprintf(r.Dump, "===== PROPOSE REQUEST =====\n%s\n===== PROPOSE RESPONSE =====\n%s\n\n", prompt, raw)
		dumpMu.Unlock()
	}
	if err != nil {
		return a, err
	}
	var out struct {
		Moves []struct {
			Segment int     `json:"segment"`
			To      string  `json:"to"`
			Conf    float64 `json:"confidence"`
			Reason  string  `json:"reason"`
		} `json:"moves"`
		Joins []struct {
			A      string  `json:"a"`
			B      string  `json:"b"`
			Conf   float64 `json:"confidence"`
			Reason string  `json:"reason"`
		} `json:"joins"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return a, fmt.Errorf("proposer returned unparseable JSON: %w\n%s", err, truncate(raw, 300))
	}

	bySeg := map[int]Segment{}
	for _, s := range segs {
		bySeg[s.Index] = s
	}
	known := map[string]bool{}
	for _, c := range cents {
		known[c.UUID] = true
	}

	for _, m := range out.Moves {
		s, ok := bySeg[m.Segment]
		if !ok || !known[m.To] || m.To == s.Speaker {
			continue
		}
		mv := Move{
			Segment: s.Index, Start: s.Start, End: s.End, Text: s.Text,
			From: s.Speaker, To: m.To, Origin: OriginContent,
			Semantic: &Verdict{Status: "agree", Confidence: m.Conf, Reason: m.Reason},
		}
		// The acoustic side now answers the same question it would have been asked
		// had it proposed this itself.
		if len(s.Embedding) > 0 {
			toCos := Cosine(s.Embedding, centroidOf(m.To, cents))
			fromCos := Cosine(s.Embedding, centroidOf(s.Speaker, cents))
			mv.ToCos, mv.FromCos = round(toCos), round(fromCos)
			mv.Margin = round(toCos - fromCos)
			mv.Acoustic = round(confidence(toCos - fromCos))
		}
		a.Moves = append(a.Moves, mv)
	}
	for _, j := range out.Joins {
		if !known[j.A] || !known[j.B] || j.A == j.B {
			continue
		}
		c := Cosine(centroidOf(j.A, cents), centroidOf(j.B, cents))
		a.Joins = append(a.Joins, Join{
			A: j.A, B: j.B, Cos: round(c), Acoustic: round(confidence(c - p.JoinCos)),
			Origin:   OriginContent,
			Semantic: &Verdict{Status: "agree", Confidence: j.Conf, Reason: j.Reason},
		})
	}
	sort.Slice(a.Moves, func(i, j int) bool { return a.Moves[i].Margin > a.Moves[j].Margin })
	sort.Slice(a.Joins, func(i, j int) bool { return a.Joins[i].Cos > a.Joins[j].Cos })
	return a, nil
}

// proposePrompt sends the transcript in TIME order rather than grouped by
// speaker. Grouping is right for judging one proposal — it establishes each id's
// role — but misattribution shows up as a line that does not belong in the
// conversation around it, and that is only visible chronologically.
func proposePrompt(segs []Segment) string {
	ordered := append([]Segment(nil), segs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	if len(ordered) > maxContextSegments {
		ordered = ordered[:maxContextSegments]
	}
	var b strings.Builder
	b.WriteString(`You are auditing an automatic speaker-diarization result. Each line below is a
passage of a recording, tagged with the speaker id the system assigned it.

The system assigns speakers by VOICE and makes two kinds of mistake:
  - one person split across several speaker ids (mic distance, volume, channel)
  - a passage attributed to the wrong speaker

Find them using ONLY what is said — role, topic, stance, who asks versus answers, who
has authority in the room, and whether a line fits the conversation around it. You
cannot hear the audio; do not speculate about it.

Propose only what the text actually supports. An empty list is a valid and useful
answer. Do not propose a move unless the passage clearly belongs to another speaker
already present, and give the id of that speaker exactly as written below.

Reply with JSON only:
{"moves":[{"segment":N,"to":"speaker-id","confidence":0.0,"reason":"one sentence"}],
 "joins":[{"a":"speaker-id","b":"speaker-id","confidence":0.0,"reason":"one sentence"}]}

TRANSCRIPT
`)
	for _, s := range ordered {
		fmt.Fprintf(&b, "#%d [%s] %.1f-%.1fs %q\n", s.Index, s.Speaker, s.Start, s.End,
			truncate(oneLine(s.Text), 200))
	}
	return b.String()
}

// Merge folds content-originated proposals into the acoustic analysis, dropping
// any that duplicate one already present. The acoustic version is kept on a
// collision because it carries the margin that produced it.
func (a *Analysis) Merge(b Analysis) {
	have := map[int]bool{}
	for _, m := range a.Moves {
		have[m.Segment] = true
	}
	for _, m := range b.Moves {
		if !have[m.Segment] {
			a.Moves = append(a.Moves, m)
		}
	}
	pair := func(x, y string) string {
		if x < y {
			return x + "|" + y
		}
		return y + "|" + x
	}
	seen := map[string]bool{}
	for _, j := range a.Joins {
		seen[pair(j.A, j.B)] = true
	}
	for _, j := range b.Joins {
		if !seen[pair(j.A, j.B)] {
			a.Joins = append(a.Joins, j)
		}
	}
}

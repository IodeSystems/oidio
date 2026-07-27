package speakers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The semantic reviewer: a second opinion drawn from WHAT was said rather than
// how it sounded.
//
// It exists because the two signals fail differently. Embeddings are fooled by
// mic distance, volume and channel — the same person at the bench and leaning
// into a table microphone can land far apart. Content is fooled by neither, but
// knows nothing about voices: it can only observe that whoever says "please
// raise your right hand" and "any questions?" is running the hearing, and is
// therefore unlikely to also be the person describing their own sewer.
//
// So the two are kept SEPARATE and reported separately. A proposal both agree on
// is worth acting on; one they split on is exactly what a human should look at.
// Collapsing them into a single blended score would hide that disagreement,
// which is the most informative thing the review produces.
//
// oidio does not host an LLM. This speaks OpenAI-compatible HTTP to whatever the
// caller points it at — corrallm being the obvious local choice — so the tooling
// gains a semantic path without oidio gaining a model.

// Reviewer asks an OpenAI-compatible endpoint to judge proposals.
type Reviewer struct {
	Endpoint string // e.g. http://127.0.0.1:8111/v1
	Model    string
	APIKey   string
	Client   *http.Client

	// Dump, when set, receives the exact prompt sent and the exact reply
	// received. The reviewer's judgement is only as good as what it was shown,
	// and that is otherwise invisible — a verdict that looks wrong is usually a
	// prompt that was wrong.
	Dump io.Writer
}

func NewReviewer(endpoint, model, key string) *Reviewer {
	return &Reviewer{
		Endpoint: strings.TrimSuffix(endpoint, "/"),
		Model:    model,
		APIKey:   key,
		Client:   &http.Client{Timeout: 180 * time.Second},
	}
}

// Review fills in the Semantic field on each proposal. A reviewer error is
// returned rather than swallowed: a review that silently ran without its second
// opinion looks identical to one that had it and agreed.
func (r *Reviewer) Review(ctx context.Context, a *Analysis, segs []Segment) error {
	if len(a.Moves) == 0 && len(a.Joins) == 0 && len(a.Splits) == 0 {
		return nil
	}
	prompt := buildPrompt(a, segs)
	if r.Dump != nil {
		fmt.Fprintf(r.Dump, "===== REQUEST =====\nPOST %s/chat/completions\nmodel: %s\ntemperature: 0\n\n%s\n", r.Endpoint, r.Model, prompt)
	}
	raw, err := r.complete(ctx, prompt)
	if r.Dump != nil {
		fmt.Fprintf(r.Dump, "\n===== RESPONSE =====\n%s\n", raw)
		if err != nil {
			fmt.Fprintf(r.Dump, "\n===== ERROR =====\n%v\n", err)
		}
	}
	if err != nil {
		return err
	}
	var out struct {
		Moves []struct {
			Segment    int     `json:"segment"`
			Status     string  `json:"status"`
			Confidence float64 `json:"confidence"`
			Reason     string  `json:"reason"`
		} `json:"moves"`
		Joins []struct {
			A          string  `json:"a"`
			BB         string  `json:"b"`
			Status     string  `json:"status"`
			Confidence float64 `json:"confidence"`
			Reason     string  `json:"reason"`
		} `json:"joins"`
		Splits []struct {
			Segment    int     `json:"segment"`
			Status     string  `json:"status"`
			Confidence float64 `json:"confidence"`
			Reason     string  `json:"reason"`
		} `json:"splits"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return fmt.Errorf("reviewer returned unparseable JSON: %w\n%s", err, truncate(raw, 400))
	}
	bySeg := map[int]*Verdict{}
	for _, m := range out.Moves {
		bySeg[m.Segment] = &Verdict{Status: normStatus(m.Status), Confidence: m.Confidence, Reason: m.Reason}
	}
	for i := range a.Moves {
		if v, ok := bySeg[a.Moves[i].Segment]; ok {
			a.Moves[i].Semantic = v
		}
	}
	for i := range a.Joins {
		for _, j := range out.Joins {
			if (j.A == a.Joins[i].A && j.BB == a.Joins[i].B) || (j.A == a.Joins[i].B && j.BB == a.Joins[i].A) {
				a.Joins[i].Semantic = &Verdict{Status: normStatus(j.Status), Confidence: j.Confidence, Reason: j.Reason}
			}
		}
	}
	bySplit := map[int]*Verdict{}
	for _, sp := range out.Splits {
		bySplit[sp.Segment] = &Verdict{Status: normStatus(sp.Status), Confidence: sp.Confidence, Reason: sp.Reason}
	}
	for i := range a.Splits {
		if v, ok := bySplit[a.Splits[i].Segment]; ok {
			a.Splits[i].Semantic = v
		}
	}
	return nil
}

// samplesPerCluster bounds how much of each speaker's text the reviewer sees.
// Enough to establish a role; not so much that a long hearing overflows context.
const samplesPerCluster = 12

func buildPrompt(a *Analysis, segs []Segment) string {
	byUUID := map[string][]Segment{}
	for _, s := range segs {
		byUUID[s.Speaker] = append(byUUID[s.Speaker], s)
	}
	var b strings.Builder
	b.WriteString(`You are reviewing an automatic speaker-diarization result for a recording.
Diarization assigns each passage to a speaker by VOICE. It makes two kinds of mistake:
one person split across several speaker ids, and one speaker id holding passages
actually spoken by different people.

Judge ONLY from what is said — role, topic, stance, who is asking versus answering,
who has authority in the room. You cannot hear the audio; do not speculate about it.

Note that a speaker id is NOT a person: the same person is often split across
several ids. Do not reject a move merely because the two ids play the same role —
if both ids appear to be the same person, that supports the move, and you should
say so.

For each proposal give a status, a confidence in [0,1], and a one-sentence reason:
  "agree"        — the content supports the proposal
  "disagree"     — the content actively contradicts it
  "insufficient" — too little content to tell either way (NOT a rejection)

A SPLIT claims one passage contains two different speakers and should be cut in
two. Judge it on whether the two halves read as different people talking — a
question and its answer, an instruction and a reply — rather than one continuous
utterance. A cut through the middle of one sentence is wrong even if both halves
are plausible.

Reply with JSON only:
{"moves":[{"segment":N,"status":"agree|disagree|insufficient","confidence":0.0,"reason":"..."}],
 "joins":[{"a":"id","b":"id","status":"agree|disagree|insufficient","confidence":0.0,"reason":"..."}],
 "splits":[{"segment":N,"status":"agree|disagree|insufficient","confidence":0.0,"reason":"..."}]}

SPEAKERS AND WHAT THEY SAY
`)
	ids := make([]string, 0, len(byUUID))
	for id := range byUUID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ss := byUUID[id]
		total := 0.0
		for _, s := range ss {
			total += s.Duration()
		}
		fmt.Fprintf(&b, "\n[%s] %.0fs across %d passages:\n", id, total, len(ss))
		sort.Slice(ss, func(i, j int) bool { return len(ss[i].Text) > len(ss[j].Text) })
		for i, s := range ss {
			if i >= samplesPerCluster {
				break
			}
			fmt.Fprintf(&b, "  - %q\n", truncate(oneLine(s.Text), 160))
		}
	}
	if len(a.Moves) > 0 {
		b.WriteString("\nPROPOSED MOVES (this passage may belong to a different speaker)\n")
		for _, m := range a.Moves {
			fmt.Fprintf(&b, "  segment %d: currently [%s], proposed [%s]\n    %q\n",
				m.Segment, m.From, m.To, truncate(oneLine(m.Text), 200))
		}
	}
	if len(a.Joins) > 0 {
		b.WriteString("\nPROPOSED JOINS (these two ids may be the same person)\n")
		for _, j := range a.Joins {
			fmt.Fprintf(&b, "  %s + %s\n", j.A, j.B)
		}
	}
	if len(a.Splits) > 0 {
		b.WriteString("\nPROPOSED SPLITS (this passage may contain two speakers)\n")
		for _, sp := range a.Splits {
			fmt.Fprintf(&b, "  segment %d, cut into:\n    A [%s] %q\n    B [%s] %q\n",
				sp.Segment, sp.Left, truncate(oneLine(sp.LeftText), 160),
				sp.Right, truncate(oneLine(sp.RightText), 160))
		}
	}
	return b.String()
}

func (r *Reviewer) complete(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       r.Model,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"temperature": 0,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", r.Endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reviewer unreachable at %s: %w", r.Endpoint, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("reviewer HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("reviewer returned no choices: %s", truncate(string(raw), 300))
	}
	return out.Choices[0].Message.Content, nil
}

// extractJSON pulls the object out of a reply that may be fenced or prefaced.
// Models wrap JSON in prose often enough that failing on it would make the
// semantic path unreliable for reasons unrelated to its judgement.
func extractJSON(s string) string {
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			s = rest[:j]
		}
	}
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// normStatus keeps an unexpected label from silently becoming agreement. A model
// that answers "yes" or "true" means agree; anything unrecognised is treated as
// insufficient, which leaves the acoustic finding standing rather than either
// endorsing or rejecting it on a parse artefact.
func normStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "agree", "agrees", "yes", "true", "support", "supports":
		return "agree"
	case "disagree", "disagrees", "no", "false", "contradict", "contradicts":
		return "disagree"
	default:
		return "insufficient"
	}
}

package speakers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Per-proposal review.
//
// Batching every proposal into one request made a verdict depend on its
// neighbours: adding four unrelated split proposals flipped six move verdicts
// from agree to disagree, same model, same moves, temperature 0. A judgement
// that moves because of something else in the batch is not a judgement about the
// proposal, and it cannot be used to gate a write.
//
// So each proposal gets its own request, carrying the same cluster context and
// exactly one thing to rule on. The cost is one call per proposal; they are
// independent, so they run concurrently.

// reviewConcurrency bounds in-flight reviewer calls. High enough to hide latency
// on ~40 proposals, low enough not to bury a single-GPU endpoint that is also
// serving the transcription models.
const reviewConcurrency = 6

// ReviewEach judges every proposal in its own request.
func (r *Reviewer) ReviewEach(ctx context.Context, a *Analysis, segs []Segment) error {
	ctxBlock := clusterContext(segs)

	type job struct {
		kind  string
		idx   int
		ask   string
		apply func(*Verdict)
	}
	var jobs []job
	for i := range a.Moves {
		m := a.Moves[i]
		jobs = append(jobs, job{"move", i, describeMove(m), func(v *Verdict) { a.Moves[i].Semantic = v }})
	}
	for i := range a.Joins {
		j := a.Joins[i]
		jobs = append(jobs, job{"join", i, describeJoin(j), func(v *Verdict) { a.Joins[i].Semantic = v }})
	}
	for i := range a.Splits {
		s := a.Splits[i]
		jobs = append(jobs, job{"split", i, describeSplit(s), func(v *Verdict) { a.Splits[i].Semantic = v }})
	}
	if len(jobs) == 0 {
		return nil
	}

	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, reviewConcurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			v, err := r.judgeOne(ctx, ctxBlock, j.ask)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			j.apply(v)
		}(j)
	}
	wg.Wait()
	return firstErr
}

// judgeOne asks about exactly one proposal.
func (r *Reviewer) judgeOne(ctx context.Context, ctxBlock, ask string) (*Verdict, error) {
	prompt := `You are reviewing ONE proposed correction to an automatic speaker-diarization result.
Diarization assigns each passage to a speaker by VOICE. It makes three kinds of mistake:
one person split across several speaker ids, one speaker id holding passages spoken by
different people, and one passage that actually contains two speakers.

Judge ONLY from what is said — role, topic, stance, who asks versus answers, who has
authority in the room. You cannot hear the audio; do not speculate about it.

A speaker id is NOT a person: the same person is often split across several ids. Do not
reject a proposal merely because two ids play the same role — if they look like the same
person, that SUPPORTS joining or moving between them.

Answer with one of:
  "agree"        — the content supports this
  "disagree"     — the content actively contradicts it
  "insufficient" — too little content to tell either way (this is NOT a rejection)

Reply with JSON only: {"status":"...","confidence":0.0,"reason":"one sentence"}

` + ctxBlock + "\n\nTHE PROPOSAL TO JUDGE\n" + ask + "\n"

	raw, err := r.complete(ctx, prompt)
	if r.Dump != nil {
		mu := &dumpMu
		mu.Lock()
		fmt.Fprintf(r.Dump, "===== REQUEST =====\n%s\n===== RESPONSE =====\n%s\n\n", prompt, raw)
		mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	var out struct {
		Status     string  `json:"status"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("reviewer returned unparseable JSON: %w\n%s", err, truncate(raw, 300))
	}
	return &Verdict{Status: normStatus(out.Status), Confidence: out.Confidence, Reason: out.Reason}, nil
}

// dumpMu serialises the exchange dump; the per-proposal calls run concurrently
// and would otherwise interleave into an unreadable file.
var dumpMu sync.Mutex

func describeMove(m Move) string {
	return fmt.Sprintf("MOVE: this passage is currently attributed to [%s], and is proposed to belong to [%s] instead.\n  %q",
		m.From, m.To, truncate(oneLine(m.Text), 300))
}

func describeJoin(j Join) string {
	return fmt.Sprintf("JOIN: speaker ids [%s] and [%s] are proposed to be the SAME person.", j.A, j.B)
}

func describeSplit(s Split) string {
	return fmt.Sprintf(`SPLIT: this one passage is proposed to contain TWO different speakers and be cut in two.
Judge whether the two halves read as different people — a question and its answer, an
instruction and a reply — rather than one continuous utterance. A cut through the middle
of a single sentence is wrong even when both halves are plausible.
  first half  [%s]: %q
  second half [%s]: %q`, s.Left, truncate(oneLine(s.LeftText), 250), s.Right, truncate(oneLine(s.RightText), 250))
}

// clusterContext is the shared evidence block: who says what, per speaker id.
func clusterContext(segs []Segment) string {
	byUUID := map[string][]Segment{}
	for _, s := range segs {
		byUUID[s.Speaker] = append(byUUID[s.Speaker], s)
	}
	ids := make([]string, 0, len(byUUID))
	for id := range byUUID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	b.WriteString("SPEAKERS AND WHAT THEY SAY\n")
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
	return b.String()
}

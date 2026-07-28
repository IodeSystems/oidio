package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Rendering a truth file as prose, so the transcript can be read and cited.
//
// The header is GENERATED, never written. These documents were hand-written
// once, which meant the header said whatever someone believed at the time — and
// on the corpus this was built against, that produced a header claiming a
// completeness the data did not carry. A generated header cannot drift: it says
// what the truth file says.
//
// That matters more than presentation. Downstream, a transcript is a source
// document, and facts get extracted from it and cited. A file that is 18-of-39
// confirmed, presented as "the verified transcript", launders an unchecked
// machine guess into the record one step removed. The banner is what keeps
// citing it legitimate, so it is load-bearing rather than decoration.

// Render turns a truth file into markdown. `title` and `source` describe the
// recording; both may be empty, in which case the stem and the truth file's own
// `source` are used.
// note, when non-empty, is appended to the header block. oidio ships a warning
// strong enough for any transcript, but it cannot know what a given corpus is
// FOR — "before it goes into a declaration, a brief, or a deposition question"
// is the right sentence for a legal archive and the wrong one everywhere else.
// The domain supplies its own rather than oidio guessing at one.
func Render(truthPath, title, source, note string) (string, error) {
	b, err := os.ReadFile(truthPath)
	if err != nil {
		return "", err
	}
	var tf TruthFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return "", fmt.Errorf("%s: %w", truthPath, err)
	}
	if len(tf.Segments) == 0 {
		return "", fmt.Errorf("%s: no segments", truthPath)
	}

	stem := strings.TrimSuffix(truthPath, ".truth.json")
	labels := map[string]string{}
	for k, v := range tf.Speakers {
		labels[k] = v
	}
	// A sibling catalog fills gaps but never overrides: the truth file was
	// written by the pass that produced these turns, so its names are the ones
	// that were actually applied to them.
	if sb, err := os.ReadFile(stem + ".speakers.json"); err == nil {
		var extra map[string]string
		if json.Unmarshal(sb, &extra) == nil {
			for k, v := range extra {
				if _, ok := labels[k]; !ok {
					labels[k] = v
				}
			}
		}
	}
	name := func(u string) string {
		if l, ok := labels[u]; ok && l != "" {
			return l
		}
		if u == "" {
			return "?"
		}
		return u
	}

	// Unclear takes precedence: a turn nobody could attribute is not "confirmed"
	// whatever else is set on it.
	var ruled, affirmed, unclear, corrected int
	talk := map[string]float64{}
	for _, s := range tf.Segments {
		switch {
		case s.Unclear:
			unclear++
		case s.Confirmed:
			ruled++
		case s.Affirmed:
			affirmed++
		}
		if s.Corrected {
			corrected++
		}
		if d := s.End - s.Start; d > 0 {
			talk[name(s.Speaker)] += d
		}
	}
	total := len(tf.Segments)
	untouched := total - ruled - affirmed - unclear

	who := make([]string, 0, len(talk))
	for w := range talk {
		who = append(who, w)
	}
	sort.Slice(who, func(i, j int) bool { return talk[who[i]] > talk[who[j]] })
	roster := make([]string, 0, len(who))
	for _, w := range who {
		roster = append(roster, fmt.Sprintf("%s (%ds)", w, int(talk[w])))
	}

	if title == "" {
		title = "Transcript (verified) — " + filepath.Base(stem)
	}
	if source == "" {
		source = tf.Source
	}
	if source == "" {
		source = "—"
	}

	var o []string
	add := func(f string, a ...any) { o = append(o, fmt.Sprintf(f, a...)) }
	add("# %s", title)
	add("")
	add("**Source:** `%s`  ", source)
	add("**Speakers:** %s  ", strings.Join(roster, " · "))
	add("")
	add("> *Generated from the truth file by `oidio verify render`. Do not hand-edit the header —")
	add("> it is a reading of the data and will be overwritten.*")
	add(">")

	if untouched == 0 && total > 0 {
		parts := []string{fmt.Sprintf("**%d ruled on individually**", ruled)}
		if affirmed > 0 {
			parts = append(parts, fmt.Sprintf("**%d affirmed in bulk**", affirmed))
		}
		if unclear > 0 {
			parts = append(parts, fmt.Sprintf("%d marked unclear", unclear))
		}
		add("> ✅ **Speaker attribution is complete** — %s, of %d turns.", strings.Join(parts, ", "), total)
		if affirmed > 0 {
			by, at := tf.AffirmedBy, tf.AffirmedAt
			if len(at) > 10 {
				at = at[:10]
			}
			extra := ""
			if by != "" {
				extra += " by **" + by + "**"
			}
			if at != "" {
				extra += " on " + at
			}
			add(">")
			add("> **Affirmed** means accepted as part of a blanket *\"the rest is right\"*%s, rather than"+
				" judged turn by turn. It is recorded separately so the question *was this particular turn"+
				" looked at* still has an honest answer.", extra)
		}
	} else {
		add("> ⚠ **Speaker attribution is INCOMPLETE** — %d ruled on, %d affirmed, %d unclear,"+
			" **%d of %d turns untouched.** Attribution on the untouched turns is the diarizer's guess"+
			" and has not been checked.", ruled, affirmed, unclear, untouched, total)
	}

	// Wording is a SEPARATE axis, and the one people conflate with the above.
	// Checking who spoke says nothing about whether the words are right.
	add(">")
	if corrected == 0 {
		add("> ⚠ **The WORDS are unverified.** Attribution was checked; the text was not. No turn in this" +
			" file has had its wording corrected. This is machine transcription — **verify any quotation" +
			" against the audio before relying on its exact wording.**")
	} else {
		add("> ⚠ **The WORDS are only partly verified** — %d of %d turns have had their text corrected by"+
			" a person. The rest is machine transcription; verify a quotation against the audio before"+
			" relying on its exact wording.", corrected, total)
	}

	if note != "" {
		add(">")
		add("> %s", note)
	}

	add("")
	add("---")
	add("")

	// One block per speaker RUN. The segmentation is an artifact of how the audio
	// was cut, and a reader does not care where those cuts fell.
	last := ""
	for _, s := range tf.Segments {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		w := name(s.Speaker)
		if w != last {
			add("**%s** *(%s)*", w, mmss(s.Start))
			add("")
			last = w
		}
		if s.Unclear {
			text = "[unclear] " + text
		}
		add("%s", text)
		add("")
	}
	return strings.TrimRight(strings.Join(o, "\n"), "\n") + "\n", nil
}

func mmss(t float64) string {
	s := int(t)
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

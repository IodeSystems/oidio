package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The two numbers must separate the two failures. A hypothesis that splits ONE
// speaker across three clusters has perfect separation and terrible strict DER —
// reported as a single figure it is indistinguishable from a hypothesis that
// mixed two people together, and the fixes are opposite.
func TestOverSplittingShowsInStrictButNotMerged(t *testing.T) {
	dir := t.TempDir()
	truth := filepath.Join(dir, "h.truth.json")
	hyp := filepath.Join(dir, "h.json")
	writeJSONFile(t, truth, TruthFile{Segments: []Segment{
		{Start: 0, End: 30, Speaker: "alice", Confirmed: true},
		{Start: 30, End: 60, Speaker: "bob", Confirmed: true},
	}})
	// alice is split into three clusters; nobody is confused with anybody.
	writeJSONFile(t, hyp, map[string]any{"segments": []Segment{
		{Start: 0, End: 10, Speaker: "c1"},
		{Start: 10, End: 20, Speaker: "c2"},
		{Start: 20, End: 30, Speaker: "c3"},
		{Start: 30, End: 60, Speaker: "c4"},
	}})
	r, _, err := Score(truth, hyp)
	if err != nil {
		t.Fatal(err)
	}
	if r.MergedDER > 1 {
		t.Fatalf("separation is perfect; merged DER should be ~0, got %.1f", r.MergedDER)
	}
	if r.StrictDER < 30 {
		t.Fatalf("two thirds of alice sits in unmapped clusters; strict DER should be high, got %.1f", r.StrictDER)
	}
}

// The mirror case: no over-splitting at all, but a cluster holds two people.
// Strict and merged should BOTH be bad, because merging cannot rescue a cluster
// that contains two voices.
func TestContaminationShowsInBoth(t *testing.T) {
	dir := t.TempDir()
	truth := filepath.Join(dir, "h.truth.json")
	hyp := filepath.Join(dir, "h.json")
	writeJSONFile(t, truth, TruthFile{Segments: []Segment{
		{Start: 0, End: 30, Speaker: "alice", Confirmed: true},
		{Start: 30, End: 60, Speaker: "bob", Confirmed: true},
	}})
	writeJSONFile(t, hyp, map[string]any{"segments": []Segment{
		{Start: 0, End: 60, Speaker: "c1"}, // one cluster, both people
	}})
	r, _, err := Score(truth, hyp)
	if err != nil {
		t.Fatal(err)
	}
	if r.MergedDER < 40 {
		t.Fatalf("a cluster holding two speakers cannot be rescued by merging; got %.1f", r.MergedDER)
	}
}

// Review coverage is reported as four separate shares. Summing them would answer
// "was the pass finished" while destroying the answer to "was THIS turn looked
// at", and the DER is only as trustworthy as the second question.
func TestCoverageStatesAreReportedSeparately(t *testing.T) {
	dir := t.TempDir()
	truth := filepath.Join(dir, "h.truth.json")
	hyp := filepath.Join(dir, "h.json")
	writeJSONFile(t, truth, TruthFile{
		AffirmedBy: "someone", AffirmedAt: "2026-07-27T00:00:00Z",
		Segments: []Segment{
			{Start: 0, End: 25, Speaker: "alice", Confirmed: true},
			{Start: 25, End: 50, Speaker: "alice", Affirmed: true},
			{Start: 50, End: 75, Speaker: "bob", Unclear: true},
			{Start: 75, End: 100, Speaker: "bob"},
		}})
	writeJSONFile(t, hyp, map[string]any{"segments": []Segment{
		{Start: 0, End: 50, Speaker: "c1"}, {Start: 50, End: 100, Speaker: "c2"},
	}})
	r, _, err := Score(truth, hyp)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]float64{
		"confirmed": r.ConfirmedPct, "affirmed": r.AffirmedPct,
		"unclear": r.UnclearPct, "untouched": r.UntouchedPct,
	} {
		if got < 24 || got > 26 {
			t.Fatalf("%s should be ~25%% of audio, got %.1f", name, got)
		}
	}
	if r.AffirmedBy != "someone" {
		t.Fatalf("file-level affirmation provenance lost: %+v", r.AffirmedBy)
	}
}

// WER is only measurable against text a person retyped. Reporting it from
// uncorrected turns would compare the recogniser to its own output.
func TestWerIsUnmeasurableWithoutCorrections(t *testing.T) {
	dir := t.TempDir()
	truth := filepath.Join(dir, "h.truth.json")
	hyp := filepath.Join(dir, "h.json")
	writeJSONFile(t, truth, TruthFile{Segments: []Segment{{Start: 0, End: 10, Speaker: "a", Confirmed: true}}})
	writeJSONFile(t, hyp, map[string]any{"segments": []Segment{{Start: 0, End: 10, Speaker: "c1"}}})
	r, _, err := Score(truth, hyp)
	if err != nil {
		t.Fatal(err)
	}
	if r.Corrected != 0 {
		t.Fatalf("no turn was corrected; got %d", r.Corrected)
	}
}

package verify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"os/exec"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	trans := filepath.Join(dir, "h.json")
	audio := filepath.Join(dir, "a.mp4")
	speak := filepath.Join(dir, "speakers.json")
	truth := filepath.Join(dir, "h.truth.json")
	if err := os.WriteFile(trans, []byte(`{"segments":[
		{"id":0,"start":0,"end":2,"speaker":"aaa","text":"it is"},
		{"id":1,"start":2,"end":4,"speaker":"bbb","text":"is that right"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("not really audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(speak, []byte(`{"aaa":"Alice"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(audio, trans, speak, truth)
	if err != nil {
		t.Fatal(err)
	}
	return s, truth
}

func readTruth(t *testing.T, path string) TruthFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing written: %v", err)
	}
	var tf TruthFile
	if err := json.Unmarshal(b, &tf); err != nil {
		t.Fatal(err)
	}
	return tf
}

// Every edit must be on disk the moment it is made. Labelling is long and dull,
// and an hour lost to a closed tab is why the pass does not get finished.
func TestEditIsPersistedImmediately(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	post(t, srv.URL+"/api/segments", `{"segments":[
		{"id":0,"start":0,"end":2,"speaker":"bbb","text":"it is","confirmed":true}]}`)

	tf := readTruth(t, truthPath)
	if len(tf.Segments) != 1 || tf.Segments[0].Speaker != "bbb" || !tf.Segments[0].Confirmed {
		t.Fatalf("edit not saved: %+v", tf.Segments)
	}
	if tf.Updated == "" || tf.Source == "" {
		t.Fatalf("provenance missing: %+v", tf)
	}
}

// A split changes the SEGMENT LIST, not just a label — the case attribution
// alone cannot express, where one speaker's last word sits in the next
// speaker's turn.
func TestSplitAndJoinChangeTheSegmentList(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// "it is" / "is that right" → the stray "is" moved to the first turn.
	post(t, srv.URL+"/api/segments", `{"segments":[
		{"id":0,"start":0,"end":2.5,"speaker":"aaa","text":"it is is","confirmed":true},
		{"id":1,"start":2.5,"end":4,"speaker":"bbb","text":"that right","confirmed":true}]}`)

	tf := readTruth(t, truthPath)
	if len(tf.Segments) != 2 {
		t.Fatalf("want 2 segments, got %d", len(tf.Segments))
	}
	if tf.Segments[0].Text != "it is is" || tf.Segments[1].Text != "that right" {
		t.Fatalf("boundary not moved: %+v", tf.Segments)
	}
	if tf.Segments[0].End != 2.5 || tf.Segments[1].Start != 2.5 {
		t.Fatalf("times not adjusted: %+v", tf.Segments)
	}
}

// An empty list is always a bug — a serialisation slip or a race — and writing
// it would erase the whole pass.
func TestEmptySegmentListIsRefused(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	post(t, srv.URL+"/api/segments", `{"segments":[{"id":0,"start":0,"end":2,"speaker":"aaa","text":"x"}]}`)
	resp, err := http.Post(srv.URL+"/api/segments", "application/json", strings.NewReader(`{"segments":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("an empty segment list must be refused")
	}
	if len(readTruth(t, truthPath).Segments) != 1 {
		t.Fatal("refused save must leave the previous state intact")
	}
}

// "unclear" is a real answer and must survive as one, distinct from unconfirmed:
// it marks the turns where NEITHER signal should be expected to be right.
func TestUnclearIsDistinctFromUnconfirmed(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	post(t, srv.URL+"/api/segments", `{"segments":[
		{"id":0,"start":0,"end":2,"speaker":"aaa","text":"x","unclear":true},
		{"id":1,"start":2,"end":4,"speaker":"bbb","text":"y"}]}`)

	tf := readTruth(t, truthPath)
	if !tf.Segments[0].Unclear || tf.Segments[0].Confirmed {
		t.Fatalf("unclear not recorded distinctly: %+v", tf.Segments[0])
	}
	if tf.Segments[1].Unclear || tf.Segments[1].Confirmed {
		t.Fatalf("untouched segment must stay unconfirmed: %+v", tf.Segments[1])
	}
}

// Resuming must restore the EDITED list. Falling back to the original would
// silently discard every join and split from the previous session.
func TestResumeRestoresEditedSegments(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	post(t, srv.URL+"/api/segments", `{"segments":[
		{"id":0,"start":0,"end":4,"speaker":"aaa","text":"it is is that right","confirmed":true}]}`)
	srv.Close()

	s2, err := New(s.audioPath, s.transPath, s.speakPath, truthPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.segments) != 1 || s2.segments[0].Text != "it is is that right" {
		t.Fatalf("edits not restored: %+v", s2.segments)
	}
	if len(s2.original) != 2 {
		t.Fatalf("the original must stay available for scoring: %+v", s2.original)
	}
}

// A speaker catalog in the diarize response's list form must load too, so an
// existing catalog can be pointed at without converting it first.
func TestSpeakerCatalogAcceptsListForm(t *testing.T) {
	dir := t.TempDir()
	trans := filepath.Join(dir, "h.json")
	audio := filepath.Join(dir, "a.mp4")
	speak := filepath.Join(dir, "s.json")
	_ = os.WriteFile(trans, []byte(`{"segments":[{"id":0,"start":0,"end":1,"speaker":"aaa","text":"x"}]}`), 0o644)
	_ = os.WriteFile(audio, []byte("x"), 0o644)
	_ = os.WriteFile(speak, []byte(`[{"uuid":"aaa","label":"Judge"},{"uuid":"bbb","name":"Clerk"}]`), 0o644)

	s, err := New(audio, trans, speak, filepath.Join(dir, "t.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.speakers["aaa"] != "Judge" || s.speakers["bbb"] != "Clerk" {
		t.Fatalf("list-form catalog not loaded: %+v", s.speakers)
	}
}

// A transcript with no segments is a wrong file, and saying so beats serving an
// empty page that looks like a finished job.
func TestNonDiarizeInputIsRefused(t *testing.T) {
	dir := t.TempDir()
	trans := filepath.Join(dir, "h.json")
	audio := filepath.Join(dir, "a.mp4")
	_ = os.WriteFile(trans, []byte(`{"text":"just a transcript"}`), 0o644)
	_ = os.WriteFile(audio, []byte("x"), 0o644)
	if _, err := New(audio, trans, "", filepath.Join(dir, "t.json")); err == nil {
		t.Fatal("expected refusal for a transcript with no segments")
	}
}

// The audio route must support Range, or seeking to a turn re-downloads the
// whole recording each time and labelling is unusable.
func TestAudioSupportsRangeRequests(t *testing.T) {
	s, _ := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/audio", nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("want 206 for a range request, got %d", resp.StatusCode)
	}
}

// Naming a speaker must reach both files: the catalog other tools read, and the
// truth file, which has to be readable on its own.
func TestNamingASpeakerUpdatesCatalogAndTruth(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	post(t, srv.URL+"/api/speaker", `{"uuid":"bbb","label":"Clerk"}`)

	if got := readTruth(t, truthPath).Speakers["bbb"]; got != "Clerk" {
		t.Fatalf("truth file missing the label: %q", got)
	}
	b, _ := os.ReadFile(s.speakPath)
	var cat map[string]string
	_ = json.Unmarshal(b, &cat)
	if cat["bbb"] != "Clerk" {
		t.Fatalf("catalog missing the label: %+v", cat)
	}
}

func post(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST %s → %d", url, resp.StatusCode)
	}
}

// Naming two ids the same person must leave ONE speaker in the truth file. If
// the absorbed id survives, scoring counts two speakers where the person who
// listened said one — the exact error a ground-truth pass exists to remove.
func TestMergedSpeakerLeavesTheCatalogClean(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// The page rewrites the segments and then drops the absorbed id.
	post(t, srv.URL+"/api/segments", `{"segments":[
		{"id":0,"start":0,"end":2,"speaker":"aaa","text":"it is","confirmed":true},
		{"id":1,"start":2,"end":4,"speaker":"aaa","text":"is that right","confirmed":true}]}`)
	post(t, srv.URL+"/api/speaker", `{"uuid":"bbb","remove":true}`)
	post(t, srv.URL+"/api/speaker", `{"uuid":"aaa","label":"Alice"}`)

	tf := readTruth(t, truthPath)
	if _, gone := tf.Speakers["bbb"]; gone {
		t.Fatalf("absorbed id still in the catalog: %+v", tf.Speakers)
	}
	if tf.Speakers["aaa"] != "Alice" {
		t.Fatalf("surviving id mislabelled: %+v", tf.Speakers)
	}
	for _, sg := range tf.Segments {
		if sg.Speaker != "aaa" {
			t.Fatalf("segment still points at the absorbed id: %+v", sg)
		}
	}
	b, _ := os.ReadFile(s.speakPath)
	var cat map[string]string
	_ = json.Unmarshal(b, &cat)
	if _, gone := cat["bbb"]; gone {
		t.Fatalf("absorbed id still in the shared catalog: %+v", cat)
	}
}

// A corrected transcript is ground truth for WORDS; an untouched one is only the
// recogniser's guess. Scoring word error rate against the latter would measure
// the recogniser against itself and report a flawless zero, so the distinction
// has to survive the round trip.
func TestCorrectedTextIsMarkedAndPersisted(t *testing.T) {
	s, truthPath := fixture(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	post(t, srv.URL+"/api/segments", `{"segments":[
		{"id":0,"start":0,"end":2,"speaker":"aaa","text":"it is","corrected":true,"confirmed":true},
		{"id":1,"start":2,"end":4,"speaker":"bbb","text":"is that right"}]}`)

	tf := readTruth(t, truthPath)
	if !tf.Segments[0].Corrected {
		t.Fatalf("correction flag lost: %+v", tf.Segments[0])
	}
	if tf.Segments[1].Corrected {
		t.Fatalf("untouched text must not claim to be corrected: %+v", tf.Segments[1])
	}
}

// Word times must reach the page. Interpolating a word's position inside its
// turn assumes an even speaking rate; one pause put the estimate 5-8 seconds out,
// and the error compounded across splits because a position within a segment
// stops meaning anything once the segment changes.
func TestWordTimesAreServedWhenPresent(t *testing.T) {
	dir := t.TempDir()
	trans := filepath.Join(dir, "h.json")
	audio := filepath.Join(dir, "a.mp4")
	_ = os.WriteFile(trans, []byte(`{"segments":[{"id":0,"start":0,"end":4,"speaker":"aaa","text":"it is so"}],
		"words":[{"word":"it","start":0.1},{"word":"is","start":2.9},{"word":"so","start":3.4}]}`), 0o644)
	_ = os.WriteFile(audio, []byte("x"), 0o644)
	s, err := New(audio, trans, "", filepath.Join(dir, "t.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.words) != 3 || s.words[1].Start != 2.9 {
		t.Fatalf("words not loaded: %+v", s.words)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Words []Word `json:"words"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Words) != 3 || got.Words[2].Word != "so" {
		t.Fatalf("words not served: %+v", got.Words)
	}
}

// A transcript with no word timestamps must still work — older files and any
// recogniser without them fall back to interpolation rather than breaking.
func TestMissingWordTimesIsNotAnError(t *testing.T) {
	s, _ := fixture(t)
	if len(s.words) != 0 {
		t.Fatalf("expected no words, got %+v", s.words)
	}
}

// The strip has to line up with the audio it describes, so the envelope length
// must follow the duration at the declared rate. A drifting length would put
// every bar — and the cut marker drawn on it — in the wrong place.
func TestPeaksLengthTracksDuration(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	// 3 seconds: 1s tone, 1s silence, 1s tone — the shape the strip exists to show.
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono:d=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-filter_complex", "[0][1][2]concat=n=3:v=0:a=1", wav)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not synthesise fixture: %v %s", err, out)
	}
	p, err := Peaks(wav)
	if err != nil {
		t.Fatal(err)
	}
	want := 3 * peaksPerSecond
	if len(p) < want-peaksPerSecond/2 || len(p) > want+peaksPerSecond/2 {
		t.Fatalf("envelope is %d samples, want about %d for 3s at %d/s", len(p), want, peaksPerSecond)
	}
	// The silent second must read as silence, or the strip cannot be used to
	// find the gap a speaker change sits in.
	mid := p[peaksPerSecond+peaksPerSecond/4 : peaksPerSecond*2-peaksPerSecond/4]
	for _, v := range mid {
		if v > 8 {
			t.Fatalf("silence measured as %d — the gap would be invisible", v)
		}
	}
	// And the tone must not be, after the display scaling.
	if p[peaksPerSecond/2] < 100 {
		t.Fatalf("tone measured as %d — the strip would be flat", p[peaksPerSecond/2])
	}
}

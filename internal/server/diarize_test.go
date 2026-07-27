package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iodesystems/oidio/internal/engine"
)

func TestResolveSpeakersMintsDistinct(t *testing.T) {
	det := []engine.SpeakerVoice{
		{Local: 0, Embedding: []float32{1, 0}},
		{Local: 1, Embedding: []float32{0, 1}},
	}
	n := 0
	uuidOf, speakers := resolveSpeakers(det, nil, 0.5, func() string { n++; return fmt.Sprintf("new-%d", n) })
	if len(uuidOf) != 2 || len(speakers) != 2 {
		t.Fatalf("want 2 speakers, got %d/%d", len(uuidOf), len(speakers))
	}
	if uuidOf[0] == uuidOf[1] {
		t.Error("distinct speakers should get distinct UUIDs")
	}
	for _, s := range speakers { // each has similarity to the one other speaker
		if len(s.Similarity) != 1 {
			t.Errorf("speaker %s: %d similarity entries, want 1", s.UUID, len(s.Similarity))
		}
	}
}

func TestResolveSpeakersReusesKnownAboveThreshold(t *testing.T) {
	det := []engine.SpeakerVoice{
		{Local: 0, Embedding: []float32{1, 0}},   // ~matches CARL
		{Local: 1, Embedding: []float32{0.1, 1}}, // no match
	}
	known := []knownSpeaker{{UUID: "CARL", Embedding: []float32{0.99, 0.01}}}
	uuidOf, _ := resolveSpeakers(det, known, 0.8, func() string { return "MINTED" })
	if uuidOf[0] != "CARL" {
		t.Errorf("speaker 0 should reuse CARL, got %q", uuidOf[0])
	}
	if uuidOf[1] != "MINTED" {
		t.Errorf("speaker 1 should be minted, got %q", uuidOf[1])
	}
}

func TestResolveSpeakersBelowThresholdMints(t *testing.T) {
	det := []engine.SpeakerVoice{{Local: 0, Embedding: []float32{1, 0}}}
	known := []knownSpeaker{{UUID: "CARL", Embedding: []float32{0.7, 0.7}}} // cos ≈ 0.707
	uuidOf, _ := resolveSpeakers(det, known, 0.9, func() string { return "MINTED" })
	if uuidOf[0] != "MINTED" {
		t.Errorf("below-threshold match should mint, got %q", uuidOf[0])
	}
}

func TestSpeakerLabelsByFirstAppearance(t *testing.T) {
	segs := []segment{
		{Speaker: "b-uuid"}, {Speaker: "a-uuid"}, {Speaker: "b-uuid"},
	}
	l := speakerLabels(segs)
	if l["b-uuid"] != "Speaker 1" || l["a-uuid"] != "Speaker 2" {
		t.Errorf("labels should number by first appearance, got %v", l)
	}
}

func TestDiarCuesSRT(t *testing.T) {
	segs := []segment{
		{Start: 0, End: 1.5, Text: "hello there", Speaker: "u1"},
		{Start: 2, End: 2, Text: "hi", Speaker: "u2"}, // one-token turn: End == Start
		{Start: 9, End: 9.2, Text: "", Speaker: "u1"}, // empty text is skipped
	}
	got := diarCues(segs, false)
	want := "1\n00:00:00,000 --> 00:00:02,000\nSpeaker 1: hello there\n\n" +
		"2\n00:00:02,000 --> 00:00:04,000\nSpeaker 2: hi\n\n"
	if got != want {
		t.Errorf("srt =\n%q\nwant\n%q", got, want)
	}
}

func TestDiarCuesVTTHeaderAndTimebase(t *testing.T) {
	got := diarCues([]segment{{Start: 0, End: 0.1, Text: "x", Speaker: "u1"}}, true)
	if !strings.HasPrefix(got, "WEBVTT\n\n") {
		t.Errorf("vtt must start with the WEBVTT header, got %q", got)
	}
	if !strings.Contains(got, "00:00:00.000 --> ") { // vtt uses '.' not ','
		t.Errorf("vtt should use dot-millisecond times, got %q", got)
	}
}

// A cue is stretched toward the next turn but never across a long silence.
func TestDiarCuesCapsExtensionOverSilence(t *testing.T) {
	segs := []segment{
		{Start: 0, End: 1, Text: "a", Speaker: "u1"},
		{Start: 60, End: 61, Text: "b", Speaker: "u1"},
	}
	got := diarCues(segs, false)
	if !strings.Contains(got, "00:00:00,000 --> 00:00:03,000") {
		t.Errorf("first cue should stop 2s after its last token, got %q", got)
	}
}

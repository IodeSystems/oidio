package server

import (
	"fmt"
	"testing"

	"github.com/iodesystems/oidio/internal/engine"
)

func TestCosine(t *testing.T) {
	approx := func(got, want float32) bool { d := got - want; return d < 0.001 && d > -0.001 }
	if c := cosine([]float32{1, 0}, []float32{1, 0}); !approx(c, 1) {
		t.Errorf("identical = %f, want 1", c)
	}
	if c := cosine([]float32{1, 0}, []float32{0, 1}); !approx(c, 0) {
		t.Errorf("orthogonal = %f, want 0", c)
	}
	if c := cosine([]float32{1, 0}, []float32{-1, 0}); !approx(c, -1) {
		t.Errorf("opposite = %f, want -1", c)
	}
	if c := cosine([]float32{1}, []float32{1, 2}); c != 0 {
		t.Errorf("mismatched length = %f, want 0", c)
	}
	if c := cosine(nil, nil); c != 0 {
		t.Errorf("empty = %f, want 0", c)
	}
}

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

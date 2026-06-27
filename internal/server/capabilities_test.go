package server

import "testing"

func TestCapabilityOf(t *testing.T) {
	for typ, want := range map[string]string{
		"transducer": "audio.stt",
		"diarize":    "audio.stt",
		"tts":        "audio.tts",
		"realtime":   "audio.realtime",
		"weird":      "unknown",
		"":           "unknown",
	} {
		if got := capabilityOf(typ); got != want {
			t.Errorf("capabilityOf(%q) = %q, want %q", typ, got, want)
		}
	}
}

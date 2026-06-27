package audio

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
)

func TestDecodePCMRoundtrip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	in := make([]float32, SampleRate) // 1s ramp
	for i := range in {
		in[i] = float32(i%200)/200 - 0.5
	}
	out, err := DecodePCM(context.Background(), bytes.NewReader(wavBytes(in, SampleRate)))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d samples, want %d", len(out), len(in))
	}
	for i := 0; i < len(in); i += 997 {
		if d := out[i] - in[i]; d > 0.01 || d < -0.01 {
			t.Errorf("sample %d: got %f want %f (pcm16 roundtrip)", i, out[i], in[i])
		}
	}
	if _, err := DecodePCM(context.Background(), bytes.NewReader([]byte("not audio at all"))); err == nil {
		t.Error("expected an error decoding garbage")
	}
}

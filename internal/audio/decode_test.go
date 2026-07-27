package audio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

// m4a/mp4 is the common phone-recording format and its demuxer must seek, so it
// only decodes when ffmpeg gets a real path. Piping stdin fails AND exits 0,
// which used to surface as a silent empty decode plus a downstream panic.
func TestDecodePCMSeekableContainer(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	// Build a 1s m4a. It has to go to a real file — mp4 needs a seekable output,
	// which is the mirror image of the read-side bug this guards.
	path := filepath.Join(t.TempDir(), "fixture.m4a")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "aac", path)
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot synthesize m4a fixture: %v", err)
	}
	m4a, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// feed it as a plain non-seekable reader, exactly as an upload arrives
	out, err := DecodePCM(context.Background(), bytes.NewReader(m4a))
	if err != nil {
		t.Fatalf("m4a decode: %v", err)
	}
	if len(out) < SampleRate/2 {
		t.Errorf("got %d samples from a 1s clip, want ~%d", len(out), SampleRate)
	}
}

func TestDecodePCMEmptyIsAnError(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := DecodePCM(context.Background(), bytes.NewReader(nil)); err == nil {
		t.Error("empty input must error, not return zero samples (sherpa panics on those)")
	}
}

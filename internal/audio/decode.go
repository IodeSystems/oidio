// Package audio decodes uploaded media into the PCM sherpa-onnx expects.
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os/exec"
)

// SampleRate is the rate sherpa-onnx models are trained at (16 kHz mono).
const SampleRate = 16000

// DecodePCM decodes any ffmpeg-readable container/codec (wav, mp3, webm, ogg,
// flac, m4a, …) from r into mono float32 PCM at 16 kHz. ffmpeg's f32le output is
// already normalized to [-1, 1], which is exactly what AcceptWaveform wants, so
// there's no manual scaling.
func DecodePCM(ctx context.Context, r io.Reader) ([]float32, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-f", "f32le", "-ac", "1", "-ar", fmt.Sprint(SampleRate), "pipe:1")
	cmd.Stdin = r
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode failed: %v: %s", err, truncate(errb.String(), 300))
	}
	raw := out.Bytes()
	n := len(raw) / 4
	samples := make([]float32, n)
	for i := 0; i < n; i++ {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return samples, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

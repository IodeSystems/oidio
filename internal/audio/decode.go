// Package audio decodes uploaded media into the PCM sherpa-onnx expects.
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
)

// SampleRate is the rate sherpa-onnx models are trained at (16 kHz mono).
const SampleRate = 16000

// DecodePCM decodes any ffmpeg-readable container/codec (wav, mp3, webm, ogg,
// flac, m4a, …) from r into mono float32 PCM at 16 kHz. ffmpeg's f32le output is
// already normalized to [-1, 1], which is exactly what AcceptWaveform wants, so
// there's no manual scaling.
//
// The input is spooled to a temp file rather than piped to ffmpeg's stdin: the
// MP4/M4A demuxer has to seek (the moov atom is often at the END of the file),
// and on a pipe it fails with "partial file" while still EXITING 0 — a silent
// empty decode. A seekable path is the only way m4a/mp4 uploads work at all.
func DecodePCM(ctx context.Context, r io.Reader) ([]float32, error) {
	f, err := os.CreateTemp("", "oidio-decode-*")
	if err != nil {
		return nil, fmt.Errorf("decode: temp file: %w", err)
	}
	defer os.Remove(f.Name())
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("decode: buffering upload: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("decode: temp file: %w", closeErr)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", f.Name(), "-f", "f32le", "-ac", "1", "-ar", fmt.Sprint(SampleRate), "pipe:1")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode failed: %v: %s", err, truncate(errb.String(), 300))
	}
	raw := out.Bytes()
	n := len(raw) / 4
	if n == 0 {
		// ffmpeg can report success and still emit nothing (unsupported stream,
		// truncated container). Downstream sherpa indexes samples[0] and panics, so
		// this has to be an error, not an empty result.
		return nil, fmt.Errorf("no decodable audio in input: %s", truncate(errb.String(), 300))
	}
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

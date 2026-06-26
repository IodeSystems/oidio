package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
)

// Format describes one OpenAI response_format: its wire name, MIME type, and the
// ffmpeg encoder to use (empty = produced natively, no ffmpeg).
type Format struct {
	MIME   string
	ffmpeg string // -f value; "" for wav/pcm (native)
	rawPCM bool   // pcm: headerless s16le
}

var formats = map[string]Format{
	"mp3":  {"audio/mpeg", "mp3", false},
	"opus": {"audio/ogg", "opus", false},
	"aac":  {"audio/aac", "adts", false},
	"flac": {"audio/flac", "flac", false},
	"wav":  {"audio/wav", "", false},
	"pcm":  {"audio/L16", "", true},
}

// LookupFormat returns the encoding for an OpenAI response_format (default mp3).
func LookupFormat(name string) (Format, bool) {
	if name == "" {
		name = "mp3"
	}
	f, ok := formats[name]
	return f, ok
}

// Encode renders mono float32 samples to the given format. wav/pcm are written
// directly; the rest are piped through ffmpeg.
func Encode(ctx context.Context, samples []float32, rate int, f Format) ([]byte, error) {
	if f.rawPCM {
		return pcm16(samples), nil
	}
	wav := wavBytes(samples, rate)
	if f.ffmpeg == "" { // wav
		return wav, nil
	}
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-f", f.ffmpeg, "pipe:1")
	cmd.Stdin = bytes.NewReader(wav)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg encode (%s) failed: %v: %s", f.ffmpeg, err, truncate(errb.String(), 300))
	}
	return out.Bytes(), nil
}

func pcm16(samples []float32) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(int16(clamp(s)*32767)))
	}
	return b
}

func wavBytes(samples []float32, rate int) []byte {
	pcm := pcm16(samples)
	var b bytes.Buffer
	dataLen := len(pcm)
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))     // PCM chunk size
	binary.Write(&b, binary.LittleEndian, uint16(1))      // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1))      // mono
	binary.Write(&b, binary.LittleEndian, uint32(rate))   // sample rate
	binary.Write(&b, binary.LittleEndian, uint32(rate*2)) // byte rate
	binary.Write(&b, binary.LittleEndian, uint16(2))      // block align
	binary.Write(&b, binary.LittleEndian, uint16(16))     // bits/sample
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataLen))
	b.Write(pcm)
	return b.Bytes()
}

func clamp(s float32) float32 {
	if s > 1 {
		return 1
	}
	if s < -1 {
		return -1
	}
	return s
}

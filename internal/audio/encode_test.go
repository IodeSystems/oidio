package audio

import (
	"encoding/binary"
	"testing"
)

func TestLookupFormat(t *testing.T) {
	if f, ok := LookupFormat(""); !ok || f.MIME != "audio/mpeg" {
		t.Fatalf("default should be mp3, got %+v ok=%v", f, ok)
	}
	if f, ok := LookupFormat("wav"); !ok || f.ffmpeg != "" || f.MIME != "audio/wav" {
		t.Fatalf("wav should be native (no ffmpeg), got %+v", f)
	}
	if f, ok := LookupFormat("pcm"); !ok || !f.rawPCM {
		t.Fatalf("pcm should be raw, got %+v", f)
	}
	if f, ok := LookupFormat("mp3"); !ok || f.ffmpeg != "mp3" {
		t.Fatalf("mp3 should encode via ffmpeg, got %+v", f)
	}
	if _, ok := LookupFormat("bogus"); ok {
		t.Fatal("bogus format should not be ok")
	}
}

func TestPcm16Clamp(t *testing.T) {
	in := []float32{0, 1, -1, 2, -2, 0.5}
	b := pcm16(in)
	if len(b) != len(in)*2 {
		t.Fatalf("len %d want %d", len(b), len(in)*2)
	}
	want := []int16{0, 32767, -32767, 32767, -32767, 16383} // ±2 clamp to ±1
	for i, w := range want {
		got := int16(binary.LittleEndian.Uint16(b[i*2:]))
		if got != w {
			t.Errorf("sample %d: got %d want %d", i, got, w)
		}
	}
}

func TestWavBytesHeader(t *testing.T) {
	samples := []float32{0, 0.5, -0.5, 1}
	wav := wavBytes(samples, 16000)
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[12:16]) != "fmt " {
		t.Fatal("bad RIFF/WAVE/fmt header")
	}
	if af := binary.LittleEndian.Uint16(wav[20:22]); af != 1 {
		t.Errorf("audio format = %d, want 1 (PCM)", af)
	}
	if ch := binary.LittleEndian.Uint16(wav[22:24]); ch != 1 {
		t.Errorf("channels = %d, want 1", ch)
	}
	if rate := binary.LittleEndian.Uint32(wav[24:28]); rate != 16000 {
		t.Errorf("rate = %d, want 16000", rate)
	}
	if bits := binary.LittleEndian.Uint16(wav[34:36]); bits != 16 {
		t.Errorf("bits = %d, want 16", bits)
	}
	if string(wav[36:40]) != "data" {
		t.Fatal("missing data chunk")
	}
	if dl := binary.LittleEndian.Uint32(wav[40:44]); int(dl) != len(samples)*2 {
		t.Errorf("data len = %d, want %d", dl, len(samples)*2)
	}
	if len(wav) != 44+len(samples)*2 {
		t.Errorf("total len = %d, want %d", len(wav), 44+len(samples)*2)
	}
}

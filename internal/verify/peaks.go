package verify

import (
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
)

// peaksPerSecond is the resolution of the amplitude strip.
//
// 50/s is one bar per 20 ms — fine enough to show the gap between two words,
// which is the thing being looked for, and coarse enough that a 44-minute
// hearing is a few hundred KB rather than a few MB. Higher resolution would
// draw detail no one can act on: the strip is used to find a PAUSE, not to
// inspect a waveform.
const peaksPerSecond = 50

// peaksRate is the sample rate the audio is decoded at for measurement only.
// Amplitude envelope survives downsampling; nothing here is played.
const peaksRate = 8000

// Peaks returns a normalised amplitude envelope, one byte per 1/peaksPerSecond
// of audio.
//
// This exists because finding a speaker change inside a two-minute turn is
// currently done by ear, scrubbing back and forth. A speaker change almost
// always sits in a silence, and silence is the one thing an amplitude strip
// shows at a glance. It is a search aid, not evidence — the attribution still
// has to be heard.
func Peaks(src string) ([]byte, error) {
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", src,
		"-ac", "1", "-ar", fmt.Sprint(peaksRate), "-f", "s16le", "-")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	per := peaksRate / peaksPerSecond
	buf := make([]byte, per*2)
	var peaks []byte
	var loudest uint16
	for {
		n, err := io.ReadFull(out, buf)
		if n > 0 {
			var m uint16
			for i := 0; i+1 < n; i += 2 {
				v := int16(binary.LittleEndian.Uint16(buf[i : i+2]))
				a := uint16(v)
				if v < 0 {
					a = uint16(-int32(v))
				}
				if a > m {
					m = a
				}
			}
			if m > loudest {
				loudest = m
			}
			peaks = append(peaks, byte(m>>8))
		}
		if err != nil {
			break
		}
	}
	_ = cmd.Wait()
	if len(peaks) == 0 {
		return nil, fmt.Errorf("no audio decoded from %s", src)
	}

	// Scale to the loudest peak. A courtroom recording sits well below full
	// scale, and an unscaled strip would be a flat line with occasional bumps —
	// which hides exactly the quiet-speech-versus-silence distinction it is
	// drawn for. This is display gain only; nothing downstream reads it.
	hi := byte(loudest >> 8)
	if hi > 0 && hi < 255 {
		f := 255.0 / float64(hi)
		for i, p := range peaks {
			v := float64(p) * f
			if v > 255 {
				v = 255
			}
			peaks[i] = byte(v)
		}
	}
	return peaks, nil
}

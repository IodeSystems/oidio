package verify

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Loudness normalisation for LISTENING, not for the models.
//
// A hearing recording is not mixed. Someone at the bench is loud, someone across
// the room is faint, and the gap is wide: the recording this was built for
// measures 17.2 LU of loudness range with quiet passages at -36 LUFS. Turning it
// up is not available — its true peak is already 0.1 dBFS — so the range itself
// has to come down.
//
// That gap is a human problem specifically. The recogniser reads the quiet parts
// correctly; a person labelling them cannot hear what to confirm, and a segment
// nobody can hear is one they will guess at, which is worse for ground truth than
// leaving it unreviewed.
//
// speechnorm rather than dynaudnorm: dynaudnorm normalises over a sliding window
// and so lifts room tone during pauses, which over twenty minutes of labelling is
// exhausting. speechnorm expands each half-cycle of the waveform, so silence
// stays silent.
const DefaultAudioFilter = "highpass=f=80,speechnorm=e=25:r=0.0005:l=1,alimiter=limit=0.95"

// Normalize writes a level-corrected copy for playback and returns its path.
//
// The ORIGINAL is what everything else uses. This copy exists only to be played
// through a browser, so it is never handed to a model and never written beside
// the source.
func Normalize(src, filter string) (string, error) {
	if filter == "" {
		filter = DefaultAudioFilter
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg not found: %w", err)
	}
	dst := filepath.Join(os.TempDir(),
		"oidio-verify-"+strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+".m4a")

	// Reuse a copy that is already newer than its source. Levelling a long
	// recording takes the better part of a minute, and the server gets restarted
	// often enough that paying it every time would discourage restarting at all.
	if si, err := os.Stat(src); err == nil {
		if di, err := os.Stat(dst); err == nil && di.ModTime().After(si.ModTime()) && di.Size() > 0 {
			return dst, nil
		}
	}

	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", src, "-af", filter, "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// A filter that changed the DURATION would shift every timestamp in the
	// transcript against the audio, and the failure would look like bad
	// diarization rather than a bad audio pipeline. None of the default filters
	// resample or retime, but the check costs nothing and the silent version of
	// this bug is expensive.
	sd, err1 := duration(src)
	dd, err2 := duration(dst)
	if err1 == nil && err2 == nil && math.Abs(sd-dd) > 0.25 {
		os.Remove(dst)
		return "", fmt.Errorf("filter changed duration %.2fs -> %.2fs; refusing to serve audio "+
			"whose timestamps no longer match the transcript", sd, dd)
	}
	return dst, nil
}

func duration(path string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// Loudness reports integrated loudness and range, for saying what the
// normalisation actually did rather than asserting it helped.
func Loudness(path string) (lufs, lra float64, err error) {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-i", path, "-af", "ebur128", "-f", "null", "-")
	out, _ := cmd.CombinedOutput()
	txt := string(out)
	i := strings.LastIndex(txt, "Integrated loudness")
	if i < 0 {
		return 0, 0, fmt.Errorf("no ebur128 summary")
	}
	for _, line := range strings.Split(txt[i:], "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "I:" {
			lufs, _ = strconv.ParseFloat(f[1], 64)
		}
		if len(f) >= 2 && f[0] == "LRA:" {
			lra, _ = strconv.ParseFloat(f[1], 64)
		}
	}
	return lufs, lra, nil
}

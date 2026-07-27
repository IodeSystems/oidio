package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/iodesystems/oidio/internal/verify"
)

// verifyCmd is `oidio verify` — the ground-truth workbench.
//
// A labelling pass is the only thing that can settle a disagreement between the
// acoustic and semantic reviewers: both produced confident, mutually
// contradictory proposals on the same hearing, and without labels there is no
// way to score either. This produces those labels.
func verifyCmd(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1", "host to bind (the port is chosen and printed)")
	port := fs.String("port", "", "fixed port; default is one chosen by the OS and printed")
	trans := fs.String("transcription", "", "diarize result to label (verbose_json)")
	audioPath := fs.String("audio", "", "the recording")
	speak := fs.String("speakers", "", "speaker catalog to read and update (uuid -> label)")
	truth := fs.String("truth", "", "where labels are written (default: TRANSCRIPTION.truth.json)")
	raw := fs.Bool("raw", false, "play the original audio instead of a level-corrected copy")
	afilter := fs.String("audio-filter", "", "ffmpeg filter chain for playback (default: speech normalisation)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: oidio verify --audio FILE --transcription FILE [--speakers FILE]

Serves a page for confirming or correcting who spoke each segment, so
diarization can be scored against what a person actually heard.

Labels are saved on every keystroke, not on a Save button.

`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *trans == "" || *audioPath == "" {
		fs.Usage()
		os.Exit(2)
	}
	if *truth == "" {
		*truth = trimExt(*trans) + ".truth.json"
	}

	// Normalised for LISTENING only. A hearing recording is not mixed, and a
	// passage nobody can hear is one they will guess at — worse for ground truth
	// than one left unreviewed. The original is untouched and is still what every
	// other tool reads.
	playPath := *audioPath
	if !*raw {
		before, beforeLRA, _ := verify.Loudness(*audioPath)
		norm, err := verify.Normalize(*audioPath, *afilter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  audio not normalised (%v) — playing the original\n", err)
		} else {
			after, afterLRA, _ := verify.Loudness(norm)
			fmt.Printf("  levelled: %.1f LUFS / %.1f LU range -> %.1f LUFS / %.1f LU\n",
				before, beforeLRA, after, afterLRA)
			playPath = norm
		}
	}

	s, err := verify.New(playPath, *trans, *speak, *truth)
	if err != nil {
		fatal("%v", err)
	}
	ln, url, err := verify.ListenOn(*listen, *port)
	if err != nil {
		fatal("listen on %s: %v", *listen, err)
	}
	fmt.Printf("oidio verify → %s\n", url)
	fmt.Printf("  audio:   %s\n", *audioPath)
	fmt.Printf("  labels:  %s  (written on every change)\n", *truth)
	if *speak != "" {
		fmt.Printf("  speakers: %s\n", *speak)
	}
	fmt.Printf("  keys are shown on the page — space/j/k to move, c confirm, a join, s split, d label, u undo\n")
	if err := http.Serve(ln, s.Handler()); err != nil {
		fatal("serve: %v", err)
	}
}

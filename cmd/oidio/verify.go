package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/oidio/internal/verify"
)

// verifyCmd is `oidio verify` — the ground-truth workbench.
//
// A labelling pass is the only thing that can settle a disagreement between the
// acoustic and semantic reviewers: both produced confident, mutually
// contradictory proposals on the same hearing, and without labels there is no
// way to score either. This produces those labels.
// verifyRender is `oidio verify render` — the truth file as prose.
//
// Lives with verify rather than as a separate tool because it reads the same
// file the workbench writes, and a renderer that has its own idea of what
// `confirmed` or `affirmed` mean will drift from the thing producing them. That
// drift already happened once when the two lived in different languages: a field
// was added here and the reader kept reporting the old numbers, quietly
// understating how much had been reviewed.
// verifyScore is `oidio verify score` — how far the machine was from the person.
//
// In the binary rather than a script because it reads the truth file's schema,
// and a reader that keeps its own copy of what `confirmed` and `affirmed` mean
// goes stale silently. That is not hypothetical: a field was added to the
// workbench and the external scorer kept reporting the old coverage for days,
// understating how much had been reviewed.
func verifyScore(args []string) {
	fs := flag.NewFlagSet("verify score", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit the result as JSON")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: oidio verify score TRUTH.json HYPOTHESIS.json

Scores a diarization result against what a person actually heard.

Two numbers, because one hides which failure produced it. STRICT is the DER
convention — one cluster per speaker, so over-splitting counts as error. MERGED
assigns each cluster to whichever speaker it overlaps most, ignoring
over-splitting, and asks only whether the voices were SEPARATED. The gap between
them is what over-splitting costs.

`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 2 {
		fs.Usage()
		os.Exit(2)
	}
	res, mapping, err := verify.Score(fs.Arg(0), fs.Arg(1))
	if err != nil {
		fatal("%v", err)
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return
	}
	verify.Report(os.Stdout, res, mapping)
}

func verifyRender(args []string) {
	fs := flag.NewFlagSet("verify render", flag.ExitOnError)
	title := fs.String("title", "", "document title (default: derived from the filename)")
	source := fs.String("source", "", "recording to name as the source (default: the truth file's own)")
	out := fs.String("o", "", "output path (default: alongside, .verified.md)")
	note := fs.String("note", "", "extra header line — what this corpus needs said before anyone quotes it")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: oidio verify render TRUTH.json [TRUTH.json...]

Writes <stem>.verified.md from <stem>.truth.json, reading <stem>.speakers.json
for names when present.

The header is generated, never written: it states how much of the file a person
actually ruled on, so a partly-reviewed transcript cannot be cited as a verified
one.

`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	for _, p := range fs.Args() {
		if !strings.HasSuffix(p, ".truth.json") {
			fmt.Fprintf(os.Stderr, "skip (not a .truth.json): %s\n", p)
			continue
		}
		md, err := verify.Render(p, *title, *source, *note)
		if err != nil {
			fatal("%v", err)
		}
		dst := *out
		if dst == "" || fs.NArg() > 1 {
			dst = strings.TrimSuffix(p, ".truth.json") + ".verified.md"
		}
		if err := os.WriteFile(dst, []byte(md), 0o644); err != nil {
			fatal("write %s: %v", dst, err)
		}
		fmt.Printf("wrote %s\n", dst)
	}
}

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

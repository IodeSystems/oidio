package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/iodesystems/oidio/internal/config"
	"github.com/iodesystems/oidio/internal/speakers"
)

// speakersReview is `oidio speakers review` — post-hoc correction of WHO said
// what, without re-running diarization.
//
// It prints a report and exits. Writing requires --apply or --apply-agreed,
// because every proposal here is a claim that the transcript is currently wrong
// about a person, and in the corpus this was built for that transcript is
// evidence. The tool's job is to make the case; approving it is not its job.
func speakersReview(args []string) {
	fs := flag.NewFlagSet("speakers review", flag.ExitOnError)
	cfgPath := fs.String("config", env("OIDIO_CONFIG", "oidio.yaml"), "oidio config (for the embedding model)")
	model := fs.String("model", "stt-diarize", "config model whose `embedding` to use")
	audioPath := fs.String("audio", "", "original audio (required: voice, not text, decides attribution)")
	out := fs.String("o", "", "write the corrected transcript here (default: alongside, .reviewed.json)")
	apply := fs.Bool("apply", false, "write every proposal")
	applyAgreed := fs.Bool("apply-agreed", false, "write only proposals BOTH voice and content agree on")
	llmEndpoint := fs.String("llm", "", "OpenAI-compatible endpoint for the content reviewer, e.g. http://127.0.0.1:8111/v1")
	llmModel := fs.String("llm-model", "", "model name for the content reviewer")
	llmKey := fs.String("llm-key", os.Getenv("OPENAI_API_KEY"), "API key for the content reviewer")
	minEmbed := fs.Float64("min-seconds", 2.0, "shortest segment whose own voiceprint is trusted")
	margin := fs.Float64("margin", 0.08, "how much better the new speaker must score to propose a move")
	joinCos := fs.Float64("join-cos", 0.85, "centroid similarity at which two speakers are proposed as one")
	passes := fs.Int("passes", 3, "re-derive centroids and re-propose this many times")
	jsonOut := fs.Bool("json", false, "emit the analysis as JSON instead of a report")
	dumpPath := fs.String("dump-exchange", "", "write the exact reviewer prompt and reply here")
	splitGain := fs.Float64("split-gain", 0.08, "how much better both halves must score before a segment is cut")
	splitStep := fs.Float64("split-step", 0.5, "granularity of the candidate cut scan, seconds")
	noSplit := fs.Bool("no-split", false, "skip split proposals (they cost one embedding per candidate cut)")
	propose := fs.Bool("propose", false, "also let the content reviewer find errors the voice pass never proposed")
	batch := fs.Bool("batch-review", false, "judge all proposals in ONE request (faster, but verdicts shift with their neighbours)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: oidio speakers review --audio FILE TRANSCRIPT.json [flags]

Reviews a diarize result for two failures, in speaker-embedding space:
  MOVE — a passage attributed to the wrong speaker
  JOIN — one person split across several speaker ids (mic distance, volume)

With --llm, a second opinion is drawn from WHAT is said, independent of how it
sounded, and the two are reported separately. Where they disagree is where a
human should look.

Nothing is written without --apply or --apply-agreed.

`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	if *audioPath == "" {
		fatal("--audio is required: deciding a passage was attributed to the wrong voice cannot be done from text")
	}

	// ~/.oidio.yml supplies defaults for anything not given explicitly, so the
	// reviewer endpoint does not have to be retyped per invocation. Flags win.
	user, err := config.LoadUser()
	if err != nil {
		fatal("user config: %v", err)
	}
	if *llmEndpoint == "" {
		*llmEndpoint = user.LLM.Endpoint
	}
	if *llmModel == "" {
		*llmModel = user.LLM.Model
	}
	if *llmKey == "" {
		*llmKey = user.LLM.Key
	}
	if user.Config != "" && !fsChanged(fs, "config") {
		*cfgPath = user.Config
	}

	if *applyAgreed && *llmEndpoint == "" {
		fatal("--apply-agreed needs a content reviewer (--llm or llm.endpoint in ~/.oidio.yml): without one there is no second signal to agree with")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("config: %v", err)
	}
	spec, ok := cfg.Models[*model]
	if !ok {
		fatal("model %q not in %s", *model, *cfgPath)
	}

	t, err := speakers.LoadTranscript(fs.Arg(0))
	if err != nil {
		fatal("%v", err)
	}
	if err := t.Embed(*audioPath, spec, *minEmbed); err != nil {
		fatal("embed: %v", err)
	}

	p := speakers.Params{
		MinEmbedSeconds: *minEmbed, MinMargin: *margin, JoinCos: *joinCos, Passes: *passes,
		MinSplitGain: *splitGain, SplitStep: *splitStep,
	}
	var spanEmbed speakers.SpanEmbedder
	if !*noSplit {
		spanEmbed = t.SpanEmbedder()
	}
	a := speakers.Analyze(t.Segments, p, spanEmbed)

	if *llmEndpoint != "" {
		if *llmModel == "" {
			fatal("--llm-model is required with --llm")
		}
		r := speakers.NewReviewer(*llmEndpoint, *llmModel, *llmKey)
		if *dumpPath != "" {
			f, err := os.Create(*dumpPath)
			if err != nil {
				fatal("dump: %v", err)
			}
			defer f.Close()
			r.Dump = f
		}
		// Content-originated proposals are gathered BEFORE judging, so they are
		// judged acoustically alongside everything else rather than arriving
		// already blessed.
		if *propose {
			extra, err := r.Propose(context.Background(), t.Segments, a.Clusters, p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "proposer failed (voice-originated results below are unaffected): %v\n", err)
			} else {
				n := len(extra.Moves) + len(extra.Joins)
				a.Merge(extra)
				fmt.Fprintf(os.Stderr, "content proposed %d correction(s) the voice pass did not\n", n)
			}
		}
		review := r.ReviewEach
		if *batch {
			review = r.Review
		}
		if err := review(context.Background(), &a, t.Segments); err != nil {
			// Not fatal: the acoustic review still stands on its own, and losing
			// the second opinion must be loud rather than silent.
			fmt.Fprintf(os.Stderr, "content reviewer failed (acoustic results below are unaffected): %v\n", err)
			if *applyAgreed {
				fatal("refusing --apply-agreed without the content reviewer's verdicts")
			}
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(a)
	} else {
		speakers.Report(os.Stdout, a, false)
	}

	if !*apply && !*applyAgreed {
		if len(a.Moves) > 0 || len(a.Joins) > 0 || len(a.Splits) > 0 {
			fmt.Fprintf(os.Stderr, "\nnothing written. re-run with --apply, or --apply-agreed to take only what both signals back.\n")
		}
		return
	}

	write := a
	if *applyAgreed {
		write = speakers.Filter(a)
		fmt.Fprintf(os.Stderr, "\napplying %d move(s), %d join(s) and %d split(s) that both signals agree on\n",
			len(write.Moves), len(write.Joins), len(write.Splits))
	}
	dst := *out
	if dst == "" {
		dst = trimExt(fs.Arg(0)) + ".reviewed.json"
	}
	doc := t.Apply(write)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fatal("encode: %v", err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		fatal("write %s: %v", dst, err)
	}
	// Written beside the input rather than over it: the original is the record of
	// what the models actually produced, and a review that silently replaced it
	// would leave no way to tell a correction from a transcription.
	fmt.Fprintf(os.Stderr, "wrote %s (original untouched)\n", dst)
}

func trimExt(p string) string {
	for i := len(p) - 1; i >= 0 && p[i] != '/'; i-- {
		if p[i] == '.' {
			return p[:i]
		}
	}
	return p
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "oidio: "+f+"\n", a...)
	os.Exit(1)
}

// fsChanged reports whether a flag was given explicitly, so a ~/.oidio.yml value
// can fill in a default without overriding something typed on the command line.
func fsChanged(fs *flag.FlagSet, name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

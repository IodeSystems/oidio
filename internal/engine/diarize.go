package engine

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/iodesystems/oidio/internal/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const sampleRate = 16000

// Diarizer runs offline speaker diarization + offline ASR and aligns them into
// speaker-labeled segments, plus a voiceprint embedding per speaker. Identity is
// the server's concern — the engine emits only local cluster ids and embeddings;
// UUIDs, similarity, and known-speaker matching live a layer up (stateless, no
// persistent catalog).
type Diarizer struct {
	mu   sync.Mutex // serializes the shared sd/asr/emb objects (cgo, unverified MT-safety)
	asr  *sherpa.OfflineRecognizer
	sd   *sherpa.OfflineSpeakerDiarization
	emb  *sherpa.SpeakerEmbeddingExtractor
	lang string

	// The model's configured clustering defaults, kept so a per-request override
	// can be applied and then not persist onto the next request through the
	// shared sd object.
	defThreshold  float32
	defNumCluster int
}

// DiarSegment is one aligned, speaker-attributed run of text.
// DiarOpts are this request's clustering overrides. Zero means "use the model's
// configured default", so a caller that sets nothing behaves as before.
type DiarOpts struct {
	ClusterThreshold float32  // AHC cutoff; lower → more speakers
	NumClusters      int      // >0 forces a known speaker count (ignores threshold)
	MergeThreshold   *float32 // ≥ this cosine between two clusters' voiceprints → same speaker; 0 disables
}

// DefaultMergeThreshold collapses clusters whose voiceprints are that close.
// High enough that only a genuinely over-split speaker re-merges; see mergeGroups
// for why lowering it is the risky direction.
const DefaultMergeThreshold float32 = 0.8

type DiarSegment struct {
	Start, End float64
	Speaker    int // local cluster id within this request
	Text       string
}

// SpeakerVoice is a detected speaker's voiceprint for this request.
type SpeakerVoice struct {
	Local     int
	Embedding []float32

	// CleanSeconds is this speaker's crosstalk-free speech; TotalSeconds is all
	// of it. Blended reports that the clean sample was too thin (minCleanSpeech)
	// and the print was built from overlapped audio after all.
	//
	// Reported because the fallback is silent and consequential: a blended print
	// is why a known-different pair of voices scored 0.876 while a known-same
	// pair scored 0.794. Without this, the only way to tell whether clean-span
	// embedding actually applied was to squint at a cosine matrix.
	CleanSeconds float64
	TotalSeconds float64
	Blended      bool
}

// TimedWord is one decoded word on the recording's clock, attributed to whoever
// diarization says was speaking at that moment.
//
// This is the unit the whole design turns on. A word belongs to exactly one
// speaker and appears exactly once, so the duplication that per-turn re-decoding
// produced in crosstalk is not something to detect and repair — it cannot be
// expressed. Text is the recogniser's own spelling, case and punctuation
// included; only the speaker is looked up by time.
type TimedWord struct {
	Text    string
	Start   float64
	Speaker int // local cluster id; -1 until assignSpeakers runs
}

type DiarResult struct {
	Text     string
	Duration float64
	Segments []DiarSegment
	Speakers []SpeakerVoice

	// Words is empty when the ASR model supplies no timestamps (sherpa's Whisper
	// decoder supplies none), in which case Segments came from per-span decoding
	// and carry no word-level detail.
	Words []TimedWord
}

func NewDiarizer(spec config.ModelSpec) (*Diarizer, error) {
	if spec.Encoder == "" || spec.Decoder == "" || spec.Joiner == "" || spec.Tokens == "" {
		return nil, fmt.Errorf("diarize needs the transducer ASR fields (encoder/decoder/joiner/tokens)")
	}
	if spec.Segmentation == "" || spec.Embedding == "" {
		return nil, fmt.Errorf("diarize needs segmentation and embedding models")
	}
	nt := reserveCore(orDefault(spec.NumThreads, 4))

	ac := sherpa.OfflineRecognizerConfig{}
	ac.FeatConfig.SampleRate = sampleRate
	ac.FeatConfig.FeatureDim = 80
	ac.ModelConfig.Transducer.Encoder = spec.Encoder
	ac.ModelConfig.Transducer.Decoder = spec.Decoder
	ac.ModelConfig.Transducer.Joiner = spec.Joiner
	ac.ModelConfig.Tokens = spec.Tokens
	ac.ModelConfig.ModelType = spec.ModelType
	ac.ModelConfig.NumThreads = nt
	ac.ModelConfig.Provider = "cpu"
	ac.DecodingMethod = "greedy_search"
	// Construct the models under a raised niceness so their onnxruntime worker
	// pools inherit low CPU priority (Linux). See withNice.
	var asr *sherpa.OfflineRecognizer
	withNice(spec.Nice, func() { asr = sherpa.NewOfflineRecognizer(&ac) })
	if asr == nil {
		return nil, fmt.Errorf("failed to init ASR recognizer (check model paths)")
	}

	thr := spec.ClusterThreshold
	if thr <= 0 {
		thr = 0.7
	}
	dc := sherpa.OfflineSpeakerDiarizationConfig{}
	dc.Segmentation.Pyannote.Model = spec.Segmentation
	dc.Segmentation.NumThreads = nt
	dc.Embedding.Model = spec.Embedding
	dc.Embedding.NumThreads = nt
	dc.Clustering.NumClusters = spec.NumClusters // 0 → auto via threshold
	dc.Clustering.Threshold = thr
	dc.MinDurationOn = orDefaultF(spec.MinDurationOn, 0.3)
	dc.MinDurationOff = orDefaultF(spec.MinDurationOff, 0.5)
	var sd *sherpa.OfflineSpeakerDiarization
	withNice(spec.Nice, func() { sd = sherpa.NewOfflineSpeakerDiarization(&dc) })
	if sd == nil {
		return nil, fmt.Errorf("failed to init diarization (check segmentation/embedding models)")
	}

	ec := sherpa.SpeakerEmbeddingExtractorConfig{Model: spec.Embedding, NumThreads: nt, Provider: "cpu"}
	var ex *sherpa.SpeakerEmbeddingExtractor
	withNice(spec.Nice, func() { ex = sherpa.NewSpeakerEmbeddingExtractor(&ec) })
	if ex == nil {
		return nil, fmt.Errorf("failed to init speaker embedding extractor")
	}

	lang := spec.Language
	if lang == "" {
		lang = "en"
	}
	return &Diarizer{
		defThreshold: thr, defNumCluster: spec.NumClusters, asr: asr, sd: sd, emb: ex, lang: lang}, nil
}

func (d *Diarizer) Language() string { return d.lang }

// Process diarizes + transcribes the whole clip (16 kHz mono).
func (d *Diarizer) Process(samples []float32, opts DiarOpts) DiarResult {
	if len(samples) == 0 {
		return DiarResult{} // sherpa's Process indexes samples[0] and would panic
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	// 0. apply this request's clustering config. sherpa's SetConfig touches only
	// the clustering fields (no model reload), and we always write both so the
	// previous request's override doesn't persist on the shared sd object.
	thr, num := d.defThreshold, d.defNumCluster
	if opts.ClusterThreshold > 0 {
		thr = opts.ClusterThreshold
	}
	if opts.NumClusters > 0 {
		num = opts.NumClusters
	}
	cc := sherpa.OfflineSpeakerDiarizationConfig{}
	cc.Clustering.Threshold = thr
	cc.Clustering.NumClusters = num
	d.sd.SetConfig(&cc)

	// 1. diarization → speaker time spans
	diar := d.sd.Process(samples)
	spans := make([]span, len(diar))
	for i, s := range diar {
		spans[i] = span{float64(s.Start), float64(s.End), s.Speaker}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	// 2. one voiceprint per cluster, then optionally merge clusters that are the
	// same person (long audio makes AHC over-split). Merging BEFORE alignment
	// means adjacent runs of a merged speaker coalesce into one segment.
	voices := d.voiceprints(samples, spans)
	// Unset means "choose for this recording"; an explicit value (including 0 =
	// off) is always obeyed, so a caller tuning the threshold is never silently
	// overridden.
	mergeThr := AdaptiveMergeThreshold(len(voices), float64(len(samples))/float64(sampleRate))
	if opts.MergeThreshold != nil {
		mergeThr = *opts.MergeThreshold
	}
	if remap := mergeGroups(voices, mergeThr); remap != nil {
		for i := range spans {
			spans[i].spk = remap[spans[i].spk]
		}
		// re-embed each merged group over its full concatenated audio
		voices = d.voiceprints(samples, spans)
	}

	// 3+4. ASR per diarization span rather than one pass over the whole file.
	//
	// The span IS the natural decode unit: it is one speaker's uninterrupted turn,
	// bounded by the pauses where they stopped talking. On the hearing that
	// prompted this, 121 turns had a median of 3.1s and a maximum of 45.9s — so
	// this does not chop mid-sentence, it decodes each turn.
	//
	// Three things follow. Attribution becomes exact, because each decode IS one
	// speaker's audio — the old spkAt guessed by nearest span midpoint and is gone.
	// No cross-speaker context bleeds into the transducer's state. And peak
	// allocation is bounded by the longest turn instead of by the recording, which
	// matters because a single AcceptWaveform over a whole file is unbounded in a
	// way no input validation catches.
	//
	// This is NOT the chunk-before-diarize mistake that collapses every speaker
	// into one: clustering above already saw the whole file, so identity is global
	// and only the decoding is per-turn.
	// One transcript of the whole recording, then diarization says only who was
	// speaking when.
	//
	// Decoding per SPAN instead cannot represent crosstalk: two concurrent spans
	// are each handed the same audio and the recogniser returns the dominant voice
	// for both, so identical words land in two speakers' mouths — 21 such pairs
	// across three hearings, one of them 176 characters. Subtracting the overlap
	// before decoding fixes the duplication and deletes 26% of a hearing's words,
	// because span edges over-claim (58% of one file looked overlapped at span
	// level against 7.9% measured frame level).
	//
	// Assigning individual words removes the failure mode instead of trading it
	// for another: a word exists once, in one place, attributed once, so
	// duplication is unrepresentable rather than detected-and-repaired. Whole-file
	// context also gives the recogniser more to punctuate and case from than a
	// 3-second turn does.
	var words []TimedWord
	duration := float64(len(samples)) / float64(sampleRate)
	for wi, w := range asrWindows(0, duration) {
		audio := slice(samples, w[0], w[1])
		if len(audio) == 0 {
			continue
		}
		_, toks, times := d.decodeTokens(audio)
		// Windows after the first re-read asrOverlap seconds so a word split across
		// the seam is seen whole by one of them. Keeping both copies would double
		// those words, so the re-read region belongs to the window that owned it
		// first.
		from := w[0]
		if wi > 0 {
			from = w[0] + asrOverlap
		}
		words = append(words, wordsFromTokens(toks, times, w[0], from)...)
	}
	sort.SliceStable(words, func(i, j int) bool { return words[i].Start < words[j].Start })

	var segs []DiarSegment
	if len(words) > 0 {
		assignSpeakers(words, spans)
		smoothRuns(words)
		segs = segmentsFromWords(words)
	} else {
		// The recogniser emitted no timestamps (sherpa's Whisper decoder does not),
		// so a word cannot be placed on the clock and the span is the only unit
		// that can be attributed. Detected from the decode itself rather than
		// assumed from the model type, because the fallback is silent: a wrong
		// guess here yields an empty transcript, not an error.
		for _, sp := range spans {
			for _, w := range asrWindows(sp.start, sp.end) {
				audio := slice(samples, w[0], w[1])
				if len(audio) == 0 {
					continue
				}
				text := d.decode(audio)
				if text == "" {
					continue
				}
				segs = append(segs, DiarSegment{Start: w[0], End: w[1], Speaker: sp.spk, Text: text})
			}
		}
	}

	// Consecutive turns by one speaker read as one turn; the diarizer's split
	// between them was about silence, not about who is talking.
	var out []DiarSegment
	for _, sg := range segs {
		if n := len(out); n > 0 && out[n-1].Speaker == sg.Speaker {
			out[n-1].End = sg.End
			out[n-1].Text += " " + sg.Text
			continue
		}
		out = append(out, sg)
	}
	var full strings.Builder
	for i, sg := range out {
		if i > 0 {
			full.WriteString(" ")
		}
		full.WriteString(sg.Text)
	}

	return DiarResult{
		Text:     strings.TrimSpace(full.String()),
		Duration: float64(len(samples)) / float64(sampleRate),
		Segments: out,
		Speakers: voices,
		Words:    words,
	}
}

// wordsFromTokens groups a recogniser's sub-word tokens into whole words on the
// recording's clock, dropping any that start before `from`.
//
// sherpa emits BPE pieces where a LEADING SPACE marks the start of a new word
// ("you", "'", "re" is one word, not three). Grouping on that boundary is what
// makes a word — not a token — the unit that gets a speaker, so an apostrophe is
// never attributed to someone else.
//
// The token's own spelling is kept: Parakeet emits cased, punctuated text and
// that IS the transcript. Only the speaker lookup is done on time.
func wordsFromTokens(tokens []string, times []float32, offset, from float64) []TimedWord {
	var out []TimedWord
	for i, tok := range tokens {
		if tok == "" {
			continue
		}
		start := offset + float64(times[i])
		if strings.HasPrefix(tok, " ") || len(out) == 0 {
			out = append(out, TimedWord{Text: strings.TrimSpace(tok), Start: start, Speaker: -1})
			continue
		}
		out[len(out)-1].Text += tok
	}
	kept := out[:0]
	for _, w := range out {
		if strings.TrimSpace(w.Text) != "" && w.Start >= from {
			kept = append(kept, w)
		}
	}
	return kept
}

// assignSpeakers gives each word the speaker who was talking at that moment.
//
// Where spans overlap — real crosstalk — the recogniser returned ONE voice and
// there is no way to know whose from the text alone, so the word goes to the
// span whose centre is nearest. That is a guess, and it is why segments carry
// `overlap`: one attribution that is flagged beats two that are certain and
// contradictory. A word inside no span at all takes the nearest span rather than
// being dropped, since the recogniser heard speech the segmenter missed.
func assignSpeakers(words []TimedWord, spans []span) {
	if len(spans) == 0 {
		return
	}
	for i := range words {
		t := words[i].Start
		best, bestDist := -1, math.Inf(1)
		for _, sp := range spans {
			var d float64
			switch {
			case t < sp.start:
				d = sp.start - t
			case t > sp.end:
				d = t - sp.end
			default:
				// Inside this span. Rank by distance from its centre so that, among
				// several overlapping spans, the one this word sits deepest inside
				// wins.
				d = -1 * (math.Min(t-sp.start, sp.end-t) + 1)
			}
			if d < bestDist {
				best, bestDist = sp.spk, d
			}
		}
		words[i].Speaker = best
	}
}

// maxBlipWords / maxBlipSeconds bound what counts as a speaker "blip" — a run too
// short to be a real turn. pyannote is configured with min_duration_on 0.3s, so
// anything at or under a second flanked by one speaker is a segmentation wobble,
// not someone interjecting a word and falling silent.
const maxBlipWords = 2
const maxBlipSeconds = 1.0

// smoothRuns absorbs A-B-A blips into A.
//
// Only same-speaker-on-both-sides runs are touched, because that is the one
// pattern that cannot be a real turn exchange: a genuine interjection leaves the
// floor with someone else, so the flanks differ. A one-word flip between two
// speakers is instead the boundary between their spans landing mid-word.
//
// Deliberately NOT a general smoother. A longer misassigned run is diarization
// being wrong about who spoke, and papering over that here would hide the error
// while making the transcript no more correct.
func smoothRuns(words []TimedWord) {
	for i := 0; i < len(words); {
		j := i
		for j < len(words) && words[j].Speaker == words[i].Speaker {
			j++
		}
		blip := j-i <= maxBlipWords && (j >= len(words) || words[j-1].Start-words[i].Start <= maxBlipSeconds)
		if blip && i > 0 && j < len(words) && words[i-1].Speaker == words[j].Speaker {
			for k := i; k < j; k++ {
				words[k].Speaker = words[i-1].Speaker
			}
		}
		i = j
	}
}

// segmentsFromWords groups consecutive words into one segment per speaker RUN. A
// segment now ends where the speaker changes, which is what a turn is — the
// diarizer's own span boundaries were about silence, not about who is talking.
func segmentsFromWords(words []TimedWord) []DiarSegment {
	var out []DiarSegment
	for _, w := range words {
		if n := len(out); n > 0 && out[n-1].Speaker == w.Speaker {
			out[n-1].Text += " " + w.Text
			out[n-1].End = w.Start
			continue
		}
		if n := len(out); n > 0 && out[n-1].End < w.Start {
			out[n-1].End = w.Start
		}
		out = append(out, DiarSegment{Start: w.Start, End: w.Start, Speaker: w.Speaker, Text: w.Text})
	}
	return out
}

// NormalizeWord reduces a token to what two recognisers can agree on: letters,
// digits and the apostrophe. Case and punctuation are exactly what the two
// disagree about, so matching on them would find almost nothing.
func NormalizeWord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '\'' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// span is one diarization output: a time range attributed to a local cluster id.
type span struct {
	start, end float64
	spk        int
}

// minCleanSpeech is how much crosstalk-free audio a speaker needs before the
// clean-only voiceprint is trusted. Below it the sample is too thin to describe
// a voice, and the full span — blended but longer — is the better of two bad
// options.
const minCleanSpeech = 3.0

// voiceprints builds one embedding per speaker, ordered by local id, from that
// speaker's CROSSTALK-FREE audio wherever there is enough of it.
//
// Embedding over everything was measurably wrong: in one 14-minute hearing the
// resulting prints put every pair of voices between 0.79 and 0.98 cosine, with a
// known-DIFFERENT pair at 0.876 above a known-SAME pair at 0.794 — no threshold
// could separate them. An embedder handed two simultaneous voices describes
// neither.
//
// The overlap is SUBTRACTED from each span rather than disqualifying it. That
// distinction is the whole value: discarding any span touched by another speaker
// threw away a 13.8s turn because of a 0.4s interjection, leaving the dominant
// speaker 168s of clean audio out of 564s. Subtracting the interjection instead
// keeps 448s. Same intent, 2.7x the evidence.
//
// (pyannote's own frame-level output would find ~501s, because sherpa's spans
// over-claim at their edges. Closing that last gap needs a second inference pass
// over the segmentation model, which is not worth a new ONNX dependency here.)
func (d *Diarizer) voiceprints(samples []float32, spans []span) []SpeakerVoice {
	clean := map[int][]float32{}
	all := map[int][]float32{}
	cleanSecs := map[int]float64{}
	totalSecs := map[int]float64{}
	for i, s := range spans {
		all[s.spk] = append(all[s.spk], slice(samples, s.start, s.end)...)
		totalSecs[s.spk] += s.end - s.start
		for _, iv := range cleanParts(spans, i) {
			clean[s.spk] = append(clean[s.spk], slice(samples, iv[0], iv[1])...)
			cleanSecs[s.spk] += iv[1] - iv[0]
		}
	}
	locals := make([]int, 0, len(all))
	for k := range all {
		locals = append(locals, k)
	}
	sort.Ints(locals)
	voices := make([]SpeakerVoice, 0, len(locals))
	for _, k := range locals {
		src, blended := clean[k], false
		if cleanSecs[k] < minCleanSpeech || len(src) == 0 {
			src, blended = all[k], true
		}
		voices = append(voices, SpeakerVoice{
			Local:        k,
			Embedding:    d.embed(src),
			CleanSeconds: cleanSecs[k],
			TotalSeconds: totalSecs[k],
			Blended:      blended,
		})
	}
	return voices
}

// minCleanPart drops slivers left behind by subtraction. A tenth of a second
// carries no speaker information and only adds boundary artifacts to the
// concatenation.
const minCleanPart = 0.25

// Subtract returns the parts of [start,end) that none of `others` cover, dropping
// any remainder shorter than minPart. `others` need not be sorted or disjoint and
// may extend past the interval.
//
// Exported because BOTH halves of the diarize pipeline need the same notion of
// "audio only this speaker occupies": voiceprints subtract crosstalk before
// embedding, and the server subtracts it before re-transcribing. Two copies of
// this arithmetic would be two definitions of clean, free to drift apart — and
// the pipeline is only coherent if the span a print describes is the span the
// text came from.
func Subtract(start, end float64, others [][2]float64, minPart float64) [][2]float64 {
	clipped := make([][2]float64, 0, len(others))
	for _, o := range others {
		lo, hi := math.Max(o[0], start), math.Min(o[1], end)
		if lo < hi {
			clipped = append(clipped, [2]float64{lo, hi})
		}
	}
	sort.Slice(clipped, func(x, y int) bool { return clipped[x][0] < clipped[y][0] })

	var out [][2]float64
	cur := start
	for _, o := range clipped {
		if o[0] > cur && o[0]-cur >= minPart {
			out = append(out, [2]float64{cur, o[0]})
		}
		if o[1] > cur {
			cur = o[1]
		}
	}
	if end > cur && end-cur >= minPart {
		out = append(out, [2]float64{cur, end})
	}
	return out
}

// cleanParts returns the sub-intervals of spans[i] that no OTHER speaker's span
// covers. Same-speaker spans are not subtracted — a speaker segmented finely
// does not overlap themselves.
func cleanParts(spans []span, i int) [][2]float64 {
	a := spans[i]
	others := make([][2]float64, 0, len(spans))
	for j, b := range spans {
		if i == j || b.spk == a.spk {
			continue
		}
		others = append(others, [2]float64{b.start, b.end})
	}
	return Subtract(a.start, a.end, others, minCleanPart)
}

// AdaptiveMergeThreshold is the cosine at which two clusters are treated as one
// speaker, raised for recordings where a false merge is more likely.
//
// A fixed threshold cannot serve both ends of the range. At 0.8 a 12-minute,
// two-speaker hearing merged correctly, while a 44-minute hearing with five-plus
// speakers collapsed into a single cluster holding 2525 of its 2650 seconds —
// unusable, and strictly worse than the over-splitting merging exists to fix.
//
// Two things drive the risk and both are known before merging starts:
//
//   - CLUSTER COUNT. n clusters give n(n-1)/2 chances for some pair to cross the
//     bar, so the count grows quadratically while the bar stays still.
//   - DURATION. Longer recordings hold more acoustic variety per speaker — mic
//     distance, leaning in and back, room noise — which spreads each voice's
//     embeddings and pushes different speakers' spreads into contact.
//
// Both enter logarithmically: doubling either should cost a fixed increment, not
// a proportional one. Capped at 0.95, above which nothing merges and the setting
// is just an expensive way of being off; floored at the old 0.8 so no recording
// merges more eagerly than before.
func AdaptiveMergeThreshold(clusters int, seconds float64) float32 {
	if clusters < 2 {
		clusters = 2
	}
	mins := seconds / 60
	if mins < 10 {
		mins = 10
	}
	thr := 0.80 + 0.03*math.Log2(float64(clusters)) + 0.01*math.Log2(mins/10)
	if thr < 0.80 {
		thr = 0.80
	}
	if thr > 0.95 {
		thr = 0.95
	}
	return float32(thr)
}

// mergeGroups unions local clusters whose voiceprints are all within thr cosine
// of each other, returning old local id → canonical (lowest) id in its group. It
// returns nil when thr ≤ 0 or nothing merged, so the caller can skip the rework.
//
// COMPLETE linkage: every pair in a group must clear thr. Single linkage merged
// A and C whenever some B sat near both, and one such bridge is enough to chain
// unrelated people together — the failure that turned a 44-minute hearing into
// one speaker. A bridge cluster is common precisely where merging is tempting:
// it is usually a mixed cluster, sitting between two real voices because it
// contains both.
func mergeGroups(voices []SpeakerVoice, thr float32) map[int]int {
	if thr <= 0 || len(voices) < 2 {
		return nil
	}
	groups := make([][]int, len(voices))
	emb := make(map[int][]float32, len(voices))
	for i, v := range voices {
		groups[i] = []int{v.Local}
		emb[v.Local] = v.Embedding
	}
	// Greedy: repeatedly take the closest pair of groups whose WORST cross-pair
	// still clears thr. Closest-first keeps the outcome independent of input
	// order, which single linkage was not.
	for {
		bestWorst, bi, bj := float32(-2), -1, -1
		for x := 0; x < len(groups); x++ {
			for y := x + 1; y < len(groups); y++ {
				worst := float32(2)
				for _, a := range groups[x] {
					for _, b := range groups[y] {
						if c := Cosine(emb[a], emb[b]); c < worst {
							worst = c
						}
					}
				}
				if worst >= thr && worst > bestWorst {
					bestWorst, bi, bj = worst, x, y
				}
			}
		}
		if bi < 0 {
			break
		}
		groups[bi] = append(groups[bi], groups[bj]...)
		groups = append(groups[:bj], groups[bj+1:]...)
	}

	remap := make(map[int]int, len(voices))
	merged := false
	for _, g := range groups {
		canon := g[0]
		for _, id := range g {
			if id < canon {
				canon = id
			}
		}
		for _, id := range g {
			remap[id] = canon
			if id != canon {
				merged = true
			}
		}
	}
	if !merged {
		return nil
	}
	return remap
}

// Cosine is the similarity between two voiceprints; 0 when either is missing or
// degenerate, so a failed embedding never reads as a perfect match.
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// maxASRWindow is a backstop, not the mechanism. Turns are the decode unit and
// they are short — nothing on the reference hearing came close — but a single
// speaker CAN hold the floor for a very long time, and one unbounded
// AcceptWaveform is exactly the shape that killed the embedding path. Five
// minutes is far above any real turn and still bounds the pathological case.
const maxASRWindow = 300.0

// asrOverlap applies only when a turn is long enough to be sub-split, so a word
// on the cut is still heard whole by one side.
const asrOverlap = 0.5

func asrWindows(start, end float64) [][2]float64 {
	if end <= start {
		return nil
	}
	var out [][2]float64
	for t := start; t < end; t += maxASRWindow {
		e := t + maxASRWindow
		if e > end {
			e = end
		}
		s := t
		if s > start {
			s -= asrOverlap
		}
		out = append(out, [2]float64{s, e})
	}
	return out
}

// minDecodeSamples is the shortest clip worth handing to the recognizer. A
// diarization span can be a fraction of a second — a cough, a cut-off word — and
// sherpa is not defensive about it: AcceptWaveform indexes samples[0], and for a
// clip too short to yield anything GetResult returns nil, which the caller then
// dereferences. `stt.go` already guards the empty case for the same reason.
const minDecodeSamples = sampleRate / 4 // 250ms

// decode runs one bounded ASR pass.
//
// The defer is safe because this is its own function: the stream is freed when
// decode returns, once per turn. Deferring inside the caller's loop instead would
// hold every stream until the whole request finished.
func (d *Diarizer) decode(audio []float32) string {
	text, _, _ := d.decodeTokens(audio)
	return text
}

// decodeTokens is decode plus the token stream and each token's offset from the
// START OF THE SUPPLIED AUDIO — the caller adds the window's own start to put
// them on the recording's clock.
//
// The transducer is the only recogniser here that emits timestamps at all
// (sherpa's Whisper decoder returns none), which is what makes it the timeline
// the better-worded Whisper text is aligned onto.
func (d *Diarizer) decodeTokens(audio []float32) (string, []string, []float32) {
	if len(audio) < minDecodeSamples {
		return "", nil, nil
	}
	st := sherpa.NewOfflineStream(d.asr)
	defer sherpa.DeleteOfflineStream(st)
	st.AcceptWaveform(sampleRate, audio)
	d.asr.Decode(st)
	r := st.GetResult()
	if r == nil {
		return "", nil, nil
	}
	// Only pair them up when the model actually supplied both; a length mismatch
	// means the tokens are not the timestamps' tokens and aligning to them would
	// silently shift every word.
	if len(r.Tokens) != len(r.Timestamps) {
		return strings.TrimSpace(r.Text), nil, nil
	}
	return strings.TrimSpace(r.Text), r.Tokens, r.Timestamps
}

// maxEmbedWindow bounds one call into the speaker-embedding model.
//
// This is where the OOM was. A speaker-embedding model expects an utterance of
// seconds; `voiceprints` was handing it a speaker's ENTIRE concatenated audio.
// Unmerged that was tolerable — the largest cluster in a 14-minute hearing was
// about 290s — but `mergeGroups` re-embeds each merged group, so a low
// merge_threshold collapsed seven clusters into one and fed ~840s into a single
// stream: 397GB virtual, 119GB resident, killed by the kernel.
//
// Thirty seconds is well past what these models need to identify a voice, and it
// makes peak allocation independent of how long anyone talks.
const maxEmbedWindow = 30 * sampleRate

// embed produces one voiceprint, averaging over bounded windows when the input is
// long.
//
// Averaging is not only the memory fix — it is the better voiceprint. One
// enormous utterance lets a single noisy stretch dominate; the mean of several
// windows is what speaker-ID systems normally use, and it is what keeps a
// merged cluster's print comparable to an unmerged one's.
func (d *Diarizer) embed(samples []float32) []float32 {
	if len(samples) == 0 {
		return nil
	}
	if len(samples) <= maxEmbedWindow {
		return d.embedOne(samples)
	}
	var sum []float32
	var n int
	for i := 0; i < len(samples); i += maxEmbedWindow {
		j := i + maxEmbedWindow
		if j > len(samples) {
			j = len(samples)
		}
		// A trailing sliver is too short to embed meaningfully and would drag the
		// mean toward noise.
		if j-i < sampleRate {
			break
		}
		v := d.embedOne(samples[i:j])
		if len(v) == 0 {
			continue
		}
		if sum == nil {
			sum = make([]float32, len(v))
		}
		if len(v) != len(sum) {
			continue
		}
		for k := range v {
			sum[k] += v[k]
		}
		n++
	}
	if n == 0 {
		return d.embedOne(samples[:maxEmbedWindow])
	}
	for k := range sum {
		sum[k] /= float32(n)
	}
	return l2norm(sum)
}

// l2norm keeps the averaged print on the unit sphere, so Cosine against a
// single-window print stays comparable.
func l2norm(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}

func (d *Diarizer) embedOne(samples []float32) []float32 {
	st := d.emb.CreateStream()
	defer sherpa.DeleteOnlineStream(st)
	st.AcceptWaveform(sampleRate, samples)
	st.InputFinished()
	return d.emb.Compute(st)
}

func slice(samples []float32, start, end float64) []float32 {
	a, b := int(start*sampleRate), int(end*sampleRate)
	if a < 0 {
		a = 0
	}
	if b > len(samples) {
		b = len(samples)
	}
	if a >= b {
		return nil
	}
	return samples[a:b]
}

func orDefaultF(v, d float32) float32 {
	if v > 0 {
		return v
	}
	return d
}

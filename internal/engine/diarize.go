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
}

// DiarSegment is one aligned, speaker-attributed run of text.
type DiarSegment struct {
	Start, End float64
	Speaker    int // local cluster id within this request
	Text       string
}

// SpeakerVoice is a detected speaker's voiceprint for this request.
type SpeakerVoice struct {
	Local     int
	Embedding []float32
}

type DiarResult struct {
	Text     string
	Duration float64
	Segments []DiarSegment
	Speakers []SpeakerVoice
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
	return &Diarizer{asr: asr, sd: sd, emb: ex, lang: lang}, nil
}

func (d *Diarizer) Language() string { return d.lang }

// Process diarizes + transcribes the whole clip (16 kHz mono).
func (d *Diarizer) Process(samples []float32) DiarResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 1. diarization → speaker time spans
	diar := d.sd.Process(samples)
	type span struct {
		start, end float64
		spk        int
	}
	spans := make([]span, len(diar))
	for i, s := range diar {
		spans[i] = span{float64(s.Start), float64(s.End), s.Speaker}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	spkAt := func(t float64) int {
		for _, s := range spans {
			if t >= s.start && t <= s.end {
				return s.spk
			}
		}
		best, bestDist := -1, math.MaxFloat64
		for _, s := range spans {
			if dst := math.Abs((s.start+s.end)/2 - t); dst < bestDist {
				bestDist, best = dst, s.spk
			}
		}
		return best
	}

	// 2. ASR → tokens + per-token timestamps
	st := sherpa.NewOfflineStream(d.asr)
	defer sherpa.DeleteOfflineStream(st)
	st.AcceptWaveform(sampleRate, samples)
	d.asr.Decode(st)
	r := st.GetResult()

	// 3. align tokens to speakers, group consecutive same-speaker runs
	var segs []DiarSegment
	var cur *DiarSegment
	for i, tok := range r.Tokens {
		var t float64
		if i < len(r.Timestamps) {
			t = float64(r.Timestamps[i])
		}
		spk := spkAt(t)
		piece := strings.ReplaceAll(tok, "▁", " ")
		if cur != nil && cur.Speaker == spk {
			cur.End = t
			cur.Text += piece
		} else {
			if cur != nil {
				segs = append(segs, *cur)
			}
			cur = &DiarSegment{Start: t, End: t, Speaker: spk, Text: piece}
		}
	}
	if cur != nil {
		segs = append(segs, *cur)
	}
	out := segs[:0]
	for _, sg := range segs {
		sg.Text = strings.TrimSpace(sg.Text)
		if sg.Text != "" {
			out = append(out, sg)
		}
	}

	// 4. one voiceprint per speaker (embed that speaker's concatenated audio)
	bySpk := map[int][]float32{}
	for _, s := range spans {
		bySpk[s.spk] = append(bySpk[s.spk], slice(samples, s.start, s.end)...)
	}
	locals := make([]int, 0, len(bySpk))
	for k := range bySpk {
		locals = append(locals, k)
	}
	sort.Ints(locals)
	voices := make([]SpeakerVoice, 0, len(locals))
	for _, k := range locals {
		voices = append(voices, SpeakerVoice{Local: k, Embedding: d.embed(bySpk[k])})
	}

	return DiarResult{
		Text:     strings.TrimSpace(r.Text),
		Duration: float64(len(samples)) / float64(sampleRate),
		Segments: out,
		Speakers: voices,
	}
}

func (d *Diarizer) embed(samples []float32) []float32 {
	if len(samples) == 0 {
		return nil
	}
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

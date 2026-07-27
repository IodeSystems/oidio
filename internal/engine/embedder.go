package engine

import (
	"fmt"

	"github.com/iodesystems/oidio/internal/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Embedder is the speaker-embedding model on its own, without diarization or
// ASR alongside it.
//
// The Diarizer holds one of these internally, but review needs to embed
// arbitrary spans of an ALREADY diarized recording — asking it to construct a
// segmentation model and a 650 MB recogniser to do that would make a review cost
// as much as the transcription it is checking.
type Embedder struct {
	ex *sherpa.SpeakerEmbeddingExtractor
}

func NewEmbedder(spec config.ModelSpec) (*Embedder, error) {
	if spec.Embedding == "" {
		return nil, fmt.Errorf("embedder needs the `embedding` model path")
	}
	c := sherpa.SpeakerEmbeddingExtractorConfig{
		Model:      spec.Embedding,
		NumThreads: reserveCore(orDefault(spec.NumThreads, 4)),
		Provider:   "cpu",
	}
	var ex *sherpa.SpeakerEmbeddingExtractor
	withNice(spec.Nice, func() { ex = sherpa.NewSpeakerEmbeddingExtractor(&c) })
	if ex == nil {
		return nil, fmt.Errorf("failed to init speaker embedding extractor (check %s)", spec.Embedding)
	}
	return &Embedder{ex: ex}, nil
}

// Embed returns the voiceprint for one span of audio, or nil if the span is too
// short to produce one.
func (e *Embedder) Embed(samples []float32, rate int) []float32 {
	if len(samples) < rate/4 {
		return nil
	}
	st := e.ex.CreateStream()
	defer sherpa.DeleteOnlineStream(st)
	st.AcceptWaveform(rate, samples)
	st.InputFinished()
	if !e.ex.IsReady(st) {
		return nil
	}
	return e.ex.Compute(st)
}

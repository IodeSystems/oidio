package engine

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/iodesystems/oidio/internal/config"
)

// Engine tests need real sherpa models; point OIDIO_TEST_MODELS at a dir holding
// them (the sherpa-diarize models layout) to run. They hammer the engines from
// many goroutines under -race to prove the concurrency guards hold (no crash, no
// result drift, no data race).
func modelsDir(t *testing.T) string {
	d := os.Getenv("OIDIO_TEST_MODELS")
	if d == "" {
		t.Skip("set OIDIO_TEST_MODELS to run engine concurrency tests")
	}
	return d
}

func TestSTTConcurrent(t *testing.T) {
	asr := filepath.Join(modelsDir(t), "sherpa-onnx-zipformer-gigaspeech-2023-12-12")
	spec := config.ModelSpec{
		Encoder:    filepath.Join(asr, "encoder-epoch-30-avg-1.int8.onnx"),
		Decoder:    filepath.Join(asr, "decoder-epoch-30-avg-1.int8.onnx"),
		Joiner:     filepath.Join(asr, "joiner-epoch-30-avg-1.int8.onnx"),
		Tokens:     filepath.Join(asr, "tokens.txt"),
		NumThreads: 2,
	}
	if _, err := os.Stat(spec.Encoder); err != nil {
		t.Skipf("gigaspeech model not present: %v", err)
	}
	stt, err := NewSTT(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer stt.Close()

	samples := make([]float32, 16000)
	for i := range samples {
		samples[i] = float32(i%200)/200 - 0.5
	}
	want := stt.Transcribe(samples, 16000).Text

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if got := stt.Transcribe(samples, 16000).Text; got != want {
					t.Errorf("concurrent STT drift: %q vs %q", got, want)
				}
			}
		}()
	}
	wg.Wait()
}

func TestTTSConcurrent(t *testing.T) {
	k := filepath.Join(modelsDir(t), "kokoro-int8-multi-lang-v1_0")
	spec := config.ModelSpec{
		Type:          "tts",
		KokoroModel:   filepath.Join(k, "model.int8.onnx"),
		KokoroVoices:  filepath.Join(k, "voices.bin"),
		KokoroTokens:  filepath.Join(k, "tokens.txt"),
		KokoroDataDir: filepath.Join(k, "espeak-ng-data"),
		KokoroLexicon: filepath.Join(k, "lexicon-us-en.txt"),
		NumThreads:    2,
	}
	if _, err := os.Stat(spec.KokoroModel); err != nil {
		t.Skipf("kokoro model not present: %v", err)
	}
	tts, err := NewTTS(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := len(tts.Synthesize("hello world from oidio", 0, 1))
	if want == 0 {
		t.Fatal("synthesis produced no audio")
	}

	// The guard serializes Generate (so -race stays clean and nothing crashes), but
	// the output length still varies slightly run-to-run: onnxruntime with
	// num_threads>1 sums parallel reductions in nondeterministic order, nudging the
	// waveform and its silence trim. Assert "sane + close", not byte-identical.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				got := len(tts.Synthesize("hello world from oidio", 0, 1))
				if got == 0 || got < want*9/10 || got > want*11/10 {
					t.Errorf("concurrent TTS: %d samples, want ~%d (+/-10%%)", got, want)
				}
			}
		}()
	}
	wg.Wait()
}

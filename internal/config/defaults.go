package config

// The default roster: what `oidio` serves when nothing configures it.
//
// oidio previously could not start without a config naming absolute paths to
// files someone had downloaded by hand, which made "run oidio" a documentation
// exercise rather than a command. Every model it needs is published on the hub,
// so the defaults name hub bundles and the files inside them; the hf cache
// fetches whatever is missing on first start.
//
// These are the four surfaces oidio serves, and the choice of model for each is
// not arbitrary — see the notes on each entry. Overriding one in a config file
// replaces only that model, so a host that wants a different recogniser does not
// have to restate the other three.
const (
	// bundleWhisper is cased and punctuated, but has a 30-second receptive
	// field: it transcribes only the first window of a long file. Good for
	// short-form batch STT, wrong for a recording.
	bundleWhisper = "csukuangfj/sherpa-onnx-whisper-base.en"

	// bundleParakeet (NeMo Parakeet-TDT) is cased, punctuated AND timestamped
	// from one model, which is what makes word-level speaker attribution
	// possible without a second recogniser. It replaced a zipformer/gigaspeech
	// transducer whose output was ALL CAPS and unpunctuated.
	bundleParakeet = "csukuangfj/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8"

	// bundleSegmentation + bundleEmbedding are the diarization pair: pyannote
	// finds the turns, wespeaker gives each a voiceprint to cluster.
	bundleSegmentation = "csukuangfj/sherpa-onnx-pyannote-segmentation-3-0"
	// One repo holding many embedding models, so this one names a file inside
	// it rather than being a model repo of its own.
	bundleEmbedding = "csukuangfj/speaker-embedding-models"

	// bundleKokoro is the TTS voice bank.
	bundleKokoro = "csukuangfj/kokoro-int8-multi-lang-v1_0"

	// bundleStreamingZipformer is a STREAMING transducer — a different class of
	// model from the offline ones above, and the only one of these that can emit
	// partial results as audio arrives. Its text is ALL CAPS and unpunctuated;
	// that is the cost of streaming.
	bundleStreamingZipformer = "csukuangfj/sherpa-onnx-streaming-zipformer-en-2023-06-26"
)

// Default returns the built-in roster. A fresh Config every call: callers merge
// a user config over it and must not share mutable state between them.
func Default() *Config {
	return &Config{
		Models: map[string]ModelSpec{
			"stt": {
				Type:    "whisper",
				Bundle:  bundleWhisper,
				Encoder: "base.en-encoder.int8.onnx",
				Decoder: "base.en-decoder.int8.onnx",
				Tokens:  "base.en-tokens.txt",
				// The CPU-bound engines run niced so inference cannot starve the
				// HTTP server into 503s; see ModelSpec.Nice.
				NumThreads: 4,
				Nice:       5,
			},
			"stt-diarize": {
				Type:      "diarize",
				Bundle:    bundleParakeet,
				ModelType: "nemo_transducer",
				Encoder:   "encoder.int8.onnx",
				Decoder:   "decoder.int8.onnx",
				Joiner:    "joiner.int8.onnx",
				Tokens:    "tokens.txt",
				// Segmentation and embedding live in their OWN bundles, so they
				// carry an explicit bundle prefix rather than resolving against
				// this model's.
				Segmentation:     bundleSegmentation + ":model.onnx",
				Embedding:        bundleEmbedding + ":wespeaker_en_voxceleb_CAM++.onnx",
				ClusterThreshold: 0.7,
				NumThreads:       4,
				Nice:             10, // the CPU-heaviest path
			},
			"tts": {
				Type:          "tts",
				Bundle:        bundleKokoro,
				KokoroModel:   "model.int8.onnx",
				KokoroVoices:  "voices.bin",
				KokoroTokens:  "tokens.txt",
				KokoroDataDir: "espeak-ng-data",
				KokoroLexicon: "lexicon-us-en.txt",
				DefaultVoice:  "af_heart",
				Voices: map[string]int{
					"af_heart": 3, "af_bella": 2, "af_nova": 7,
					"am_adam": 11, "am_echo": 12, "am_michael": 16, "am_onyx": 17,
					"alloy": 0, "echo": 12, "fable": 1, "onyx": 17, "nova": 7, "shimmer": 10,
				},
				NumThreads: 4,
				Nice:       5,
			},
			"realtime-stt": {
				Type:    "realtime",
				Bundle:  bundleStreamingZipformer,
				Encoder: "encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx",
				Decoder: "decoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx",
				Joiner:  "joiner-epoch-99-avg-1-chunk-16-left-128.int8.onnx",
				Tokens:  "tokens.txt",
				// OpenAI Realtime clients send 24 kHz PCM; sherpa resamples.
				InputSampleRate: 24000,
				// Raised from sherpa's 0.8 default so a natural mid-sentence
				// pause does not cut an utterance short.
				Rule2Silence:      2.0,
				SpokenPunctuation: true,
				NumThreads:        4,
				Nice:              5,
			},
		},
	}
}

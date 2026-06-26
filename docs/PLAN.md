# oidio — plan

OpenAI-compatible audio server in Go over sherpa-onnx. Standard-first; deviate
only where OpenAI has no equivalent, and only additively.

## Slices

- ✅ **S1 — Batch STT.** `POST /v1/audio/transcriptions` (+`/translations`) over an
  offline transducer. `response_format`: json, verbose_json, text, srt, vtt.
  `stream=true` → OpenAI SSE (`transcript.text.delta` … `transcript.text.done`).
  ffmpeg decode → 16 kHz mono. Config-driven model registry. Validated end-to-end
  against the gigaspeech int8 zipformer.
  - **next**: pseudo-segments from token timestamps (currently one whole-clip
    segment); whisper/sense-voice model types; honest `translate` task wiring.
- ✅ **S2 — Diarization** (`type: diarize`). Done + validated. Offline
  `OfflineSpeakerDiarization` (pyannote segmentation + speaker embedding +
  clustering) + the transducer ASR, aligned by token timestamp → speaker-labeled
  segments. **Stateless identity:** each speaker is a UUID (matched to a
  caller-supplied `known_speakers` above `speaker_confidence`, else minted); the
  response carries each speaker's 512-d voiceprint `embedding` and a `similarity`
  object (cosine to the others). No server-side catalog; names live at the caller.
  Request args (additive multipart fields): `speaker_confidence`, `known_speakers`
  (JSON `[{uuid,embedding}]`). Validated on the real 2-speaker EN clips — correct
  count, and a passed-back voiceprint reuses its UUID.
  - **next**: per-request `num_speakers`/clustering override (currently model
    config); pseudo-segments for the plain STT path.
  - **risk**: auto speaker-count is threshold-sensitive (`cluster_threshold`
    default 0.7); pass a known count when possible.
- ✅ **S3 — TTS** (`type: tts`). Done + validated. `POST /v1/audio/speech` via
  Kokoro (`OfflineTts`) → audio bytes. `response_format` mp3/opus/aac/flac (ffmpeg)
  + wav/pcm (native); `voice` resolves through a config name→sid map (or a bare
  integer sid), `speed` honored. Validated: valid mp3 + wav out, distinct voices.
  - **next**: stream chunks (`GenerateWithCallback`) for lower latency; expose the
    full Kokoro voice catalog.
- ◻ **S4 — Realtime STT** (`type: realtime`). `GET /v1/realtime` WebSocket, OpenAI
  Realtime transcription schema, over `OnlineRecognizer` + VAD endpointing. Live
  `…delta`/`…completed` events.
- ◻ **S5 — WebRTC** transport for realtime (pion). Optional; lower-latency live
  mic with built-in jitter/echo handling. Heavier (ICE/DTLS/SDP).
- ◻ **S6 — `/v1/capabilities`** discovery manifest (mirror corrallm's), advertising
  per-model surfaces.

## Speaker / diarization design (decided)

oidio is **stateless about identity**. There is deliberately **no speaker manager
and no named enrollment catalog** — that primitive exists in sherpa-onnx, but
storing names server-side is the wrong layer.

Per diarized request:

- **Threshold is an argument.** The auto-match confidence is a request parameter,
  not server config.
- **Speakers are UUIDs.** Each distinct speaker gets a UUID, stable within the
  request. Each `verbose_json` segment carries `speaker: "<uuid>"`.
- **Result carries the voiceprints + similarities.** A top-level `speakers` array:
  ```jsonc
  "speakers": [
    { "uuid": "…",
      "embedding": [ /* float32 voiceprint */ ],
      "similarity": { "<other-uuid>": 0.42, … } }   // cosine to other speakers
  ]
  ```
  The `embedding` lets a consumer persist a voiceprint and re-match later; the
  `similarity` object exposes the raw matching evidence so the consumer decides.
- **Cross-call stability is consumer-driven.** A request may include
  `known_speakers: [{uuid, embedding}]` (the consumer's own gallery). oidio reuses
  a known UUID when a segment matches above the threshold, else mints a new one.
- **Names live at the consumer.** Any real-name ↔ UUID mapping is the API
  consumer's responsibility, in their system. oidio never sees or stores a name.

Implementation: `SpeakerEmbeddingExtractor` per speaker/segment + cosine
similarity computed directly (no `SpeakerEmbeddingManager` persistent store);
`google/uuid` for IDs.

## Conventions

- Standard OpenAI shapes first; extensions additive (`speaker` on segments, the
  `speakers` array, the catalog-free design) — plain clients keep working.
- One process, many models, dispatch on the `model` field.
- CPU-only, int8 models. cgo + bundled sherpa lib; keep it a standalone binary
  (fault isolation; corrallm stays CGO-free and just proxies it).

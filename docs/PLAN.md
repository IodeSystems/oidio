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
- ✅ **S4 — Realtime STT** (`type: realtime`). Done + validated. `GET /v1/realtime`
  WebSocket in the OpenAI Realtime transcription schema, over a streaming
  `OnlineRecognizer` with built-in silence endpointing. Client streams base64
  PCM16 (`input_audio_buffer.append`, resampled from `input_sample_rate`); server
  emits `conversation.item.input_audio_transcription.delta` (incremental) and
  `.completed` (per endpoint / on `.commit`). Validated: session.created → live
  deltas → correct final transcript.
  - **next**: tune multi-utterance segmentation (`rule2_silence`); optional
    per-session model select from `session.update`.
- ✅ **S5 — WebRTC** transport for realtime (pion). Done + validated. `POST
  /v1/realtime` with an `application/sdp` offer → SDP answer (non-trickle, gathered
  ICE). The client's Opus mic track is decoded (pion/opus → 48 kHz mono) into the
  same streaming recognizer as the WS path; transcript events ride the `oai-events`
  data channel (`session.created/updated`, `…delta`, `…completed`; `commit`
  finalizes). Validated with a pion client streaming ogg-opus: ICE connected →
  30 deltas → 2 accurate final transcripts.
  - **next**: TURN config for non-LAN peers; bundle a browser WebRTC example.
- ✅ **S6 — `/v1/capabilities`** discovery manifest. Done. Public JSON listing each
  model (id, type, capability) with per-type metadata — realtime: transports
  [websocket, webrtc]; diarize: diarization flag; tts: voices + formats — plus the
  endpoints, models_by_capability, and example requests (curl/ws/webrtc). Mirrors
  corrallm's manifest so a client/LLM discovers oidio's surfaces.

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

## Icebox (deferred, opt-in)

- **audio.cpp ([0xShug0/audio.cpp](https://github.com/0xShug0/audio.cpp)) as a GPU
  engine path.** Pure-C++/GGML inference with its own OpenAI-compatible server,
  CUDA-optimized (the box has an RTX 5090; oidio is CPU-only). v0.1 (Jun 2026),
  C++-only (no Go bindings), GGUF + broad streaming not done yet — too early to
  bind into oidio. **Not for TTS** (Kokoro/generic solutions are fine — no
  interest). What's actually compelling:
  - **Streaming diarization (Sortformer).** sherpa diarization is offline-only;
    Sortformer is a streaming model → the realtime-diarization we shelved becomes
    possible. **Synthesis worth noting:** run oidio's stateless `resolveSpeakers`
    identity layer *on top of* Sortformer's live segments → **realtime speaker
    UUIDs**, which neither tool does alone.
  - GPU STT (Parakeet-TDT / Qwen3-ASR) if CPU latency ever bites.
  - **Cheap integration path** (no rewrite): run audio.cpp's server, let corrallm
    proxy it as a second audio backend — same pattern oidio uses. Revisit when it
    matures past v0.1.

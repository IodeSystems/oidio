# oidio

> _oído_ — "ear." An OpenAI-compatible audio server in Go, backed by
> [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx). CPU-only, no Python.

Point any OpenAI client at it for speech-to-text. One Go binary covers the audio
surface that otherwise takes a pile of separate Python services — STT (batch and,
as slices land, realtime), diarization, and TTS — by calling sherpa-onnx through
its Go bindings directly. No `uv`, no venvs, no sidecar per model.

Status: **feature-complete** — batch STT, diarization, TTS, realtime over both
WebSocket and WebRTC, and a `/v1/capabilities` discovery manifest. See
[`docs/PLAN.md`](docs/PLAN.md) for optional polish (TURN, a browser example).

## Why

The usual self-hosted audio stack is N Python processes (one per model, each its
own environment) glued behind a proxy. sherpa-onnx is C++ with Go bindings that
expose the whole stack — streaming + offline ASR, VAD, speaker embeddings, offline
diarization, and TTS (incl. Kokoro) — so it collapses into a single static-ish Go
binary that speaks OpenAI natively.

## Endpoints

Standard first; deviate only where OpenAI has no equivalent, and then only
**additively** (extra fields plain clients ignore).

| Endpoint | Status |
|---|---|
| `POST /v1/audio/transcriptions` (+`/translations`) — multipart; `response_format` json/verbose_json/text/srt/vtt; `stream=true` → SSE | ✅ |
| ↑ same endpoint, a `diarize` model — speaker-labeled segments + stateless `speakers` (UUID + voiceprint + similarity) | ✅ |
| `POST /v1/audio/speech` — TTS (Kokoro); mp3/opus/aac/flac/wav/pcm | ✅ |
| `GET /v1/realtime` — live STT over WebSocket (OpenAI Realtime transcription schema) | ✅ |
| `POST /v1/realtime` — live STT over WebRTC (`application/sdp` offer → answer; Opus track + `oai-events` data channel) | ✅ |
| `GET /v1/models`, `GET /v1/capabilities`, `GET /health` | ✅ |

`stream=true` emits OpenAI's streaming-transcription SSE — `transcript.text.delta`
… `transcript.text.done`.

## Quick start

```sh
make build
cp oidio.example.yaml oidio.yaml   # edit model paths
./oidio --config oidio.yaml        # listens on :8077
```

```sh
curl -sS localhost:8077/v1/audio/transcriptions -F model=stt -F file=@speech.wav
# {"text":"…"}
curl -sS localhost:8077/v1/audio/transcriptions -F model=stt -F stream=true -F file=@speech.wav
# data: {"type":"transcript.text.delta","delta":"…"}  … data: {"type":"transcript.text.done","text":"…"}
```

Needs `ffmpeg` on PATH (decodes any upload to 16 kHz mono). Models are sherpa-onnx
releases; see `oidio.example.yaml`.

## Configure

`oidio.yaml` maps an OpenAI `model` name to an engine and its model files. One
process hosts several models and dispatches on the request's `model` field:

```yaml
addr: ":8077"
models:
  stt:
    type: transducer        # offline zipformer transducer
    encoder: models/…/encoder.int8.onnx
    decoder: models/…/decoder.int8.onnx
    joiner:  models/…/joiner.int8.onnx
    tokens:  models/…/tokens.txt
    num_threads: 4
    language: en
```

## Speakers (design)

oidio is **stateless about identity** — it never stores names or a speaker
catalog. A `diarize` model returns, per request:

- A **stable speaker UUID** on each `verbose_json` segment (`speaker`).
- A top-level **`speakers`** array — each `{uuid, embedding, similarity}`, where
  `embedding` is the voiceprint (so a consumer can persist and re-match it) and
  `similarity` is the cosine score to every other speaker in the result.
- The auto-match **confidence threshold is a request argument**, and a consumer
  may pass **known speakers** (`[{uuid, embedding}]`) so UUIDs stay stable across
  calls.
- The **clustering knobs are request arguments** too: `cluster_threshold` (lower →
  more speakers) and `num_clusters` (force a known count).
- `speaker_merge_threshold` collapses clusters whose voiceprints are that close
  into one speaker, recovering a speaker that long audio over-split. Unset picks
  a threshold from the cluster count and duration (0.83–0.95): more clusters and
  more audio mean more chances for some pair to cross the bar, so the bar rises
  with both. Merging is complete-link — every pair in a group must clear it —
  because a single bridging cluster otherwise chains unrelated speakers together,
  which collapsed a 44-minute hearing into one speaker. Pass `0` to disable.

`response_format=srt` / `vtt` on a diarize model emits one cue per speaker turn,
each prefixed `Speaker N:` (numbered by first appearance — a UUID isn't readable
in a subtitle). The JSON formats still carry the UUIDs.

Mapping a UUID to a real person ("that's Carl") is the **API consumer's** job, in
their own system. oidio only emits voiceprints, UUIDs, and similarities. See
[`docs/PLAN.md`](docs/PLAN.md) for the full shape.

## With corrallm

oidio is a plain OpenAI backend; [corrallm](https://github.com/IodeSystems/CorraLLM)
proxies it like any other model (spawn `cmd`, `proxy` to its port), with no
audio-specific code. It replaces corrallm's Python audio adapters.

## Develop

```sh
go test -race ./...     # unit tests (audio codecs, speaker resolution, config, capabilities)
```
The engine **concurrency** tests need real sherpa models; point `OIDIO_TEST_MODELS`
at a models dir to run them (otherwise they skip):
```sh
OIDIO_TEST_MODELS=/path/to/models go test -race ./internal/engine/
```
Each engine serializes its shared sherpa objects with a mutex — the cgo
thread-safety is unverified, and CPU-bound inference loses nothing to serialization
(concurrency is bounded upstream anyway). CI (`.github/workflows/ci.yml`) builds,
vets, and runs the race tests on every push.

## Stack

Go 1.26, cgo + the prebuilt sherpa-onnx native lib (via `sherpa-onnx-go`), stdlib
HTTP, pion (WebRTC/Opus). `ffmpeg` for decode/encode.

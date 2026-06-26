# oidio

> _oído_ — "ear." An OpenAI-compatible audio server in Go, backed by
> [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx). CPU-only, no Python.

Point any OpenAI client at it for speech-to-text. One Go binary covers the audio
surface that otherwise takes a pile of separate Python services — STT (batch and,
as slices land, realtime), diarization, and TTS — by calling sherpa-onnx through
its Go bindings directly. No `uv`, no venvs, no sidecar per model.

Status: **batch STT shipped and validated.** Diarization, TTS, and realtime are
scaffolded (the routes exist and report `501`) and land next — see
[`docs/PLAN.md`](docs/PLAN.md).

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
| `POST /v1/audio/speech` — TTS | ◻ 501 |
| `GET /v1/realtime` — live STT over WebSocket | ◻ 501 |
| `GET /v1/models`, `GET /health` | ✅ |

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

Mapping a UUID to a real person ("that's Carl") is the **API consumer's** job, in
their own system. oidio only emits voiceprints, UUIDs, and similarities. See
[`docs/PLAN.md`](docs/PLAN.md) for the full shape.

## With corrallm

oidio is a plain OpenAI backend; [corrallm](https://github.com/IodeSystems/CorraLLM)
proxies it like any other model (spawn `cmd`, `proxy` to its port), with no
audio-specific code. It replaces corrallm's Python audio adapters.

## Stack

Go 1.26, cgo + the prebuilt sherpa-onnx native lib (via `sherpa-onnx-go`), stdlib
HTTP. `ffmpeg` for decode.

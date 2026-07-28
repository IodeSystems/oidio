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

## Tools

Two subcommands that are not part of the server. They exist because a
diarization result is a *hypothesis*, and a hypothesis nobody has checked cannot
be scored, corrected, or trusted.

### `oidio verify` — ground-truth workbench

```sh
oidio verify --listen 0.0.0.0 --audio hearing.m4a --transcription hearing.json \
             --speakers speakers.json --truth hearing.truth.json
# oidio verify → http://0.0.0.0:41475
```

Serves a keyboard-driven page for correcting **who spoke each turn**. It is an
editor, not a survey: attribution alone cannot express the common failure, a
diarization boundary landing mid-sentence and leaving one speaker's last word in
the next speaker's turn. So it owns segments — join, split at a word boundary,
correct the text, undo.

| | |
|---|---|
| `space` `r` · `j` `k` | play / replay · next / prev turn |
| `enter` | pick speaker (`enter` again = confirm as-is) |
| `shift+enter` | pick for **every turn of this cluster** |
| `1`–`9` · `↑` `↓` | assign speaker |
| `a` · `s` | join with previous · split |
| `c` · `x` · `u` | correct the text · unclear · undo |
| `/` · `?` | next / prev unreviewed |
| `-` `=` · `,` `.` · `h` | volume (to 300%) · text size · hide the key bar |

**Clusters are the machine's guess; people are yours.** Every turn carries an
immutable `cluster` and a mutable `speaker`. Keeping the machine's grouping is
what makes the common repair expressible: *"the diarizer split one person in
two, and everything it called c3 is one person"* needs that grouping to survive the
first correction.

Three flags in the output mean different things, and the difference matters when
scoring:

- `confirmed` — a person ruled on this turn. Absent means untouched, and an
  untouched turn is **not evidence**.
- `unclear` — audible but not attributable. A real answer: it marks turns where
  no method should be expected to be right.
- `corrected` — the text was retyped. **Only these are ground truth for word
  error rate.** Scoring WER across everything would compare the recogniser to
  its own output and report a flawless zero.

Saved on every keystroke via atomic rename; `--raw` disables playback level
correction, `--port` pins the port so a restart does not orphan an open tab.

### `oidio verify render` — the truth file as prose

```sh
oidio verify render hearing.truth.json          # writes hearing.verified.md
oidio verify render --note "…" *.truth.json     # a line your corpus needs said
```

The header is **generated, never written**. A hand-written one says whatever
someone believed when they wrote it, and drifts the moment the file changes — on
the corpus this was built against, that produced a header claiming a
completeness the data did not carry.

It matters because a transcript is a source document downstream, and facts get
extracted from it and cited. A file that is 18-of-39 confirmed, presented as
"the verified transcript", launders an unchecked machine guess into the record
one step removed. So the banner states exactly how much was ruled on, and
attribution and WORDING are reported as separate axes — checking who spoke says
nothing about whether the words are right.

`--note` appends a line the domain supplies. oidio ships a warning strong enough
for any transcript but cannot know what a corpus is *for*; "before it goes into
a declaration, a brief, or a deposition question" is right for a legal archive
and wrong everywhere else.

### `oidio verify score` — how far the machine was from the person

```sh
oidio verify score hearing.truth.json hearing.diarized.json
```
```
REVIEW        30% confirmed · 70% affirmed · 0% unclear · 0% untouched
WER           unmeasurable — no turn's text has been corrected

STRICT  DER  55.8%   one cluster per speaker — over-splitting counts as error
MERGED  DER  15.9%   each cluster to its best speaker — separation only
cost of over-splitting: 39.9 points
```

**Two numbers, because one hides which failure produced it.** On three hearings
from one corpus, strict DER came out at 55.8%, 55.0% and 27.9% — and the two
that matched did so for opposite reasons. One separated its voices well and was
wrecked by over-splitting; the other over-split less but failed to separate at
all, with a single cluster holding 1125 seconds spread near-evenly across four
people. As one figure those look identical and call for opposite fixes.

Review coverage is reported as four shares rather than summed: `confirmed` is a
per-turn ruling and `affirmed` a blanket acceptance of the rest, and a DER is
only as trustworthy as the answer to *was **this** turn looked at*. Untouched
audio is scored as if it were truth, and says so.

### `oidio speakers review` — post-hoc correction

```sh
oidio speakers review --audio hearing.m4a hearing.json \
                      --llm http://localhost:8111/v1 --llm-model qwen
```

Proposes corrections without re-running diarization: **MOVE** a passage filed
under the wrong voice, **JOIN** one person split across ids, **SPLIT** a turn
that holds two speakers. With `--llm`, a second opinion is drawn from *what was
said* — independent of how it sounded — and the two signals are reported
separately, never blended. Where they disagree is the most informative thing the
review produces. Nothing is written without `--apply` or `--apply-agreed`.

### Prior art

[Gecko](https://github.com/gong-io/gecko) (Gong.io, Interspeech 2019) covers
much of the same ground and adds a waveform view and multi-model comparison.
[voxmap-studio](https://arxiv.org/abs/2606.26842) is pyannote-integrated and
gates export on per-segment confirmation. Both are worth looking at before
adopting these. What is different here is that the tools are subcommands of the
server that produced the output, so there is no export/import round trip and the
truth file is the same schema as the API response — which is what makes scoring
a hypothesis against it a one-liner.

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

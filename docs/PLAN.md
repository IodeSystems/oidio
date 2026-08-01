# oidio — plan

OpenAI-compatible audio server in Go over sherpa-onnx. Standard-first; deviate
only where OpenAI has no equivalent, and only additively.

## Slices

- ✅ **S1 — Batch STT.** `POST /v1/audio/transcriptions` (+`/translations`) over an
  offline transducer. `response_format`: json, verbose_json, text, srt, vtt.
  `stream=true` → OpenAI SSE (`transcript.text.delta` … `transcript.text.done`).
  ffmpeg decode → 16 kHz mono. Config-driven model registry. Validated end-to-end
  against the gigaspeech int8 zipformer.
  - ✅ **m4a/mp4 uploads (bug fix).** `DecodePCM` piped uploads to ffmpeg's stdin,
    but the MP4 demuxer must seek (moov atom is usually at the END of the file);
    on a pipe it failed with "partial file" *and still exited 0*, so decode
    returned zero samples with a nil error. sherpa then indexed `samples[0]` and
    panicked the connection. Now: spool the upload to a temp file so ffmpeg gets a
    seekable path, error on a zero-sample decode, and guard `STT.Transcribe` /
    `Diarizer.Process` against empty input. **No m4a upload worked before this.**
  - **next**: pseudo-segments from token timestamps (currently one whole-clip
    segment); whisper/sense-voice model types; honest `translate` task wiring.
  - **risk**: uploads now hit disk twice (multipart spool + decode spool). Fine at
    current sizes; revisit if large-file throughput matters.
- ✅ **S2 — Diarization** (`type: diarize`). Done + validated. Offline
  `OfflineSpeakerDiarization` (pyannote segmentation + speaker embedding +
  clustering) + the transducer ASR, aligned by token timestamp → speaker-labeled
  segments. **Stateless identity:** each speaker is a UUID (matched to a
  caller-supplied `known_speakers` above `speaker_confidence`, else minted); the
  response carries each speaker's 512-d voiceprint `embedding` and a `similarity`
  object (cosine to the others). No server-side catalog; names live at the caller.
  Request args (additive multipart fields): `speaker_confidence`, `known_speakers`
  (JSON `[{uuid,embedding}]`), `cluster_threshold`, `num_clusters`,
  `speaker_merge_threshold`. Validated on the real 2-speaker EN clips — correct
  count, and a passed-back voiceprint reuses its UUID.
  - ✅ **Per-request clustering + merge pass.** The clustering knobs are now
    arguments, not just model config (sherpa `SetConfig` rewrites clustering with
    no model reload; defaults are re-applied every call so an override can't leak
    across requests on the shared `sd`). `speaker_merge_threshold` adds an
    optional union-find pass over the pairwise voiceprint cosines: clusters that
    close collapse to one speaker, spans are remapped, and the group is re-embedded
    over its full concatenated audio before alignment — so merged runs coalesce
    into single segments. Off by default (`merge_threshold: 0`).
  - ✅ **SRT/VTT on the diarize path.** `response_format=srt|vtt` used to fall
    through to JSON here. Now one cue per speaker turn, prefixed `Speaker N:`
    (numbered by first appearance; UUIDs are unreadable in a subtitle). Segment
    times are token-START timestamps, so cues stretch to the next turn's start,
    capped at 2 s so a cue can't span a long silence, floored at 0.5 s.
  - **next**: pseudo-segments for the plain STT path.
  - **risks**: merging is **single-link, so transitive** — A–B and B–C fuse A and C
    even when A–C is below threshold; one bad link can join two real people. Keep
    the threshold high (≈0.8+). **Untested on real audio** — the merge logic has
    pure unit tests only; no end-to-end run against a long multi-speaker clip has
    been done, so the useful threshold value is unknown. Auto speaker-count remains
    threshold-sensitive; pass `num_clusters` when the count is known.
  - **blocking decision (user)**: whether to enable `merge_threshold` by default in
    the shipped config, and at what value — needs a real long-file validation run
    first.
  - **optional extension**: return the merge decisions in the response (which local
    clusters collapsed, at what cosine) so a consumer can audit or override them.
  - **CPU starvation guard** (done): the CPU-bound in-process engines (diarize,
    stt/whisper, realtime) used to saturate every core and 503 the HTTP/WS server.
    Now, for all three: (1) `num_threads` is auto-capped to `GOMAXPROCS-1`
    (`reserveCore`, reserve a core for request serving) and (2) a `nice:` config
    knob raises the onnxruntime worker pool's Linux niceness at model init
    (`withNice`, per-thread `setpriority`; the pool inherits it), so nice-0 handler
    goroutines preempt it. No-op off Linux.
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

- **Thresholds are arguments.** The auto-match confidence (`speaker_confidence`)
  and the clustering knobs (`cluster_threshold`, `num_clusters`,
  `speaker_merge_threshold`) are all request parameters; server config only sets
  their defaults.
- **Over-split recovery is server-side, opt-in.** Long audio makes AHC split one
  person into several clusters. `speaker_merge_threshold` collapses clusters whose
  voiceprints match that closely, before UUIDs are assigned — the server acting on
  the same cosine evidence it already returns. It stays opt-in because merging two
  real people is worse than emitting two ids for one; the `similarity` matrix is
  still returned either way, so a consumer can also do this itself.
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

- ✅ **S? — `oidio attest`: the verify workbench, moved into the shared framework.**
  `internal/attestaudio` + `cmd/oidio/attest.go`. Turns a diarize result into an
  `attest` reading and serves the shared review UI over it. The framework lives
  in `github.com/iodesystems/attest` (local `replace`), where raglit and kgraph
  use the same one — see that repo's `plan/plan.md` for why three repos had grown
  three halves of it.

  What this has that `verify` does not:
  - **The audio a reviewer hears is identified.** Each turn carries the sha256 of
    its DECODED samples (16 kHz mono s16le, pinned), so the page can say whether
    what is playing is what the recogniser was given. The levelled rendering is
    served separately and labelled as not-the-artifact. `verify` plays a
    level-corrected copy and records no such distinction, so a verdict made
    against it cannot say which audio it rests on.
  - **A verdict says who made it and under whose authority** — an attorney can
    hand a paralegal the link and the record shows both.
  - **The same page reviews a scanned exhibit**, because raglit emits the same
    format.

  - **next**: use it on a real hearing. Until then `oidio verify` is untouched
    and remains the working workbench.
  - **risk**: sealing decodes each turn separately (`-ss`/`-to` after `-i`, for
    sample-accurate seek), so a transcript with thousands of turns is slow to
    seal. The fix is a single streaming pass that hashes windows as samples go
    by; not built because nothing has hit it.
- ✅ **`oidio attest import` — carrying a verify pass across.**
  `internal/attestaudio/importtruth.go`.

  **Settled:** a verify `confirmed` IS replayed as an attest `confirmed`. The
  alternative is why — downgrading to `affirmed` would make a turn somebody sat
  and judged indistinguishable from one they swept past, which is precisely the
  failure `affirmed` exists to prevent, and refusing to import throws the pass
  away. So the ruling crosses intact and the CONTEXT crosses with it:
  `--reviewed-by` is required (verify recorded no author, and inventing one
  produces something that reads like a real signature), and every entry carries
  `auth: import:oidio-verify`, which the review page shows — so an imported
  ruling never looks like one made in front of the page.

  The substantial part is that verify is an EDITOR. A truth file's turns
  routinely are not the machine's turns, so joins and splits become
  **resegments**, partitioned by shared boundary: a time both segmentations
  agree is a turn edge. One act of re-cutting inside one span is one resegment,
  because the log must describe edits that were actually performed.

  What it deliberately will not do:
  - A re-cut turn carries **no evidence digest** — nothing read it — so the page
    says "no recorded artifact, this unit was cut by a person".
  - Retyping on a re-cut turn is **kept but not scorable**: there is no machine
    reading of that exact span to score against, and counting it would measure
    the recogniser against itself.
  - A sweep is replayed **per unit, not as a blanket**. attest's blanket is
    positional and would affirm turns created after the sweep, putting a ruling
    in someone's mouth.
  - Segments outside the recording are **refused and reported**, not imported as
    one enormous resegment superseding nothing.

  - **next**: run it on the real corpus, then cut `verify` down to a shim.
  - **risk**: idempotent by convergence, not by detection — importing twice
    appends the pass twice and the later entries win, so the state is identical
    but the log says it happened twice, because it did.

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

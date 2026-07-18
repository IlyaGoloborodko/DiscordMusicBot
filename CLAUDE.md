# discordAudio — project guide (for Claude)

A Go Discord music + AI-DJ bot. Users control it by slash commands **and by voice**
(wake word "Марина"). It orchestrates two external Python services.

## Services & ports (all local)
| Service | Env var | Port | Purpose |
|---|---|---|---|
| AI agent + TTS | `AI_SERVICE_ADDR` | 8000 | `POST /agent` (decides actions), `POST /tts` (→ OpenAI `gpt-4o-mini-tts`) |
| Search/media | `SEARCH_SERVICE_ADDR` | 9000 | `/search`, `/stream`, `/playlist` (YouTube etc.) |
| OpenAI STT (cloud) | `OPENAI_API_KEY` | — | command text; `gpt-4o-mini-transcribe`. **Required, no fallback** |
| Vosk STT (websocket) | `VOSK_SERVER_ADDR` | 2700 | `alphacep/kaldi-ru`, wake-word gate. Always local |

Python sources live outside this repo: `C:\Users\sok20\PycharmProjects\DiscordAiService`
and `...\DsBotSearchService`.

## Env vars
- `AI_SERVICE_ADDR`, `AI_SERVICE_API_KEY`, `SEARCH_SERVICE_ADDR` — service endpoints.
- `DEBUG_GUIID` — comma-separated guild IDs to register slash commands in; **empty ⇒
  global** (every server, but up to an hour to propagate). Guild commands are instant,
  which is why it's the dev setting — but they exist only there, so a bot on a second
  server joins voice and shows no commands. `config.CommandGuildIDs()` maps empty to
  `[]string{""}` because discordgo reads an empty guild id as "global"; returning an
  empty slice instead would register nothing anywhere (`config/consts_test.go`).
- `OPENAI_API_KEY` — **required for voice control**; without it commands are not
  transcribed at all (the bot warns at startup and slash commands still work).
  `OPENAI_STT_MODEL` defaults to `gpt-4o-mini-transcribe` (~5.3% WER ru, $0.003/min,
  0.5-1.5s). The Vosk gate stays local, so only wake-word utterances leave the machine.
  **The self-hosted whisper fallback was removed**: it measured 4s+ per utterance on
  this CPU against 0.5-1.5s for OpenAI, and the GPU route is closed (the RTX 50-series
  is sm_120, too new for the image's PyTorch). Restoring it means bringing back
  `whisperTranscribe`, its 25.7GB image and the `vad_filter`/`initial_prompt` handling.
- `VOSK_SERVER_ADDR` — the wake-word gate.
- `STT_VOSK_ONLY=1` — use Vosk for the command text too (skip the command model).
  **Leave off.** Vosk's small model is a wake-word gate; using it as the command model
  is what made recognition feel broken (it ran this way for weeks while the whisper
  container was dead, which is the real story behind "распознаётся косо/криво").
- `STT_VOSK_STREAM=1` — keep one Vosk connection per speaker and stream audio as it
  arrives, instead of opening one per utterance. Measured finalisation cost: **~260ms
  and flat**, versus 600-720ms for the per-utterance path, which also grows with the
  length of the utterance. Off by default; the per-utterance path stays as fallback and
  is used automatically if the dial fails.
- `STT_LOG_LEVEL` — 0 silent / 1 commands (default) / 2 all transcripts.
- `AI_DEBUG=1` — log raw `[ai] ->` request / `[ai] <-` response to the AI service.
- `DJ_BREAK_EVERY` — DJ comment every N tracks (default 3).
- `WHISPER_BIN`/`WHISPER_MODEL`/`WHISPER_SERVER_ADDR` — DEAD (local whisper removed);
  safe to delete from `.env`.

## Layout
- `cmd/bot` — entrypoint.
- `internal/discord` — slash-command registration + dispatch (`commands.go`).
- `internal/voice` — voice receive + STT pipeline (`listen.go`, `stt.go`), command
  handlers (`join.go`, `pause.go`, `volume.go`, `play.go`, `prompt.go`, ...).
- `internal/player` — per-guild player: single goroutine + command channel
  (`player.go`, `manager.go`).
- `internal/stream` — ffmpeg → PCM → gopus → Discord (`stream.go`).
- `internal/aiService` — HTTP client + models + tool registry (`client.go`,
  `models.go`, `tools.go`).
- `internal/music` — search-service HTTP client.
- `third_party/discordgo` — **vendored fork** of discordgo, editable, via
  `replace github.com/bwmarrin/discordgo => ./third_party/discordgo`.

## Why the vendored discordgo fork (DAVE E2EE)
Discord **mandates the DAVE (E2EE) protocol** on voice since **2026-03-02**; upstream
`bwmarrin/discordgo` can't connect (close code 4017). The fork (`yeongaori/discordgo`)
does the DAVE handshake. It only implemented **send** (encrypt). We added **receive**
decryption so the bot can transcribe other users:
- `third_party/discordgo/dave_recv.go` — `DecryptFrame` (per-sender key from the shared
  MLS exporter secret; AES-CTR, skips the 8-byte truncated tag).
- `voice.go` — `ssrcUsers` map (SSRC→user from OP5), `SSRCUser()`, decrypt call in
  `opusReceiver`, and `Speaking int` (Discord sends it as a number, not bool).
BSD-3 licensed; keep `LICENSE`. Don't edit `third_party/` casually — it forces a full
rebuild (see below).

## Voice → AI flow
1. `opusReceiver` (fork) decrypts DAVE frames; `listen.go` decodes per-SSRC, segments on
   a 3s pause / 10s cap.
2. **Vosk** transcribes the segment (cheap) and gates on wake word "Марина" (`wakeWords`
   in stt.go). Only wake-word utterances (or the ≤10s armed follow-up) go further.
3. Command text comes from OpenAI (or Vosk itself under `STT_VOSK_ONLY`). Full text
   incl. the wake word is sent to the AI.

## The wake word is a two-stage cascade — don't collapse it
The two stages have **opposite jobs**. Keep them that way; `stt_test.go` locks it down.

1. **Vosk / `containsNearWake`** — high recall, low precision. It only decides "is this
   worth paying the accurate model for". It matches `nearWakeWords`, which deliberately
   includes **real words that are not the wake word** ("машина", "малина").
2. **Accurate STT / `containsWakeWord`** — precision. It decides whether to act, using
   `wakeWords`. Whatever stage 1 merely misheard is dropped here, having cost one
   transcription (~$0.0002) and no wrong action. Skipped when `armed` (in a follow-up the
   name was in the previous segment).

**Why:** Vosk's big model has an open vocabulary, and its language model prefers "машина"
(common noun) over "марина" (a name) on near-identical acoustics. Observed live:
`VOSK="я машина как дела"` for "Марина, как дела" → the bot ignored the user. Demanding an
exact hit from the cheap model is asking it for precision it does not have.

**Two things that look like fixes and are not:**
- *Adding "машина" to `wakeWords`.* Then "включи Машину времени" wakes the bot for real.
  The loose list exists precisely so the confusions never reach the acting decision.
- *Vosk `phrase_list` (closed grammar).* Measured, A/B, on `vosk-model-small-ru-0.22`
  (dynamic graph — the shipped `alphacep/kaldi-ru` big model is static HCLG and rejects it
  anyway): a restricted vocabulary **snaps everything to the nearest phrase**. "Моя машина
  сломалась вчера" came back as `"марина марина [unk]"` and "Включи Машину времени" as
  `"[unk] марина"` — constant false wakes. The big open-vocabulary model got both right.

## Streaming gate (STT_VOSK_STREAM) — three things measured the hard way
1. **Utterance boundaries stay ours.** Kaldi's endpointer only fires when it hears a
   real noise floor: measured, it finalises after low-level noise and stays silent
   through digital zeros, the acoustic model having never been trained on absolute
   silence. Discord sends no packets at all while nobody speaks, so waiting for Kaldi
   to close an utterance means waiting forever. The `pauseTimeout` timer decides.
2. **`{"eof" : 1}` is what completes a transcript, not padding.** The online decoder
   holds words back until audio follows them. A 1.4s "Марина" came back **empty**, and
   "Марина, как дела" came back as just "марина"; noise tails of 300ms up to 2s did not
   help. The end-of-stream marker did, immediately. It also ends the session, which is
   why the connection is recycled per utterance (`Flush` → `TakeText` → `Close`).
3. **The recognizer must be recycled.** It keeps decoder state across utterances, so a
   reused connection returns the previous transcript glued to the front — one "Марина"
   would then hold the gate open for every sentence after it.

**The segment cap must not cut where it lands.** It bounds memory (without it an
unbroken monologue grew the buffer without limit — 14.7s segments seen live), but unlike
a pause it falls wherever the speaker happens to be, so cutting there can split the wake
word across two segments and lose it. Instead, when the cap is reached and nothing near
the name has been decoded, the audio is dropped — it could never have been needed — and
the stream keeps running, holding a `capOverlapSamples` tail to cover the gate's ~2s
decoding lag in case the name is being spoken right then. Only a real wake candidate
causes a cut, and then it is an ordinary one, so a command is never delivered twice
(which a naive overlap would do whenever a whole command landed inside it).

The incremental downmix (`downmix.go`) exists because the 5-tap filter reads two samples
either side of each output. Run per packet without carry-over it would clamp at every
20ms Opus boundary — a click every 20ms, in the exact band that separates "марина" from
"машина". `downmix_test.go` locks it to the batch version sample-for-sample.

## STT audio handling — what NOT to re-add
Benchmarks on real Russian audio show VAD-cutting and AGC cost **6-9 points of WER**: the
models are trained on unprocessed audio, so "cleaning" it moves the input off-distribution.
- **Don't trim on an amplitude threshold.** A `trimSilence()` used to cut to the region
  above int16 350. Unvoiced consonants (с, ш, ф, х, ц) are low-amplitude, so it ate word
  onsets/endings; the companion RMS gate dropped quiet mics' clips entirely. Both gone.
  `minSpeechRMS` is now a silence floor (40), not a speech gate. Nothing else trims.
- **Don't resample ourselves.** ffmpeg produces the 16k mono FLAC that goes to OpenAI.
  Our own decimator rolled 8kHz off ~7dB and aliased 9-14kHz back onto 2-7kHz at only
  -12..-19dB — right where the fricatives are. `downmixTo16kMono` and the streaming
  `downmixer` survive *only* because Vosk's websocket protocol demands 16k mono.
- **Don't add AGC/normalization** — actively harmful per the above.
- `whisperPrompt` (sent as `prompt`) biases the decoder toward the wake word + playback
  vocabulary. This is contextual biasing and buys most of what training a custom KWS
  model would, for free.
- **A prompt needs an echo defence.** Fed non-speech, the model parrots the prompt back as
  the transcript — measured: silence and noise both returned "Разговор с ботом Мариной."
  verbatim, which carries the wake word and would hand the AI a phantom command. The
  retired local backend had `vad_filter` to suppress it; the OpenAI API has no such knob,
  so **the blacklist in `hallucinationMarkers` plus the Vosk gate are now the whole
  defence**. Keep `whisperPrompt`'s first sentence something no real user would say.
4. `handleAI` calls `POST /agent` with `Tools: PlayerTools()` and `context`
   (now_playing, queue, queue_len, volume). Music **ducks** while the AI thinks.

## AI contract — client-declared tools (variant A)
The bot advertises capabilities as **tools** so the AI service stays decoupled from
Discord. Request has `tools: [...]`; response has `tool_calls: [{name, arguments}]`.
- Tools: `play`/`enqueue`/`replace_queue` (args `{tracks:[...]}` — service resolves via
  its own search), `pause`/`resume`/`skip`/`stop` (no args), `volume_up`/`volume_down`
  (no args, bot does current ±1).
- `AgentResponse.PrimaryEffect()` reduces tool_calls to one queue/transport action +
  tracks; `VolumeDelta()` handles volume. Legacy `action`+`tracks` kept as a fallback
  for autoplay/DJ prompts (which send no tools).

## Player
Single run goroutine owns the queue; callers use the command channel. Live atomic
controls read by the stream loop: `duckDepth` (ducking while AI thinks, ×0.25),
`paused` (holds position), `volume` (1-10, gain = level/10, default 5). Only
play/replace/skip/stop preempt the current track; enqueue/pause/resume/volume/none keep
it playing.

**Talking over the music (overlay mixing).** `spoken_answer` used to go to `p.pending`,
which the loop only plays *between* tracks — so answers waited for the current track to
end. Now: if `musicPlaying` is set, `applyAgent` spawns `speakOver()`, which fetches the
TTS, transcodes it to 48k stereo in memory (`stream.TranscodePCM`), ducks the music and
hands the clip to `overlayBuf`. The stream loop drains it frame by frame and mixes it
over the (already gain-reduced) music with saturating adds — so the assistant is heard
immediately at full volume over quiet music. With nothing playing, the old `pending`
path is used.

## KNOWN GOTCHAS
- **AI session-memory poisoning** (biggest live issue): the AI service keeps
  conversation memory per guild/channel. If it once returns bad output (English canned
  text, `play_track` instead of `play`, empty tool args, no tool_call), it few-shot
  copies its own bad history and keeps failing **for that channel**. Proven: same request
  in a FRESH channel works; in the poisoned channel it returns `action=none`/`play_track`.
  Fix is service-side (don't feed raw assistant outputs back into memory; reset memory).
  To test the bot, use a fresh channel or clear that session's memory.
- **Slow cold builds**: CGO `gopus` (compiles libopus C) + `cloudflare/circl` (16 pkgs
  for DAVE). Incremental builds are ~0.4s; editing `third_party/` forces a full rebuild.
  Prefer `go build -o bot.exe ./cmd/bot` then run the binary.
- gopus emits harmless C `-Wstringop-overread` warnings — ignore.
- Port 9000 is the search service — never map whisper there (use 9010).

## Build / run
```
go build -o bot.exe ./cmd/bot   # then run ./bot.exe (fast restarts, no recompile)
go build ./...                  # check everything compiles
```
Windows + PowerShell primary; use single-line `docker run` (backtick, not `\`, for line
continuation). GPU whisper needs a Blackwell-capable image — the user's RTX 5060 Ti
(sm_120) only works with `faster_whisper` (CTranslate2), not `openai_whisper` (PyTorch);
currently running CPU.

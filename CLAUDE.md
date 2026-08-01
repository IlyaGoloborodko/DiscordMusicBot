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
and `...\media-source-service`.

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
- `VOICE_IDLE_TIMEOUT` — leave the voice channel after this long with no speech, no
  commands and nothing playing or queued (**in seconds**, default `3600`, `0` disables;
  suffixed durations like `30m` are not accepted). discordgo has
  nothing for this: `UpdateGameStatus(idle …)` is the bot's presence and
  `Guild.AfkTimeout` is the server's rule for moving *people* to the AFK channel, so
  connection lifetime is ours to manage. Playing or queued music counts as activity on
  its own — nobody should have to talk over an album to keep the bot around.
- `VOICE_ALONE_TIMEOUT` — leave once the voice channel has been empty of *people* for
  this long, whatever the player is doing (**in seconds**, default `600`, `0` disables).
  Checked separately from the idle timeout (`aloneTooLong`), and it has to be:
  music counting as activity plus autoplay refilling the queue means the idle timeout
  would never fire for a bot playing to nobody, and every one of those tracks would be
  reported to `/playback` as genuinely listened to. The default 10 minutes are grace: a client
  reconnecting or a listener switching channels drops out of `VoiceStates` briefly, and
  cutting the music under someone who never left is the worse failure. A state that
  cannot be read (no session, no cached guild) never counts as an empty room.
- `LOG_LEVEL` / `TG_LOG_LEVEL` — two independent levels over one call
  (`logger.Debugf/Infof/Warnf/Errorf`): what reaches the console and what reaches
  Telegram. `DEBUG < INFO < WARN < ERROR`, plus `OFF` for Telegram. Defaults `INFO` /
  `ERROR` — the phone is for "something broke", not a live feed. Mirrors the AI
  service's `LOG_LEVEL`/`TELEGRAM_LOG_LEVEL` (`app/logging_setup.py`), including
  accepting `WARNING` as a spelling. `logger.Send` still exists and is exactly
  `Errorf("%s", text)`. Telegram is optional: with no `TG_BOT_SECRET`/`TG_CHAT_ID` the
  bot starts anyway (it used to `log.Fatal`) — a missing log destination must not stop
  the music.
- `STT_LOG_LEVEL` — 0 silent / 1 commands (default) / 2 all transcripts (separate knob,
  it decides which transcripts get logged at all, not their severity).
- `AI_DEBUG=1` — log raw `[ai] ->` request / `[ai] <-` response to the AI service.
- `ADMIN_API_KEY` / `ADMIN_ADDR` — the admin API (see below). **The API is off unless
  the key is set**, and setting `ADMIN_ADDR` *without* a key is a startup error rather
  than an open endpoint — there is no configuration that serves without a token.
  `ADMIN_ADDR` defaults to `127.0.0.1:8090` (loopback, so a laptop run is not exposed);
  compose overrides it to `0.0.0.0:8090`, which is still only reachable inside the
  compose network because nothing publishes the port.
- `TELEMETRY_FILE` / `TELEMETRY_MAX_EVENTS` / `TELEMETRY_MAX_FILE_MB` — where the
  operational history goes (default: memory only, 2000 events, 32MB before it rotates
  to one `.1`). In compose it is a named volume, so the history that explains a crash
  survives the restart that followed it.
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
- `internal/telemetry` — ring buffer + JSONL of what the bot decided (`store.go`).
- `internal/adminapi` — read-only HTTP API for the admin panel (`server.go`,
  `handlers.go`, `stats.go`, `auth.go`).
- `internal/adminauth` — **leaf package, must keep zero imports**: the header
  names and roles shared by the bot, the gateway and the AI service.
- `internal/adminui` + `cmd/adminui` — the panel's gateway: Discord OAuth2,
  signed-cookie sessions, authenticating reverse proxy, embedded static panel.
- `deploy/admin/Dockerfile` — builds the gateway (context is the repo root).
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

## DAVE epoch changes (someone joins/leaves the voice channel)
The fork cannot process an MLS commit — `HandleCommit` is a no-op — so on opcode 29 it
asks the gateway to re-add it and waits for a fresh Welcome.

**Never send READY_FOR_TRANSITION (23) from that path.** The protocol is explicit: a
client that cannot process a commit sends INVALID_COMMIT_WELCOME (31) *instead of* 23.
Sending both told the gateway we had transitioned fine, so it never re-added us, the
Welcome never came, and audio died in both directions — the stale sender key is rejected
by everyone (music stops) while stale receive keys turn frames into noise (transcripts go
empty). Measured live: ~26s dead every time anyone joined or left, recovered only by the
watchdog reconnecting. Order is 31 first, then the key package (26): the gateway removes
us on 31 and re-adds us when it sees the new key package.

`watchReWelcome` stays as a safety net — if no Welcome arrives it rebuilds the
connection, which costs ~16s, so its timeout must not be tight enough to fire on a
Welcome that was merely slow.

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

**Renaming the bot** = `WAKE_WORDS`, `WAKE_WORDS_NEAR` and `STT_PROMPT` in `.env`, all
three together (each falls back to the shipped Russian defaults when blank; entries are
normalized, so `Алиса` in `.env` matches `алиса,` in a transcript). The failure this
invites is renaming only `WAKE_WORDS`: stage 1 then nominates mishearings of the *old*
name, nothing reaches stage 2, and the bot hears its name and does nothing — with every
list looking sane in isolation. `voice.CheckWakeWordConfig()` runs at startup and logs an
ERROR (so it reaches Telegram) if no wake word is covered by the near list.
The echo marker is **derived** from `STT_PROMPT`'s first sentence (`promptEcho`), not
listed separately — it used to be a second hand-kept copy, and a rename that updated only
the prompt would have disarmed the defence silently.

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

## Playback reports (POST /playback)
The AI service logs a track when it hands it over, which over-counts: a queue of five
that the listener skipped after two still logged five. Only the bot knows what was
actually heard, so it reports each track that produced audio — including tracks started
by slash command, which are just as honest a taste signal as the AI's picks.

- **`played_ms` counts frames handed to Discord**, never elapsed time
  (`stream.Controls.Frames` × `FrameMs`). A paused stream emits nothing, so pause time
  cannot leak in — the failure that would look perfectly plausible in the data.
  `stream/pause_test.go` drives the real ffmpeg loop to prove it.
- **A track that produced no audio reports nothing** (`frames <= 0`). That is the whole
  point: queued-but-unplayed tracks were the pollution.
- **Fire-and-forget**: reporting runs off the player loop, failures are swallowed (visible
  only under `AI_DEBUG`), no retries, no queue. Analytics must never delay music.
- **No dedup, no aggregation.** Two plays are two events; a repeat listen is itself the
  signal. `reason` is reported as observed — a skip is not a dislike, tracks get skipped
  for having just played.
- **`title`/`uploader`/`url` ride along** (optional fields). A track started by slash
  command never passes through the agent, so the service has only ever seen its id and
  stores the row with the title set to the raw YouTube id and no uploader — which is
  what an admin panel then shows. The bot holds the metadata regardless, so sending it
  is free. They stay optional: the service keeps its `track_id` fallback.
- Address: `PLAYBACK_SERVICE_ADDR`, falling back to `AI_SERVICE_ADDR`.

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

## Admin API + telemetry (`internal/adminapi`, `internal/telemetry`)
Read-only, and deliberately so: nothing here can change playback or settings, so the
panel is safe to look at while diagnosing. It exists because the bot kept **nothing** —
working out why it ignored someone meant grepping hours of `docker logs` for `[stt]`
lines by eye, hundreds an hour from a single open microphone.

Endpoints, all under `/admin`: `health`, `state`, `events`, `stats/stt`, `stats/agent`.

- **`stats/agent` lives here, not in the AI service.** The bot sees what the server
  cannot: the calls that never arrived (connection refused, timeouts). Those are exactly
  the failures that look like "the bot ignored me" from the channel. The AI service was
  told not to build its own.
- **Recording never blocks the caller.** `Record` takes a short lock on the ring and
  hands the disk copy to a buffered channel; when that channel is full the event is
  **dropped**. Callers are on the voice receive loop, where waiting means losing Opus
  packets. Same rule as playback reports: analytics must never delay the music.
- **Every aggregate reports its `window`.** The buffer is a fixed-size ring, so once it
  is full "how many times did this happen" silently becomes "…in the last N events". A
  response that did not say so would be lying by omission — hence `truncated`.
- **Transcripts are gated twice.** Whether they are captured follows `STT_LOG_LEVEL`
  (reused rather than adding a second knob that could disagree with it and leave speech
  recorded after somebody thought they had turned it off); whether they are served is
  decided by role — **owner only**. Everyone else gets the same event with the decisions
  intact and the words removed, which is all an operator needs.

**Auth contract, shared with the gateway and the AI service:** `X-Admin-Token` (compared
with `subtle.ConstantTimeCompare`), plus `X-Admin-User-Id`, `X-Admin-User-Name`,
`X-Admin-Role` (`viewer` < `moderator` < `owner`). The identity headers are **ignored
without a valid token** and an unrecognised role becomes `viewer`, never `owner`: on the
wire those headers are unauthenticated hints, and trusting them would let anything that
reaches the port inside the compose network declare itself owner. User authentication
(Discord OAuth2) belongs to the gateway; the gateway also strips any client-supplied
`X-Admin-*` before adding its own.

**Why `snapshot` was widened instead of adding a command kind.** `cmdSnapshot` is
answered in *two* places — `handle()` and inline in `playItem` — the second so that
asking during playback does not block until the track ends. A new kind would have had to
remember `playItem`, and forgetting it would hang every request made while music was
playing, which is most of them. Widening the existing reply keeps both paths correct by
construction. `State` also takes a `context`: a wedged player must yield a 503, not a
hung panel, and `unavailable` is reported distinctly from "nothing playing".

## The panel's gateway (`internal/adminui`, `cmd/adminui`)
The only process in the stack meant to be reachable from outside the compose network,
which is why every security decision lives there. Separate binary from the bot on
purpose: the panel stays up while the bot restarts — exactly when someone wants to look
at it — and neither service ends up owning the other's settings.

- **Discord OAuth2, scope `identify` only.** No passwords are stored, revocation is a
  list edit, and the admins already have Discord accounts. The `state` parameter is
  mandatory: without it somebody could hand a victim a callback URL carrying the
  attacker's authorization code and silently log them into the wrong account.
- **Sessions are a signed cookie, no server-side table.** Signed, not encrypted — the
  payload is a user id, a name and a role, none of them secret; what matters is that
  the browser cannot *change* it. `HttpOnly`, `SameSite=Lax`, `Secure` when serving TLS.
- **The role is re-read from the env lists on every request**, never trusted from the
  cookie. Removing someone from `.env` takes effect at their next request instead of
  whenever their 12-hour session happens to lapse.
- **The proxy strips every `X-Admin-*` header the client sent before adding its own**
  (`stripAdminHeaders`, by prefix so a header added later is covered). Skipping this
  would let a browser send `X-Admin-Role: owner` and have it forwarded alongside our
  service token, which the backends trust. `TestForgedAdminHeadersAreStripped` is the
  test for this package.
- **The service token never reaches the browser.** It is attached server-side, so a
  stolen session cannot be replayed against the backends directly.
- **Access lists are env** (`ADMIN_OWNER_IDS` / `_MODERATOR_IDS` / `_VIEWER_IDS`), not a
  database: adding someone is a `.env` edit and a restart, and there is no bootstrap
  problem or "last owner removed" trap. Empty lists are a **startup refusal** — "no
  list" must never read as "let anyone in". A duplicated id gets the strongest role.
- **TLS is opt-in via `ADMIN_DOMAIN`**: set it and the gateway serves `:443` with
  automatic Let's Encrypt certs (plus `:80` for the ACME challenge, and `HostWhitelist`
  so it is not a certificate mill for anyone else's SNI); leave it blank and it serves
  plain HTTP, which is what you want while reaching the panel over an SSH tunnel.

**`internal/adminauth` must keep zero imports.** The gateway used to reach the shared
header names through `internal/adminapi`, which imports `internal/voice`, which imports
the cgo opus bindings — so `CGO_ENABLED=0 go build ./cmd/adminui` failed outright and
the container image could not be built. Anything that both the gateway and the bot need
belongs in that leaf package.

**The panel is hand-written HTML/CSS/JS with no bundler**, embedded via `embed.FS`.
Images are built on the server — one core, 2GB, shared with Postgres, Redis and Vosk —
where a Vite build is slow enough to risk being OOM-killed. The JS builds DOM nodes and
never assigns `innerHTML`: track titles and transcripts are other people's text.

## KNOWN GOTCHAS
- **AI session-memory poisoning** (biggest live issue): the AI service keeps
  conversation memory **per guild, not per channel**. Its session key is
  `v7:<guild_id>` (falling back to user id, then `"global"`), and the `channel_id` the
  bot sends on every `/agent` call is accepted and then never used. If the service once
  returns bad output (English canned text, `play_track` instead of `play`, empty tool
  args, no tool_call), it few-shot copies its own bad history and keeps failing **for
  that whole server**.
  **This entry used to claim the memory was per channel**, citing "same request in a
  FRESH channel works" as proof. The schema refutes it — a second channel of the same
  guild reads the same row — so that test cannot have shown what it was recorded as
  showing; most likely it was run on a different *server*. Which leaves the cause
  genuinely open: memory is still the prime suspect, but the channel-isolation evidence
  for it does not hold, and nothing has re-established it. Treat "reset the memory and
  it's fixed" as untested until seen live again.
  To test the bot, use a different SERVER, or clear the session with
  `POST /agent/forget` (it wipes both Postgres and Redis). Fix is service-side: don't
  feed raw assistant outputs back into memory.
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

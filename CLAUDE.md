# discordAudio — project guide (for Claude)

A Go Discord music + AI-DJ bot. Users control it by slash commands **and by voice**
(wake word "Марина"). It orchestrates two external Python services.

## Services & ports (all local)
| Service | Env var | Port | Purpose |
|---|---|---|---|
| AI agent + TTS | `AI_SERVICE_ADDR` | 8000 | `POST /agent` (decides actions), `POST /tts` (→ Piper 4215) |
| Search/media | `SEARCH_SERVICE_ADDR` | 9000 | `/search`, `/stream`, `/playlist` (YouTube etc.) |
| Whisper STT (HTTP) | `WHISPER_SERVER_ADDR` | 9010 | `onerahmet/openai-whisper-asr-webservice`, `POST /asr` |
| Vosk STT (websocket) | `VOSK_SERVER_ADDR` | 2700 | `alphacep/kaldi-ru`, wake-word gate + (optionally) command text |

Python sources live outside this repo: `C:\Users\sok20\PycharmProjects\DiscordAiService`
and `...\DsBotSearchService`.

## Env vars
- `AI_SERVICE_ADDR`, `AI_SERVICE_API_KEY`, `SEARCH_SERVICE_ADDR` — service endpoints.
- `WHISPER_SERVER_ADDR`, `VOSK_SERVER_ADDR` — STT backends.
- `STT_VOSK_ONLY=1` — use Vosk for the command text too (skip whisper). Vosk is better
  at Russian but has no punctuation.
- `STT_LOG_LEVEL` — 0 silent / 1 commands (default) / 2 all transcripts.
- `AI_DEBUG=1` — log raw `[ai] ->` request / `[ai] <-` response to the AI service.
- `DJ_BREAK_EVERY` — DJ comment every N tracks (default 3).
- `WHISPER_BIN`/`WHISPER_MODEL` — DEAD (old exec path removed); safe to delete.

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
3. Command text = Vosk (`STT_VOSK_ONLY`) or whisper. Full text incl. the wake word is
   sent to the AI.
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

# discordAudio

A Go Discord **music + AI-DJ bot**. Control it with slash commands **or by voice** — say
the wake word **"Марина"** followed by a request, and an AI agent decides what to do
(play, queue, pause, skip, change volume, describe the queue).

## How it works

Music reaches the queue two ways:

```
Voice / AI:  Discord voice ─► bot ─► Vosk (wake word, local) ─► OpenAI STT ─► AI agent (/agent, tools)
                                                                                  └─► returns tracks
Direct:      /play ─► bot ─► search service (/search)  ─► tracks

Then for every queued track:
             bot ─► search service (/stream, resolve URL) ─► ffmpeg ─► voice channel
             AI spoken replies ─► TTS (/tts) ─► voice channel
```

The bot talks to the **search service directly** — for `/play` (search) and to resolve
the stream URL of every track before playback — and to the **AI agent** for voice /
`/prompt` requests (the agent runs its own search and returns tracks). The **wake-word
gate runs locally** (Vosk), so only utterances that start with "Марина" are sent to the
command STT; the AI agent and search are separate Python services.

## Services

| Component                                                                   | Default addr | Role |
|-----------------------------------------------------------------------------|---|---|
| **[DiscordAiService](https://github.com/IlyaGoloborodko/DiscordAiService)** | `http://127.0.0.1:8000` | `POST /agent` (decisions), `POST /tts` (Piper) |
| **[media-source-service](https://github.com/IlyaGoloborodko/media-source-service)**                                                | `http://127.0.0.1:9000` | `/search`, `/stream`, `/playlist` |
| **OpenAI** (command STT)                                                    | cloud | `gpt-4o-mini-transcribe`, set `OPENAI_API_KEY` |
| **Vosk** (wake word)                                                        | `ws://127.0.0.1:2700` | `alphacep/kaldi-ru`, always local |

## Prerequisites

- **Go** 1.25+, a **C toolchain** (CGO — for `gopus`/libopus), and **ffmpeg** on `PATH`.
- **Docker** for the STT containers.
- The two Python services running (DiscordAiService, DsBotSearchService).
- A Discord bot with the **voice** intents; it must join non-deaf to receive audio.

## Setup

1. **Config**
   ```
   cp .env.example .env      # then fill DISCORD_TOKEN, AI_SERVICE_ADDR, etc.
   ```

2. **Wake-word gate** (PowerShell, one line)
   ```powershell
   docker run -d --name vosk-ru --restart unless-stopped -p 2700:2700 alphacep/kaldi-ru
   ```
   > This is the only local STT container. Commands themselves go to OpenAI, so
   > `OPENAI_API_KEY` is required for voice control — there is no local fallback. The
   > gate runs first, so only utterances containing the wake word leave the machine.

3. **Run the bot**
   ```
   go build -o bot.exe ./cmd/bot   # then run ./bot.exe (fast restarts)
   ```

## Commands

| Command | Action |
|---|---|
| `/play <query>` | Search and queue music |
| `/prompt <text>` | Talk to the AI DJ (same as voice) |
| `/join` | Join your voice channel and start listening |
| `/pause` | Toggle pause / resume |
| `/volume [1-10]` | Show or set volume |
| `/skip`, `/stop`, `/queue` | Skip, stop+clear, show queue |

**By voice:** `/join`, then say e.g. *"Марина, включи что-нибудь бодрое"*,
*"Марина, поставь на паузу"*, *"Марина, сделай погромче"*, *"Марина, что в очереди?"*.

## Environment

See [`.env.example`](.env.example) for the full list. Key ones: `DISCORD_TOKEN`,
`AI_SERVICE_ADDR`, `SEARCH_SERVICE_ADDR`, `OPENAI_API_KEY`, `VOSK_SERVER_ADDR`,
`STT_VOSK_ONLY`, `STT_LOG_LEVEL`, `AI_DEBUG`, `DJ_BREAK_EVERY`.

## Notes

- **Vendored discordgo** (`third_party/discordgo`): Discord mandates the DAVE E2EE
  protocol on voice since 2026-03-02, so upstream can't connect. This is a patched fork
  with DAVE send **and** receive (the bot decrypts other users' audio to transcribe it).
- Editing `third_party/` forces a full rebuild; otherwise incremental builds are ~0.4s.
- See [`CLAUDE.md`](CLAUDE.md) for architecture details and known gotchas.

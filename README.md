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
- The two Python services running (DiscordAiService, media-source-service).
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

## Deployment (server)

The whole stack — bot, AI service, search service and their dependencies — comes up
with one command. This repository's `docker-compose.yml` pulls in the other two
services' compose files, so each stays runnable on its own.

Step-by-step for a fresh Debian box — swap, Docker, clone, config — is in
[deploy/SERVER.md](deploy/SERVER.md). The short version:

**Bootstrap, once per server.** Clone the three repositories side by side:

```bash
mkdir -p /srv/discord && cd /srv/discord
git clone <discordAudio>       && git clone <DiscordAiService> && git clone <media-source-service>
```

Then put each service's `.env` in place — **each service reads its own, from its own
directory**; there is no shared one:

| File | Must contain at minimum |
|---|---|
| `discordAudio/.env` | `DISCORD_TOKEN`, `OPENAI_API_KEY` |
| `DiscordAiService/.env` | `POSTGRES_PASSWORD` (compose refuses to start without it) |
| `media-source-service/.env` | optional; defaults work |
| `media-source-service/cookies.txt` | YouTube cookies, **LF line endings** |

Service addresses (`AI_SERVICE_ADDR`, `REDIS_ADDR`, …) are *not* taken from `.env` in
the stack: compose overrides them with container names, so the same `.env` keeps
working for a local run against `127.0.0.1`.

These files are gitignored and deploys never overwrite them — `git reset --hard`
leaves untracked files alone, so this is a one-time step.

**Deploying.** Run the *CI and Deploy* workflow (or, on the server):

```bash
cd /srv/discord/discordAudio && docker compose up -d --build
```

The workflow updates all three checkouts and refuses to start a half-configured
stack: a missing repository, an empty `.env` or an absent `cookies.txt` fails the
run with a message naming the file. That is deliberate — a stack that comes up
missing one service looks like a network fault from the bot's side.

Secrets used by the workflow: `SSH_HOST`, `SSH_USER`, `SSH_PRIVATE_KEY`, and
`APP_PATH` (the path to *this* repository, e.g. `/srv/discord/discordAudio`; the
other two are found next to it).

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

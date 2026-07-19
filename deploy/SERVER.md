# Deploying the stack on a fresh Debian server

Written for a 2GB VPS. Steps 1–6 are the one-off bootstrap; after that a deploy
is one command (or the *CI and Deploy* workflow).

Everything the stack listens on is bound to `127.0.0.1`. Nothing needs opening in
a firewall — the bot only makes outbound connections to Discord.

---

## 1. Update the system

```bash
sudo apt-get update && sudo apt-get upgrade -y
```

## 2. Swap — do not skip this on 2GB

The server compiles the Go bot itself, and CGO (libopus) plus the 16 `circl`
packages the DAVE protocol pulls in are the memory-hungry part. Without swap the
first build is likely to die as `signal: killed`, which looks like a compiler bug
and is not one.

```bash
sudo fallocate -l 4G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
free -h          # confirm the swap line is there
```

## 3. Docker (official repository, not Debian's)

`docker-compose.yml` uses top-level `include`, which needs Compose **v2.20+**.
Debian's own `docker.io` package ships an older plugin, and the failure is a
confusing "unsupported attribute" rather than a version complaint.

```bash
sudo apt-get install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

docker compose version    # must be >= 2.20
```

Run docker without sudo (log out and back in afterwards):

```bash
sudo usermod -aG docker $USER
```

Cap the logs, or a chatty container fills a small disk over a few weeks:

```bash
sudo tee /etc/docker/daemon.json > /dev/null <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
EOF
sudo systemctl restart docker
```

## 4. Clone the three repositories side by side

The directory names are what compose and the deploy workflow look for, and they
match the repository names — a plain `git clone` produces the right layout:

```bash
sudo mkdir -p /srv/discord && sudo chown $USER:$USER /srv/discord
cd /srv/discord

git clone https://github.com/IlyaGoloborodko/discordAudio.git
git clone https://github.com/IlyaGoloborodko/DiscordAiService.git
git clone https://github.com/IlyaGoloborodko/media-source-service.git
```

Private repositories over HTTPS will ask for credentials — use a GitHub personal
access token as the password, or set up an SSH deploy key per repository and
clone the `git@github.com:` URLs instead.

## 5. Configuration — one .env per service

Each service reads its own `.env` from its own directory; there is no shared one.
They are gitignored, so copy them from your machine (PowerShell):

```powershell
scp .env user@server:/srv/discord/discordAudio/.env
scp ..\..\PycharmProjects\DiscordAiService\.env user@server:/srv/discord/DiscordAiService/.env
scp ..\..\PycharmProjects\media-source-service\.env user@server:/srv/discord/media-source-service/.env
scp ..\..\PycharmProjects\media-source-service\cookies.txt user@server:/srv/discord/media-source-service/cookies.txt
```

Then fix these up on the server:

| File | Change |
|---|---|
| `discordAudio/.env` | **Remove `AI_SERVICE_PATH` / `SEARCH_SERVICE_PATH`** — they point at a Windows layout. The defaults are the server one. |
| `DiscordAiService/.env` | **Add `POSTGRES_PASSWORD=...`** — compose refuses to start without it, on purpose. |
| `media-source-service/cookies.txt` | Must have **LF** line endings. `sed -i 's/\r$//' cookies.txt` after copying from Windows. |

Service addresses (`AI_SERVICE_ADDR`, `REDIS_ADDR`, …) need no editing: compose
overrides them with container names, so the same file keeps working locally.

## 6. Start it

```bash
cd /srv/discord/discordAudio
docker compose up -d --build
```

First run takes a while: it compiles Go with CGO, builds two Python images and
downloads the Vosk model.

## 7. Check it came up

```bash
docker compose ps                         # everything Up, dependencies healthy
docker compose logs -f bot                # expect "Bot is up!"
docker compose logs ai | grep -i upgrade  # alembic migrations ran
docker stats --no-stream                  # expect roughly 1.0-1.2 GiB total
```

Then say the wake word in Discord and watch `docker compose logs -f bot` for a
line containing `VOSK="..." near=true`.

---

## Later deploys

```bash
cd /srv/discord/discordAudio && docker compose up -d --build
```

Or run the **CI and Deploy** workflow, which updates all three checkouts over SSH
and refuses to start a half-configured stack. It needs these repository secrets:

| Secret | Value |
|---|---|
| `SSH_HOST` | server address |
| `SSH_USER` | the user that owns `/srv/discord` |
| `SSH_PRIVATE_KEY` | private key for that user |
| `APP_PATH` | `/srv/discord/discordAudio` |

## If the build gets killed

`signal: killed` during the Go build means memory. Check `free -h` first — swap
is step 2 for a reason. If swap is present and it still dies, build the images
somewhere with more memory and push them to a registry, then have the server pull
instead of build.

## Rolling back the wake-word gate

The gate runs a small Vosk model (222MiB) instead of `alphacep/kaldi-ru`
(5.28GiB) — measured identical on the same clips, see
`deploy/vosk-gate/Dockerfile`. It will not fit alongside everything else on a 2GB
box, but if the small model turns out to be worse on real microphone audio, swap
it back in `docker-compose.yml`:

```yaml
  vosk:
    image: alphacep/kaldi-ru:latest   # instead of build: ./deploy/vosk-gate
```

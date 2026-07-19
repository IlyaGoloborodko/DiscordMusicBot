# ---------- BUILD STAGE ----------
FROM golang:1.25 AS builder

RUN apt-get update && apt-get install -y \
    gcc \
    libc6-dev \
    pkg-config \
    libopus-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY go.mod go.sum ./
# third_party must land before `go mod download`: go.mod redirects discordgo to
# ./third_party/discordgo (the vendored DAVE fork), and resolving that replace
# means reading its go.mod. Without this the download step fails outright — it
# is a dependency, not just source.
COPY third_party ./third_party
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o bot ./cmd/bot

# ---------- RUNTIME STAGE ----------
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    ffmpeg \
    ca-certificates \
    libopus0 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/bot .

CMD ["./bot"]
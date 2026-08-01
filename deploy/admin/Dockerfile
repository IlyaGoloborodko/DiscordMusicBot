# The admin panel's gateway. Build context is the repository root:
#
#   docker build -f deploy/admin/Dockerfile .
#
# ---------- BUILD STAGE ----------
FROM golang:1.25 AS builder

WORKDIR /app

COPY go.mod go.sum ./
# Same reason as the bot's Dockerfile: go.mod redirects discordgo to
# ./third_party/discordgo, and resolving that replace means reading its go.mod,
# so it has to be present before `go mod download`.
COPY third_party ./third_party
RUN go mod download

COPY . .

# CGO off, unlike the bot. This binary has no opus and no ffmpeg — it is a
# gateway — so a static build means the runtime stage needs nothing but
# certificates, and the image lands in the low tens of megabytes on a VPS that
# is already sharing 2GB between five containers.
#
# The panel's HTML, CSS and JavaScript are compiled in through embed, so there
# is nothing to COPY into the runtime stage and nothing to mount.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o adminui ./cmd/adminui

# ---------- RUNTIME STAGE ----------
FROM alpine:3.20

# ca-certificates is not optional: without it the OAuth2 calls to discord.com
# and the ACME requests to Let's Encrypt both fail TLS verification.
RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /app/adminui .

# Not root. This is the one process in the stack that is reachable from the
# internet, so it gets the least it can work with.
RUN adduser -D -u 10001 admin && mkdir -p /data && chown admin /data
USER admin

CMD ["./adminui"]

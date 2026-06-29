module discordAudio

go 1.25

require (
	github.com/bwmarrin/discordgo v0.29.1-0.20260214123928-f43dd94faaac
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	github.com/joho/godotenv v1.5.1
	github.com/redis/go-redis/v9 v9.19.0
	layeh.com/gopus v0.0.0-20210501142526-1ee02d434e32
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/gorilla/websocket v1.4.2 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.32.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/yeongaori/discordgo v0.0.0-20260307092356-fd09989565b3

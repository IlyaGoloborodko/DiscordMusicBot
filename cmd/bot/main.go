package main

import (
	"context"
	"discordAudio/internal/discord"
	"discordAudio/internal/logger"
	"discordAudio/internal/player"
	"discordAudio/internal/storage"
	"discordAudio/internal/voice"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"discordAudio/internal/config"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

// initEnv loads .env and lets it win over variables already in the environment.
// Overload, not Load: Load leaves pre-existing variables alone, so a stale
// machine-wide value silently beats the file you just edited — a Windows User-level
// OPENAI_API_KEY shadowed the .env one and every request came back 401 with a key
// that appeared nowhere in the repo.
func initEnv() {
	if err := godotenv.Overload(); err != nil {
		log.Println(".env file not found, using system environment variables")
	}
}

// loadLogger wires up Telegram logging if it is configured. It is optional: a
// bot with no token runs exactly as before, minus the phone notifications, and
// killing the music because a logging destination is missing would be the wrong
// trade. Console logging always works.
func loadLogger(ctx context.Context, rdb *redis.Client) {
	tgCfg, err := logger.LoadTelegramConfig()
	if err != nil {
		log.Printf("telegram logging is off: %v", err)
		return
	}

	tgLogger, err := logger.NewTelegramLogger(tgCfg, rdb)
	if err != nil {
		log.Printf("telegram logging is off: %v", err)
		return
	}

	logger.Init(ctx, tgLogger)
	log.Printf("logging ready: console=%s telegram=%s", logger.ConsoleLevel(), logger.TelegramLevel())
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initEnv()
	wd, _ := os.Getwd()
	log.Println("working dir:", wd)

	redisStorage := storage.DefaultRedisConfig()
	rdb, err := storage.NewClient(context.Background(), redisStorage)
	if err != nil {
		log.Fatalf("failed to connect to redis server: %v", err)
	}

	loadLogger(ctx, rdb)

	trackCache := voice.NewTrackCache(rdb)
	voice.InitTrackCache(trackCache)

	voice.InitPlayerManager(player.NewManager())

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN not set")
	}

	config.DebugGuildIDs = os.Getenv("DEBUG_GUIID")

	// Not fatal — slash commands and playback work without it — but voice control
	// does not, and there is no local fallback to quietly take over. Say so at
	// startup rather than letting it surface as a failed command much later.
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
		log.Println("WARNING: OPENAI_API_KEY not set — voice commands will not be transcribed")
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("error creating Discord session,", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuilds

	err = dg.Open()
	if err != nil {
		log.Fatal("error opening connection,", err)
	}

	err = discord.RegisterCommands(dg)
	if err != nil {
		log.Fatal("error register Discord commands,", err)
	}

	// Startup is INFO, so it only reaches Telegram under TG_LOG_LEVEL=INFO. It
	// used to go there unconditionally; at the default ERROR the phone now stays
	// quiet on a restart.
	logger.Infof("Bot is up!")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	cancel()

	dg.Close()
}

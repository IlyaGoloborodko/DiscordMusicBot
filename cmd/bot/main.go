package main

import (
	"context"
	"discordAudio/internal/adminapi"
	"discordAudio/internal/discord"
	"discordAudio/internal/logger"
	"discordAudio/internal/player"
	"discordAudio/internal/storage"
	"discordAudio/internal/telemetry"
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

// guildNames reads the guild list out of discordgo's state cache, which the
// library keeps current from gateway events — so this costs no API call and can
// be called per request.
//
// The lock is discordgo's own: State embeds an RWMutex and mutates the guild
// objects in place on GUILD_CREATE (`*g = *guild`), so reading Name without
// holding it is a genuine race, not a formality.
func guildNames(s *discordgo.Session) []adminapi.Guild {
	if s == nil || s.State == nil {
		return nil
	}
	s.State.RLock()
	defer s.State.RUnlock()

	out := make([]adminapi.Guild, 0, len(s.State.Guilds))
	for _, g := range s.State.Guilds {
		if g != nil {
			out = append(out, adminapi.Guild{ID: g.ID, Name: g.Name})
		}
	}
	return out
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

	// Kept in a variable rather than built inline: the admin API reports on these
	// players, and until now the only reference in the process was the
	// unexported global inside package voice.
	players := player.NewManager()
	voice.InitPlayerManager(players)
	voice.CheckWakeWordConfig()

	events := telemetry.New(telemetry.ConfigFromEnv())
	telemetry.Init(events)
	defer events.Close()

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

	// The admin API is optional and never fatal: a misconfigured diagnostic must
	// not stop the bot. The one refusal that IS worth failing on is an empty
	// token, and even then we only decline to serve — see adminapi.New.
	admin, err := adminapi.NewFromEnv(adminapi.Deps{
		Players:     players,
		Events:      events,
		VoiceStatus: voice.Status,
		Guilds:      func() []adminapi.Guild { return guildNames(dg) },
	})
	if err != nil {
		logger.Errorf("[admin] not starting: %v", err)
	}
	admin.Start()

	// Startup is INFO, so it only reaches Telegram under TG_LOG_LEVEL=INFO. It
	// used to go there unconditionally; at the default ERROR the phone now stays
	// quiet on a restart.
	logger.Infof("Bot is up!")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	// Stop serving before cancel(): a request in flight reads player state, and
	// letting those finish first keeps the last thing in the log a clean stop
	// rather than a burst of failures caused by our own shutdown.
	admin.Shutdown(context.Background())
	cancel()

	dg.Close()
}

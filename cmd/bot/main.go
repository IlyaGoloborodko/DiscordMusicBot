package main

import (
	"context"
	"discordAudio/internal/discord"
	"discordAudio/internal/logger"
	"discordAudio/internal/radio"
	"discordAudio/internal/storage"
	"log"
	"os"
	"os/signal"
	"syscall"

	"discordAudio/internal/config"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

func initEnv() {
	if err := godotenv.Load(); err != nil {
		log.Println(".env file not found, using system environment variables")
	}

	if err := radio.LoadAllStations(); err != nil {
		log.Printf("failed to load station URLs: %v", err)
	} else {
		log.Printf("loaded %d stations", len(radio.AllStations))
	}
}

func loadLogger(ctx context.Context) {
	tgCfg, err := logger.LoadTelegramConfig()
	if err != nil {
		log.Fatal(err)
	}

	redisStorage := storage.DefaultRedisConfig()
	rdb, err := storage.NewClient(context.Background(), redisStorage)
	if err != nil {
		log.Fatalf("failed to connect to redis server: %v", err)
	}

	tgLogger, err := logger.NewTelegramLogger(tgCfg, rdb)
	if err != nil {
		log.Fatal(err)
	}

	logger.Init(ctx, tgLogger)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wd, _ := os.Getwd()
	log.Println("working dir:", wd)

	initEnv()
	loadLogger(ctx)

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN not set")
	}

	config.DebugGuildID = os.Getenv("DEBUG_GUIID")

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("error creating Discord session,", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuilds

	err = dg.Open()
	if err != nil {
		log.Fatal("error opening connection,", err)
	}

	log.Println("Bot is up!")

	err = discord.RegisterCommands(dg)
	if err != nil {
		log.Fatal("error register Discord commands,", err)
	}

	err = logger.Send("Bot is up!")
	if err != nil {
		log.Fatal("error sending init success log,", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	cancel()

	dg.Close()
}

package main

import (
	"discordAudio/internal/discord"
	"discordAudio/internal/radio"
	"log"
	"os"
	"os/signal"
	"syscall"

	"discordAudio/internal/config"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Println(".env file not found, using system environment variables")
	}

	if err := radio.LoadAllStations(); err != nil {
		log.Printf("failed to load station URLs: %v", err)
	} else {
		log.Printf("loaded %d stations", len(radio.AllStations))
	}
}

func main() {
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

	//dg.AddHandler(discord.MessageHandler)

	err = dg.Open()
	if err != nil {
		log.Fatal("error opening connection,", err)
	}
	log.Println("Bot is up!")

	err = discord.RegisterCommands(dg)
	if err != nil {
		log.Fatal("error register Discord commands,", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	dg.Close()
}

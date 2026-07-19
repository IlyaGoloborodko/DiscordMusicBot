package discord

import (
	"discordAudio/internal/config"
	"discordAudio/internal/logger"
	"discordAudio/internal/voice"
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
)

// volumeMin is the minimum for the /volume level option (MinValue is *float64).
var volumeMin float64 = 1

var (
	commands = []*discordgo.ApplicationCommand{
		{
			Name:        "play",
			Description: "play Music",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:         "query",
					Description:  "Type something",
					Type:         discordgo.ApplicationCommandOptionString,
					Required:     true,
					Autocomplete: true,
				},
			},
		},
		{
			Name:        "prompt",
			Description: "say something to AI",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "query",
					Description: "Type something",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
				},
			},
		},
		{
			Name:        "join",
			Description: "Join your voice channel and start listening",
		},
		{
			Name:        "pause",
			Description: "Pause or resume playback",
		},
		{
			Name:        "volume",
			Description: "Show or set playback volume (1-10)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "level",
					Description: "Volume level, 1-10",
					Type:        discordgo.ApplicationCommandOptionInteger,
					Required:    false,
					MinValue:    &volumeMin,
					MaxValue:    10,
				},
			},
		},
		{
			Name:        "skip",
			Description: "Skip to the next track",
		},
		{
			Name:        "queue",
			Description: "Show the current queue",
		},
		{
			Name:        "stop",
			Description: "Stop playing and clear the queue",
		},
	}

	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"play": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.PlayMusic(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Play command: %v", err))
			}
		},
		"prompt": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.ProcessPrompt(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Prompt command: %v", err))
			}
		},
		"join": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.Join(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Join command: %v", err))
			}
		},
		"pause": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.Pause(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Pause command: %v", err))
			}
		},
		"volume": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.Volume(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Volume command: %v", err))
			}
		},
		"skip": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.Skip(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Skip command: %v", err))
			}
		},
		"queue": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.Queue(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Queue command: %v", err))
			}
		},
		"stop": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.Stop(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Stop command: %v", err))
			}
		},
	}
)

// RegisterCommands publishes the slash commands, either into the guilds named by
// DEBUG_GUIID or globally when it is empty. See config.CommandGuildIDs.
func RegisterCommands(s *discordgo.Session) error {
	guilds := config.CommandGuildIDs()

	// Bulk overwrite, not one create per command: it replaces the WHOLE command
	// set for the scope, so a command dropped from `commands` disappears from
	// Discord. ApplicationCommandCreate only ever adds, which is why /radio
	// survived for months after its code was deleted — nothing ever removed it,
	// and nobody could tell from the source that it was still being served.
	want := commands
	if !config.SupportCommands {
		// An empty set is how you unpublish everything; the old code registered
		// each command and then deleted it again, which briefly published them.
		want = nil
	}

	RegisteredCommands = nil
	for _, guildID := range guilds {
		created, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, guildID, want)
		if err != nil {
			logger.Errorf("cannot publish commands in guild %q: %v", guildID, err)
			continue
		}
		RegisteredCommands = append(RegisteredCommands, created...)
	}

	if guilds[0] == "" {
		log.Printf("[discord] registered %d commands globally (Discord may take up to an hour to show them on every server)", len(RegisteredCommands))
	} else {
		log.Printf("[discord] registered %d commands in guilds %v — they exist ONLY there; clear DEBUG_GUIID to publish everywhere", len(RegisteredCommands), guilds)
	}

	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		// Запускаем обработку каждой команды в отдельной горутине
		switch i.Type {
		case discordgo.InteractionApplicationCommandAutocomplete:
			go func() {
				if err := voice.Search(s, i, i.ApplicationCommandData().Name); err != nil {
					fmt.Println(err)
				}
			}()
		case discordgo.InteractionApplicationCommand:
			cmdName := i.ApplicationCommandData().Name
			h, ok := commandHandlers[cmdName]
			if !ok {
				return
			}

			if commandNeedsDeferredResponse(cmdName) {
				if err := deferInteractionResponse(s, i); err != nil {
					logger.Send(fmt.Sprintf("error deferring %s command: %v", cmdName, err))
					return
				}
			}
			go h(s, i)
		}
	})

	return nil
}

func commandNeedsDeferredResponse(name string) bool {
	switch name {
	case "play", "prompt":
		return true
	default:
		return false
	}
}

func deferInteractionResponse(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

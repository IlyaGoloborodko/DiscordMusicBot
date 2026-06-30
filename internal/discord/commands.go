package discord

import (
	"discordAudio/internal/config"
	"discordAudio/internal/logger"
	"discordAudio/internal/voice"
	"fmt"

	"github.com/bwmarrin/discordgo"
)

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

var serverGuiid string

func RegisterCommands(s *discordgo.Session) error {
	if config.Debug {
		serverGuiid = config.DebugGuildID
	} else {
		serverGuiid = ""
	}

	RegisteredCommands = make([]*discordgo.ApplicationCommand, len(commands))
	for i, v := range commands {
		cmd, err := s.ApplicationCommandCreate(s.State.User.ID, serverGuiid, v)
		if err != nil {
			logger.Send(fmt.Sprintf("Cannot create '%v' command: %v", v.Name, err))
		}
		RegisteredCommands[i] = cmd
	}
	if !config.SupportCommands {
		for _, v := range RegisteredCommands {
			if v == nil {
				continue
			}
			err := s.ApplicationCommandDelete(s.State.User.ID, serverGuiid, v.ID)
			if err != nil {
				logger.Send(fmt.Sprintf("Cannot delete '%v' command: %v", v.Name, err))
			}
		}
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

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
			Name:        "radio",
			Description: "play Radio",
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
			Name:        "stop",
			Description: "Stop playing",
		},
	}

	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"play": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.PlayMusic(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Play command: %v", err))
			}
		},
		"radio": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.PlayRadio(s, i)
			if err != nil {
				logger.Send(fmt.Sprintf("error processing Play command: %v", err))
			}
		},
		"stop": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := voice.StopRadio(s, i)
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
		go func() {
			switch i.Type {
			case discordgo.InteractionApplicationCommandAutocomplete:
				if err := voice.Search(s, i, i.ApplicationCommandData().Name); err != nil {
					fmt.Println(err)
				}
			case discordgo.InteractionApplicationCommand:
				if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
					h(s, i)
				}
			}
		}()
	})

	return nil
}

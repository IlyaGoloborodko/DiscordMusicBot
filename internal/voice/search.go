package voice

import (
	"context"
	"discordAudio/internal/logger"
	"discordAudio/internal/music"
	"discordAudio/internal/radio"
	"fmt"

	"github.com/bwmarrin/discordgo"
)

func Search(s *discordgo.Session, i *discordgo.InteractionCreate, cmdName string) error {
	if len(i.ApplicationCommandData().Options) == 0 {
		return nil
	}
	query := i.ApplicationCommandData().Options[0].StringValue()

	switch cmdName {
	case "play":
		return SearchMusic(s, i, query)
	case "radio":
		return SearchRadio(s, i, query)
	default:
		return nil
	}
}

func SearchRadio(s *discordgo.Session, i *discordgo.InteractionCreate, query string) error {
	found := radio.SearchRadio(query)
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, 10)
	for _, r := range found {
		name := r.Name
		if r.Country != "" {
			name += " (" + r.Country + ")"
		}
		if len(name) > 100 {
			name = name[:100]
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  name,
			Value: r.StationUUID,
		})
	}

	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
}

func SearchMusic(s *discordgo.Session, i *discordgo.InteractionCreate, query string) error {
	tracks, err := music.Search(query)
	if err != nil {
		logger.Send(fmt.Sprintf("SearchMusic-Search error: %v", err))
		return err
	}

	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, 10)

	for _, t := range tracks {
		name := t.Title

		if t.Uploader != "" {
			name = fmt.Sprintf("%s — %s", t.Title, t.Uploader)
		}

		name = music.SafeName(name)

		if trackCache != nil {
			_ = trackCache.Save(
				context.Background(),
				t.ID,
				t.Title,
				t.Uploader,
				t.URL,
			)
		}

		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  name,
			Value: t.ID,
		})

		if len(choices) == 10 {
			break
		}
	}

	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
}

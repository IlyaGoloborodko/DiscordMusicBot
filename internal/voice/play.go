package voice

import (
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/music"
	"discordAudio/internal/stream"
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
)

func PlayMusic(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	ytVideoId := i.ApplicationCommandData().Options[0].StringValue()

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		return err
	}

	vc, found := discordUtils.FindVoiceConnection(s, i.GuildID)
	if !found || vc == nil {
		vc, err = JoinVoice(s, i)
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: "Сначала зайди в голосовой канал!",
			})
			return nil
		}
	}

	time.Sleep(800 * time.Millisecond)

	stream.StopChan()

	streamURL, err := music.GetStreamURL(ytVideoId)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "Не удалось получить аудиопоток.",
		})
		return err
	}

	go func() {
		if err := stream.StartStreaming(vc, streamURL); err != nil {
			log.Println("Error streaming music:", err)
		}
	}()

	if err := vc.Speaking(true); err != nil {
		log.Println("Error setting speaking:", err)
	}

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("🎧 Playing music: %s", ytVideoId),
	})
	return err
}

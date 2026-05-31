package voice

import (
	"context"
	"discordAudio/internal/aiService"
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/logger"
	"discordAudio/internal/stream"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
)

func ProcessPrompt(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	userMessage := i.ApplicationCommandData().Options[0].StringValue()

	var err error

	vc, found := discordUtils.FindVoiceConnection(s, i.GuildID)
	if !found || vc == nil {
		vc, err = JoinVoice(s, i)
		if err != nil || vc == nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: "Сначала зайди в голосовой канал!",
			})
			return nil
		}
	}

	stream.StopCurrentStream()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	ai := aiService.NewClient()

	audioStream, err := ai.Prompt(ctx, userMessage)
	if err != nil {
		cancel()
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "Не удалось получить аудиопоток от AI.",
		})
		return err
	}

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: "🔊",
	})
	if err != nil {
		cancel()
		_ = audioStream.Close()
		return err
	}

	go func() {
		defer cancel()
		defer audioStream.Close()

		if err := stream.StartStreamingReader(vc, audioStream); err != nil {
			logger.Send(fmt.Sprintf("AI stream error: %v", err))
		}
	}()

	return nil
}

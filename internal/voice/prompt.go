package voice

import (
	"context"
	"discordAudio/internal/aiService"
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/stream"

	"github.com/bwmarrin/discordgo"
)

func GetAiResponse(s *discordgo.Session, i *discordgo.InteractionCreate) error {
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

	stream.StopChan()

	ai := aiService.NewClient()

	_, err = ai.Prompt(context.Background(), userMessage)
	if err != nil {
		return err
	}
	//audioStream, err := tts.Synthesize(text)
	//if err != nil {
	//	return err
	//}
	//
	//go func() {
	//	if err := stream.StartStreaming(vc, audioStream); err != nil {
	//		logger.Send(fmt.Sprintf("stream error: %v", err))
	//	}
	//}()
	return err
}

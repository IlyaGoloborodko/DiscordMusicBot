package voice

import (
	"context"
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/logger"
	"discordAudio/internal/music"
	"discordAudio/internal/stream"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
)

func PlayMusic(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	ytVideoId := i.ApplicationCommandData().Options[0].StringValue()

	track, cacheErr := trackCache.Get(context.Background(), ytVideoId)
	if cacheErr != nil {
		logger.Send("Error on music playing: " + cacheErr.Error())
	}

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
			logger.Send(fmt.Sprintf("Error streaming music: %v", err))
		}
	}()

	if cacheErr == nil {
		_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("🎧 Играем: %s — %s", track.Title, track.Uploader),
		})
	} else {
		_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("🎧 Играем: %s", ytVideoId),
		})
	}
	return err
}

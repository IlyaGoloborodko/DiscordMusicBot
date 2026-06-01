package voice

import (
	"context"
	"discordAudio/internal/aiService"
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/logger"
	"discordAudio/internal/music"
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

	aiTextResponse, err := ai.Prompt(ctx, userMessage)
	if err != nil {
		cancel()
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "Ошибочка.",
		})
		return err
	}

	audioStream, err := ai.Tts(ctx, aiTextResponse.FullAnswerForTTS)
	if err != nil {
		cancel()
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "Ошибочка",
		})
		return err
	}

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: aiTextResponse.FullAnswerForTTS,
	})
	if err != nil {
		cancel()
		_ = audioStream.Close()
		return err
	}

	musicResultChan := make(chan promptMusicResult, 1)
	go func() {
		musicResultChan <- findPromptMusic(aiTextResponse.SearchStringForMusic)
	}()

	go func() {
		defer cancel()
		defer audioStream.Close()

		if err := stream.StartStreamingPCMReader(vc, audioStream, 22050, 1); err != nil {
			logger.Send(fmt.Sprintf("AI stream error: %v", err))
			return
		}
		musicResult := <-musicResultChan
		if musicResult.err != nil {
			logger.Send(fmt.Sprintf("AI music search error: %v", musicResult.err))
			return
		}

		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("🎧 Играем: [%s](%s) — %s", musicResult.track.Title, musicResult.track.URL, musicResult.track.Uploader),
		})

		if err := stream.StartStreaming(vc, musicResult.streamURL); err != nil {
			logger.Send(fmt.Sprintf("AI music stream error: %v", err))
		}
	}()
	return nil
}

type promptMusicResult struct {
	track     music.Track
	streamURL string
	err       error
}

func findPromptMusic(query string) promptMusicResult {
	tracks, err := music.Search(query)
	if err != nil {
		return promptMusicResult{err: err}
	}
	if len(tracks) == 0 {
		return promptMusicResult{err: fmt.Errorf("no tracks found for query: %s", query)}
	}

	track := tracks[0]
	streamURL, err := music.GetStreamURL(track.ID)
	if err != nil {
		return promptMusicResult{err: err}
	}

	return promptMusicResult{
		track:     track,
		streamURL: streamURL,
	}
}

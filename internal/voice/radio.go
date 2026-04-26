package voice

import (
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/logger"
	"discordAudio/internal/radio"
	"discordAudio/internal/stream"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
)

func PlayRadio(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	selectedUUID := i.ApplicationCommandData().Options[0].StringValue()

	var station *radio.Station
	for _, st := range radio.AllStations {
		if st.StationUUID == selectedUUID {
			station = &st
			break
		}
	}
	if station == nil {
		return nil
	}

	radioURL := station.StreamURL

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		return err
	}

	// Пытаемся найти существующее соединение
	vc, found := discordUtils.FindVoiceConnection(s, i.GuildID)
	if !found || vc == nil {
		// Если нет — подключаемся
		vc, err = JoinVoice(s, i)
		if err != nil {
			// Тут можно ответить пользователю
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: "Сначала зайди в голосовой канал!",
			})
			return nil
		}

		// Ждём, чтобы соединение реально установилось
		time.Sleep(time.Second)
	}

	time.Sleep(250 * time.Millisecond)

	// Останавливаем предыдущий стрим (если был)
	stream.StopChan()

	// Запускаем стрим в отдельной горутине
	go func() {
		if err := stream.StartStreaming(vc, radioURL); err != nil {
			logger.Send(fmt.Sprintf("Error streaming: %v", err))
		}
	}()

	// Включаем speaking
	if err := vc.Speaking(true); err != nil {
		logger.Send(fmt.Sprintf("Error setting speaking: %v", err))
	}

	// Отправляем сообщение пользователю
	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: "🎧 Стрим: " + station.Name + " " + station.Country,
	})
	return err
}

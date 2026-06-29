package voice

import (
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/stream"

	"github.com/bwmarrin/discordgo"
)

func Stop(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	vc, found := discordUtils.FindVoiceConnection(s, i.GuildID)
	// Если бота нет в канале, всё равно нужно ответить Дискорду, чтобы не было ошибки
	if !found || vc == nil {
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: "Я сейчас не в голосе.",
			},
		})
	}

	stream.StopCurrentStream()
	err := vc.Speaking(false)
	if err != nil {
		return err
	}

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: "⏹️ Воспроизведение остановлено.",
		},
	})
	if err != nil {
		return err
	}

	return nil
}

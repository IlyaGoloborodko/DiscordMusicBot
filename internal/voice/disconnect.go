package voice

import (
	"discordAudio/internal/discordUtils"

	"github.com/bwmarrin/discordgo"
)

func DisconnectChannel(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	vc, found := discordUtils.FindVoiceConnection(s, i.GuildID)
	if !found {
		return nil
	}
	// Stop listening before dropping the connection: OpusRecv is never closed, so
	// the run goroutine would otherwise sit on it forever.
	StopVoiceListener(vc)
	err := vc.Disconnect()
	if err != nil {
		return err
	}
	return nil
}

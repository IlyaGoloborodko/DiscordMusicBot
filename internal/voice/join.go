package voice

import (
	"github.com/bwmarrin/discordgo"
)

// Join is the /join command: the bot connects to the caller's voice channel and
// starts listening. It posts nothing to chat (even on error) — Discord still
// requires the interaction to be acknowledged, so we ack ephemerally and delete
// the response right away, leaving no visible message.
func Join(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})
	_, _ = ensureVoice(s, i)
	_ = s.InteractionResponseDelete(i.Interaction)
	return nil
}

func JoinVoice(s *discordgo.Session, i *discordgo.InteractionCreate) (*discordgo.VoiceConnection, error) {
	guild, err := s.State.Guild(i.GuildID)
	if err != nil {
		return nil, err
	}

	var vcID string
	userID := i.Member.User.ID
	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			vcID = vs.ChannelID
			break
		}
	}

	if vcID == "" {
		return nil, nil
	}

	// mute=false, deaf=false — deaf must be false so the bot receives audio
	// (vc.OpusRecv) for voice listening / speech-to-text.
	vc, err := s.ChannelVoiceJoin(i.GuildID, vcID, false, false)
	if err != nil {
		return nil, err
	}
	return vc, nil
}

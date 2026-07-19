package voice

import (
	"discordAudio/internal/discordUtils"
	"discordAudio/internal/player"

	"github.com/bwmarrin/discordgo"
)

var playerManager *player.Manager

func InitPlayerManager(m *player.Manager) {
	if m == nil {
		panic("playerManager is nil")
	}
	playerManager = m
}

// followup sends an ephemeral follow-up to a deferred interaction.
func followup(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
	})
}

// followupEphemeral closes a deferred interaction with a note only its author
// sees. Used when the real, public answer is posted by someone else (the player)
// and a public followup here would just duplicate it in the channel.
func followupEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}

// respond sends an immediate ephemeral response to a (non-deferred) interaction.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: content,
		},
	})
}

// ensureVoice returns the active voice connection for the guild, joining the
// caller's channel if the bot is not connected yet.
func ensureVoice(s *discordgo.Session, i *discordgo.InteractionCreate) (*discordgo.VoiceConnection, error) {
	vc, found := discordUtils.FindVoiceConnection(s, i.GuildID)
	if found && vc != nil {
		StartVoiceListener(s, vc, i.ChannelID)
		return vc, nil
	}
	vc, err := JoinVoice(s, i)
	if err == nil && vc != nil {
		StartVoiceListener(s, vc, i.ChannelID)
	}
	return vc, err
}

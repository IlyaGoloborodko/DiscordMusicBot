package voice

import (
	"context"
	"discordAudio/internal/aiService"
	"time"

	"github.com/bwmarrin/discordgo"
)

func ProcessPrompt(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	userMessage := i.ApplicationCommandData().Options[0].StringValue()

	vc, err := ensureVoice(s, i)
	if err != nil || vc == nil {
		followup(s, i, "Сначала зайди в голосовой канал!")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ai := aiService.NewClient()
	resp, err := ai.Agent(ctx, aiService.AgentRequest{
		Session: agentSession(i),
		Message: userMessage,
	})
	if err != nil {
		followup(s, i, "Ошибочка.")
		return err
	}

	// Acknowledge in the originating interaction; the spoken answer and music
	// playback are handled by the player.
	ack := resp.DisplayText
	if resp.Action == aiService.ActionClarify && resp.Clarification != "" {
		ack = resp.Clarification
	}
	if ack == "" {
		ack = "Ок"
	}
	followup(s, i, ack)

	playerManager.Get(s, vc, i.GuildID, i.ChannelID).ApplyAgent(resp)
	return nil
}

func agentSession(i *discordgo.InteractionCreate) aiService.AgentSession {
	sess := aiService.AgentSession{
		GuildID:   i.GuildID,
		ChannelID: i.ChannelID,
	}
	if i.Member != nil && i.Member.User != nil {
		sess.UserID = i.Member.User.ID
		sess.UserName = i.Member.User.Username
	}
	return sess
}

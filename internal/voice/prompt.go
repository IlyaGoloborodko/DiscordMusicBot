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

	// Duck the music while the AI thinks; restore afterwards.
	p := playerManager.Get(s, vc, i.GuildID, i.ChannelID)
	p.Duck()
	defer p.Unduck()

	// Give the agent the current queue so it can answer / voice questions about
	// what is playing and what is next.
	now, queue := p.Snapshot()

	ai := aiService.NewClient()
	resp, err := ai.Agent(ctx, aiService.AgentRequest{
		Session: agentSession(i),
		Message: userMessage,
		Trigger: aiService.TriggerUser,
		Context: map[string]any{
			"now_playing": now,
			"queue":       queue,
			"queue_len":   len(queue),
			"volume":      p.Volume(),
		},
		Tools: aiService.PlayerTools(),
	})
	if err != nil {
		followup(s, i, "Ошибочка.")
		return err
	}

	// Close the interaction privately. The visible answer — display text,
	// clarification, and the spoken reply — is posted by the player, which is the
	// single place that happens. A public followup here repeated the same line
	// (ApplyAgent → announce), so the channel showed every /prompt answer twice.
	followupEphemeral(s, i, "Ок")

	p.ApplyAgent(resp)
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

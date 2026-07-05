package voice

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

// Volume shows or sets playback volume on a 1-10 scale.
func Volume(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	p, ok := playerManager.Lookup(i.GuildID)
	if !ok {
		return respond(s, i, "Сейчас ничего не играет.")
	}

	opts := i.ApplicationCommandData().Options
	if len(opts) == 0 {
		return respond(s, i, fmt.Sprintf("🔊 Текущая громкость: %d/10", p.Volume()))
	}

	applied := p.SetVolume(int(opts[0].IntValue()))
	return respond(s, i, fmt.Sprintf("🔊 Громкость: %d/10", applied))
}

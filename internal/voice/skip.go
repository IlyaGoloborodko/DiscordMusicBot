package voice

import (
	"github.com/bwmarrin/discordgo"
)

func Skip(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	p, ok := playerManager.Lookup(i.GuildID)
	if !ok {
		return respond(s, i, "Сейчас ничего не играет.")
	}

	p.Skip()
	return respond(s, i, "⏭️ Дальше.")
}

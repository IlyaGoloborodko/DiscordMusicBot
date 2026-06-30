package voice

import (
	"github.com/bwmarrin/discordgo"
)

func Stop(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	p, ok := playerManager.Lookup(i.GuildID)
	if !ok {
		return respond(s, i, "Я сейчас ничего не играю.")
	}

	p.Stop()
	return respond(s, i, "⏹️ Остановлено, очередь очищена.")
}

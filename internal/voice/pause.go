package voice

import (
	"github.com/bwmarrin/discordgo"
)

// Pause toggles playback pause/resume for the guild's player.
func Pause(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	p, ok := playerManager.Lookup(i.GuildID)
	if !ok {
		return respond(s, i, "Сейчас ничего не играет.")
	}
	if p.TogglePause() {
		return respond(s, i, "⏸️ Пауза.")
	}
	return respond(s, i, "▶️ Продолжаем.")
}

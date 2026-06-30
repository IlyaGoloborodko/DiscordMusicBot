package voice

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

const maxQueueShown = 10

func Queue(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	p, ok := playerManager.Lookup(i.GuildID)
	if !ok {
		return respond(s, i, "Очередь пуста.")
	}

	nowPlaying, queue := p.Snapshot()

	var b strings.Builder
	if nowPlaying != "" {
		fmt.Fprintf(&b, "🎧 Сейчас: %s\n", nowPlaying)
	}

	if len(queue) == 0 {
		if nowPlaying == "" {
			return respond(s, i, "Очередь пуста.")
		}
		b.WriteString("Очередь пуста.")
		return respond(s, i, b.String())
	}

	b.WriteString("📋 Очередь:\n")
	for idx, title := range queue {
		if idx == maxQueueShown {
			fmt.Fprintf(&b, "…и ещё %d", len(queue)-maxQueueShown)
			break
		}
		fmt.Fprintf(&b, "%d. %s\n", idx+1, title)
	}

	return respond(s, i, strings.TrimRight(b.String(), "\n"))
}

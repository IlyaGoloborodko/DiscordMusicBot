package voice

import (
	"context"

	"discordAudio/internal/aiService"

	"github.com/bwmarrin/discordgo"
)

func PlayMusic(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	ytVideoID := i.ApplicationCommandData().Options[0].StringValue()

	vc, err := ensureVoice(s, i)
	if err != nil || vc == nil {
		followup(s, i, "Сначала зайди в голосовой канал!")
		return nil
	}

	track := aiService.Track{Provider: "youtube", ID: ytVideoID, Title: ytVideoID}
	if info, cacheErr := trackCache.Get(context.Background(), ytVideoID); cacheErr == nil {
		track.Title = info.Title
		track.Uploader = info.Uploader
		track.URL = info.Url
	}

	playerManager.Get(s, vc, i.GuildID, i.ChannelID).Enqueue([]aiService.Track{track})
	followup(s, i, "➕ В очередь: "+playTrackLabel(track))
	return nil
}

func playTrackLabel(t aiService.Track) string {
	if t.URL != "" {
		return "[" + t.Title + "](" + t.URL + ")"
	}
	return t.Title
}

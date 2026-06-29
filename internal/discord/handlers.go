package discord

import (
	"github.com/bwmarrin/discordgo"
)

func MessageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	//if m.Author.Bot {
	//	return
	//}
	//
	//content := strings.ToLower(m.Content)

	//if strings.HasPrefix(content, "!join") {
	//	err := voice.JoinVoice(s, m)
	//	if err != nil {
	//		log.Fatalf("error joining voice channel: %v", err)
	//	}
	//	return
	//}

	//if strings.HasPrefix(content, "!stop") {
	//	err := voice.Stop(s, m)
	//	if err != nil {
	//		log.Fatalf("error stop: %v", err)
	//	}
	//	return
	//}
	//
	//if strings.HasPrefix(content, "!disconnect") {
	//	err := voice.DisconnectChannel(s, m)
	//	if err != nil {
	//		log.Fatalf("error disconnecting channel: %v", err)
	//	}
	//}

	//if strings.HasPrefix(content, "!search ") {
	//	err := voice.Search(s, m)
	//	if err != nil {
	//		log.Fatalf("error searching: %v", err)
	//	}
	//	return
	//}
}

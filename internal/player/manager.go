package player

import (
	"sync"

	"github.com/bwmarrin/discordgo"
)

// Manager keeps one Player per guild.
type Manager struct {
	mu      sync.Mutex
	players map[string]*Player
}

func NewManager() *Manager {
	return &Manager{players: make(map[string]*Player)}
}

// Get returns the guild's player, creating it (bound to vc/channel) on first
// use, or rebinding an existing one to the current voice connection.
func (m *Manager) Get(s *discordgo.Session, vc *discordgo.VoiceConnection, guildID, channelID string) *Player {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.players[guildID]
	if !ok {
		p = newPlayer(s, vc, guildID, channelID)
		m.players[guildID] = p
		return p
	}
	p.bind(vc, channelID)
	return p
}

// Lookup returns an existing player without creating one.
func (m *Manager) Lookup(guildID string) (*Player, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.players[guildID]
	return p, ok
}

// All returns every live player, for callers that must report on all guilds at
// once rather than look one up.
//
// It copies the map out instead of taking a callback so the caller can do slow
// work — State() waits on each player's run loop — without holding m.mu. Holding
// it would mean one wedged player blocks every /join in the process.
func (m *Manager) All() []*Player {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*Player, 0, len(m.players))
	for _, p := range m.players {
		out = append(out, p)
	}
	return out
}

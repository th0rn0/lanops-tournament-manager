package bot

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

// Announce posts a plain-text message to the configured announcement channel.
// It is a no-op when no announcement channel ID is configured.
func (b *Bot) Announce(msg string) {
	if b.cfg.DiscordAnnouncementChannelID == "" {
		return
	}
	_, _ = b.session.ChannelMessageSend(b.cfg.DiscordAnnouncementChannelID, msg)
}

// NotifyTournamentCreated posts when a new tournament opens for registration.
func NotifyTournamentCreated(s *discordgo.Session, channelID string, tournamentID int64, name, webBase string) {
	if channelID == "" {
		return
	}
	_, _ = s.ChannelMessageSend(channelID, fmt.Sprintf(
		"📢 **New Tournament: %s** — registration is open!\nJoin now: %s/tournaments/%d",
		name, webBase, tournamentID,
	))
}

// NotifyBracketGenerated posts when a bracket is generated and the tournament goes live.
func NotifyBracketGenerated(s *discordgo.Session, channelID string, tournamentID int64, name, webBase string) {
	if channelID == "" {
		return
	}
	_, _ = s.ChannelMessageSend(channelID, fmt.Sprintf(
		"🏆 **%s** has started — the bracket is live!\nView: %s/tournaments/%d",
		name, webBase, tournamentID,
	))
}

// NotifyMatchReady posts when a match is ready to be played.
func NotifyMatchReady(s *discordgo.Session, channelID string, matchID, tournamentID int64, playerA, playerB, webBase string) {
	if channelID == "" {
		return
	}
	_, _ = s.ChannelMessageSend(channelID, fmt.Sprintf(
		"⚔️ **Match Ready!** %s vs %s (Match #%d)\nView: %s/tournaments/%d",
		playerA, playerB, matchID, webBase, tournamentID,
	))
}

// NotifyTournamentComplete posts when a tournament ends.
func NotifyTournamentComplete(s *discordgo.Session, channelID string, tournamentID int64, name, webBase string) {
	if channelID == "" {
		return
	}
	_, _ = s.ChannelMessageSend(channelID, fmt.Sprintf(
		"🎉 **%s** is complete! Check the final standings: %s/tournaments/%d",
		name, webBase, tournamentID,
	))
}

package bot

import (
	"context"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/th0rn0/lanops-tournament-manager/internal/models"
	"github.com/th0rn0/lanops-tournament-manager/internal/tournament"
)

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        "tournament",
		Description: "Tournament commands",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "list",
				Description: "List active tournaments",
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "join",
				Description: "Join a tournament",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "tournament_id",
						Description: "Tournament ID",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "leave",
				Description: "Leave a tournament",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "tournament_id",
						Description: "Tournament ID",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "info",
				Description: "Show tournament details",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "tournament_id",
						Description: "Tournament ID",
						Required:    true,
					},
				},
			},
		},
	},
	{
		Name:        "match",
		Description: "Match commands",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "result",
				Description: "Submit match result",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "match_id",
						Description: "Match ID",
						Required:    true,
					},
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "winner_participant_id",
						Description: "Winning participant ID",
						Required:    true,
					},
				},
			},
		},
	},
	{
		Name:        "admin",
		Description: "Admin commands (requires admin role)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "bracket-generate",
				Description: "Generate bracket for a tournament",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "tournament_id",
						Description: "Tournament ID",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "tournament-create",
				Description: "Create a new tournament",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "name",
						Description: "Tournament name",
						Required:    true,
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "format",
						Description: "single_elimination or double_elimination",
						Required:    true,
						Choices: []*discordgo.ApplicationCommandOptionChoice{
							{Name: "Single Elimination", Value: "single_elimination"},
							{Name: "Double Elimination", Value: "double_elimination"},
						},
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "result-override",
				Description: "Override a match result",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "match_id",
						Description: "Match ID",
						Required:    true,
					},
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "winner_participant_id",
						Description: "Winning participant ID",
						Required:    true,
					},
				},
			},
		},
	},
}

func (b *Bot) registerCommands() error {
	_, err := b.session.ApplicationCommandBulkOverwrite(b.session.State.User.ID, b.cfg.DiscordGuildID, commands)
	if err != nil {
		return fmt.Errorf("bulk overwrite commands: %w", err)
	}
	return nil
}

func (b *Bot) handleTournamentCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		respond(s, i, "Unknown subcommand")
		return
	}

	sub := data.Options[0]
	ctx := i.Interaction // context via interaction

	switch sub.Name {
	case "list":
		b.cmdTournamentList(s, i)
	case "join":
		tournamentID := sub.Options[0].IntValue()
		b.cmdTournamentJoin(s, i, ctx, tournamentID)
	case "leave":
		tournamentID := sub.Options[0].IntValue()
		b.cmdTournamentLeave(s, i, ctx, tournamentID)
	case "info":
		tournamentID := sub.Options[0].IntValue()
		b.cmdTournamentInfo(s, i, tournamentID)
	}
}

func (b *Bot) cmdTournamentList(s *discordgo.Session, i *discordgo.InteractionCreate) {
	rows, err := b.pool.Query(botCtx(), `
		SELECT id, name, format, status,
		       (SELECT COUNT(*) FROM participants p WHERE p.tournament_id = t.id) AS cnt
		FROM tournaments t
		WHERE status IN ('registration', 'active')
		ORDER BY created_at DESC LIMIT 10
	`)
	if err != nil {
		respond(s, i, "Database error")
		return
	}
	defer rows.Close()

	msg := "**Active Tournaments:**\n"
	found := false
	for rows.Next() {
		var id int64
		var name, format string
		var status models.TournamentStatus
		var cnt int
		if err := rows.Scan(&id, &name, &format, &status, &cnt); err != nil {
			continue
		}
		msg += fmt.Sprintf("• **#%d %s** — %s, %s, %d participants\n", id, name, format, status, cnt)
		found = true
	}
	if !found {
		msg = "No active tournaments."
	}
	respond(s, i, msg)
}

func (b *Bot) cmdTournamentJoin(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.Interaction, tournamentID int64) {
	// Look up user by Discord ID
	discordID := ""
	if i.Member != nil {
		discordID = i.Member.User.ID
	} else if i.User != nil {
		discordID = i.User.ID
	}
	if discordID == "" {
		respond(s, i, "Could not identify your Discord account.")
		return
	}

	var userID int64
	err := b.pool.QueryRow(botCtx(), `SELECT id FROM users WHERE discord_id = $1`, discordID).Scan(&userID)
	if err != nil {
		respond(s, i, "You haven't logged into the web app yet. Visit the tournament page to sign in first.")
		return
	}

	var status models.TournamentStatus
	var maxParts int
	err = b.pool.QueryRow(botCtx(), `
		SELECT status, max_participants FROM tournaments WHERE id = $1
	`, tournamentID).Scan(&status, &maxParts)
	if err != nil {
		respond(s, i, "Tournament not found.")
		return
	}
	if status != models.StatusRegistration {
		respond(s, i, "Tournament is not open for registration.")
		return
	}

	var count int
	_ = b.pool.QueryRow(botCtx(), `SELECT COUNT(*) FROM participants WHERE tournament_id = $1`, tournamentID).Scan(&count)
	if count >= maxParts {
		respond(s, i, "Tournament is full.")
		return
	}

	_, err = b.pool.Exec(botCtx(), `
		INSERT INTO participants (tournament_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING
	`, tournamentID, userID)
	if err != nil {
		respond(s, i, "Failed to join tournament.")
		return
	}

	respond(s, i, fmt.Sprintf("You've joined tournament #%d!", tournamentID))
}

func (b *Bot) cmdTournamentLeave(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.Interaction, tournamentID int64) {
	discordID := discordIDFromInteraction(i)
	if discordID == "" {
		respond(s, i, "Could not identify your Discord account.")
		return
	}

	var userID int64
	if err := b.pool.QueryRow(botCtx(), `SELECT id FROM users WHERE discord_id = $1`, discordID).Scan(&userID); err != nil {
		respond(s, i, "User not found.")
		return
	}

	var status models.TournamentStatus
	_ = b.pool.QueryRow(botCtx(), `SELECT status FROM tournaments WHERE id = $1`, tournamentID).Scan(&status)
	if status != models.StatusRegistration {
		respond(s, i, "Cannot leave after the tournament has started.")
		return
	}

	if _, err := b.pool.Exec(botCtx(), `DELETE FROM participants WHERE tournament_id = $1 AND user_id = $2`, tournamentID, userID); err != nil {
		respond(s, i, "Failed to leave tournament.")
		return
	}
	respond(s, i, fmt.Sprintf("You've left tournament #%d.", tournamentID))
}

func (b *Bot) cmdTournamentInfo(s *discordgo.Session, i *discordgo.InteractionCreate, tournamentID int64) {
	var name, format string
	var status models.TournamentStatus
	var maxParts int
	err := b.pool.QueryRow(botCtx(), `
		SELECT name, format, status, max_participants FROM tournaments WHERE id = $1
	`, tournamentID).Scan(&name, &format, &status, &maxParts)
	if err != nil {
		respond(s, i, "Tournament not found.")
		return
	}

	var count int
	_ = b.pool.QueryRow(botCtx(), `SELECT COUNT(*) FROM participants WHERE tournament_id = $1`, tournamentID).Scan(&count)

	respond(s, i, fmt.Sprintf(
		"**%s** (ID: %d)\nFormat: %s | Status: %s | Participants: %d/%d\n🌐 View bracket: %s/tournaments/%d",
		name, tournamentID, format, status, count, maxParts,
		b.webBaseURL(), tournamentID,
	))
}

func (b *Bot) handleMatchCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}
	sub := data.Options[0]
	if sub.Name != "result" {
		return
	}

	matchID := sub.Options[0].IntValue()
	winnerPartID := sub.Options[1].IntValue()

	discordID := discordIDFromInteraction(i)
	var userID int64
	if err := b.pool.QueryRow(botCtx(), `SELECT id FROM users WHERE discord_id = $1`, discordID).Scan(&userID); err != nil {
		respond(s, i, "You must log into the web app before submitting results.")
		return
	}

	canSubmit, err := tournament.CanSubmitResult(botCtx(), b.pool, matchID, userID)
	if err != nil || !canSubmit {
		respond(s, i, "You are not authorized to submit this result.")
		return
	}

	if err := tournament.SubmitResult(
		botCtx(), b.pool, b.broadcaster,
		matchID, winnerPartID, nil, nil, nil,
		userID, false,
	); err != nil {
		respond(s, i, "Error: "+err.Error())
		return
	}

	respond(s, i, fmt.Sprintf("Result recorded for match #%d. Winner: participant #%d", matchID, winnerPartID))
}

func (b *Bot) handleAdminCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Verify admin role
	if !b.hasAdminRole(i) {
		respond(s, i, "You don't have the admin role required for this command.")
		return
	}

	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}
	sub := data.Options[0]

	switch sub.Name {
	case "bracket-generate":
		tournamentID := sub.Options[0].IntValue()
		b.cmdAdminBracketGenerate(s, i, tournamentID)
	case "tournament-create":
		name := sub.Options[0].StringValue()
		format := sub.Options[1].StringValue()
		b.cmdAdminTournamentCreate(s, i, name, format)
	case "result-override":
		matchID := sub.Options[0].IntValue()
		winnerID := sub.Options[1].IntValue()
		b.cmdAdminResultOverride(s, i, matchID, winnerID)
	}
}

func (b *Bot) cmdAdminBracketGenerate(s *discordgo.Session, i *discordgo.InteractionCreate, tournamentID int64) {
	bracketID, err := tournament.GenerateBracket(botCtx(), b.pool, tournamentID, b.cfg.MaxParticipants)
	if err != nil {
		respond(s, i, "Failed to generate bracket: "+err.Error())
		return
	}
	_ = bracketID

	var name string
	_ = b.pool.QueryRow(botCtx(), `SELECT name FROM tournaments WHERE id = $1`, tournamentID).Scan(&name)

	b.broadcaster.BroadcastBracketUpdate(tournamentID)
	NotifyBracketGenerated(s, b.cfg.DiscordAnnouncementChannelID, tournamentID, name, b.webBaseURL())
	respond(s, i, fmt.Sprintf("Bracket generated for tournament #%d! View: %s/tournaments/%d",
		tournamentID, b.webBaseURL(), tournamentID))
}

func (b *Bot) cmdAdminTournamentCreate(s *discordgo.Session, i *discordgo.InteractionCreate, name, format string) {
	discordID := discordIDFromInteraction(i)
	var creatorID int64
	_ = b.pool.QueryRow(botCtx(), `SELECT id FROM users WHERE discord_id = $1`, discordID).Scan(&creatorID)
	if creatorID == 0 {
		respond(s, i, "You must log into the web app first.")
		return
	}

	var id int64
	err := b.pool.QueryRow(botCtx(), `
		INSERT INTO tournaments (name, format, max_participants, created_by)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, name, format, b.cfg.MaxParticipants, creatorID).Scan(&id)
	if err != nil {
		respond(s, i, "Failed to create tournament.")
		return
	}

	NotifyTournamentCreated(s, b.cfg.DiscordAnnouncementChannelID, id, name, b.webBaseURL())
	respond(s, i, fmt.Sprintf("Tournament **%s** created (ID: %d). Sign-up link: %s/tournaments/%d",
		name, id, b.webBaseURL(), id))
}

func (b *Bot) cmdAdminResultOverride(s *discordgo.Session, i *discordgo.InteractionCreate, matchID, winnerPartID int64) {
	if err := tournament.SubmitResult(
		botCtx(), b.pool, b.broadcaster,
		matchID, winnerPartID, nil, nil, nil,
		0, true,
	); err != nil {
		respond(s, i, "Error: "+err.Error())
		return
	}
	respond(s, i, fmt.Sprintf("Result overridden for match #%d. Winner: participant #%d", matchID, winnerPartID))
}

func (b *Bot) hasAdminRole(i *discordgo.InteractionCreate) bool {
	if i.Member == nil {
		return false
	}
	for _, role := range i.Member.Roles {
		if role == b.cfg.DiscordAdminRoleID {
			return true
		}
	}
	return false
}

func (b *Bot) webBaseURL() string {
	return "http://" + b.cfg.Host + ":" + b.cfg.Port
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func discordIDFromInteraction(i *discordgo.InteractionCreate) string {
	if i.Member != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func botCtx() context.Context {
	return context.Background()
}

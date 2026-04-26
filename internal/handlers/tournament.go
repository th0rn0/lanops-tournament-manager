package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"html/template"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/th0rn0/lanops-tournament-manager/internal/auth"
	"github.com/th0rn0/lanops-tournament-manager/internal/models"
	"github.com/th0rn0/lanops-tournament-manager/internal/tournament"
)

// GuildMemberChecker verifies that a Discord user belongs to the configured guild.
type GuildMemberChecker interface {
	IsGuildMember(ctx context.Context, discordUserID string) (bool, error)
}

type TournamentHandler struct {
	pool          *pgxpool.Pool
	brokers       *BracketBrokerMap
	tmpls         map[string]*template.Template
	maxParts      int
	guildChecker  GuildMemberChecker
}

func NewTournamentHandler(pool *pgxpool.Pool, brokers *BracketBrokerMap, tmpls map[string]*template.Template, maxParticipants int, guildChecker GuildMemberChecker) *TournamentHandler {
	return &TournamentHandler{pool: pool, brokers: brokers, tmpls: tmpls, maxParts: maxParticipants, guildChecker: guildChecker}
}

// GET /tournaments
func (h *TournamentHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT t.id, t.name, t.game, t.description, t.format, t.team_mode, t.team_size,
		       t.max_participants, t.status, t.created_by, t.created_at, t.updated_at,
		       (SELECT COUNT(*) FROM participants p WHERE p.tournament_id = t.id) AS participant_count
		FROM tournaments t
		WHERE t.status != 'cancelled'
		ORDER BY t.created_at DESC
		LIMIT 50
	`)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type TournamentRow struct {
		models.Tournament
		ParticipantCount int
	}
	var tournaments []TournamentRow
	for rows.Next() {
		var tr TournamentRow
		if err := rows.Scan(
			&tr.ID, &tr.Name, &tr.Game, &tr.Description, &tr.Format, &tr.TeamMode, &tr.TeamSize,
			&tr.MaxParticipants, &tr.Status, &tr.CreatedBy, &tr.CreatedAt, &tr.UpdatedAt,
			&tr.ParticipantCount,
		); err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		tournaments = append(tournaments, tr)
	}

	render(w, r, h.tmpls, "tournament_list.html", map[string]interface{}{
		"Tournaments": tournaments,
	})
}

// GET /tournaments/{id}
func (h *TournamentHandler) Detail(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var t models.Tournament
	err = h.pool.QueryRow(r.Context(), `
		SELECT id, name, game, description, format, team_mode, team_size, max_participants,
		       status, created_by, created_at, updated_at
		FROM tournaments WHERE id = $1
	`, id).Scan(
		&t.ID, &t.Name, &t.Game, &t.Description, &t.Format, &t.TeamMode, &t.TeamSize, &t.MaxParticipants,
		&t.Status, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		http.Error(w, "tournament not found", http.StatusNotFound)
		return
	}

	// Load bracket + matches if exists
	var bracketID *int64
	_ = h.pool.QueryRow(r.Context(), `SELECT id FROM brackets WHERE tournament_id = $1`, id).Scan(&bracketID)

	var matches []*models.Match
	if bracketID != nil {
		matches, _ = tournament.LoadMatchesForBracket(r.Context(), h.pool, *bracketID)
	}

	// Load participants
	rows, _ := h.pool.Query(r.Context(), `
		SELECT p.id, p.tournament_id, p.user_id, p.team_id, p.seed, p.registered_at,
		       COALESCE(u.username, '') AS user_username,
		       COALESCE(u.avatar, '') AS user_avatar,
		       COALESCE(u.discord_id, '') AS user_discord_id,
		       COALESCE(te.name, '') AS team_name
		FROM participants p
		LEFT JOIN users u ON u.id = p.user_id
		LEFT JOIN teams te ON te.id = p.team_id
		WHERE p.tournament_id = $1
		ORDER BY p.seed, p.registered_at
	`, id)
	var participants []*models.Participant
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			p := &models.Participant{}
			if err := rows.Scan(
				&p.ID, &p.TournamentID, &p.UserID, &p.TeamID, &p.Seed, &p.RegisteredAt,
				&p.UserUsername, &p.UserAvatar, &p.UserDiscordID, &p.TeamName,
			); err != nil {
				continue
			}
			participants = append(participants, p)
		}
	}

	userID, _ := auth.UserIDFromContext(r.Context())

	// Check if user is registered
	isRegistered := false
	userTeamID := int64(0)
	for _, p := range participants {
		if p.UserID != nil && *p.UserID == userID {
			isRegistered = true
			break
		}
	}

	// Load teams if team tournament (player_created mode)
	var teams []*models.Team
	if t.IsTeam() && t.TeamMode == models.TeamModePlayerCreated {
		teamRows, _ := h.pool.Query(r.Context(), `
			SELECT te.id, te.tournament_id, te.name, te.captain_id, te.open, te.join_password, te.invite_token,
			       COALESCE(u.username, '') AS captain_name,
			       COUNT(tm.user_id) AS member_count
			FROM teams te
			LEFT JOIN users u ON u.id = te.captain_id
			LEFT JOIN team_members tm ON tm.team_id = te.id
			WHERE te.tournament_id = $1
			GROUP BY te.id, u.username
			ORDER BY te.created_at
		`, id)
		if teamRows != nil {
			defer teamRows.Close()
			for teamRows.Next() {
				team := &models.Team{}
				if err := teamRows.Scan(
					&team.ID, &team.TournamentID, &team.Name, &team.CaptainID,
					&team.Open, &team.JoinPassword, &team.InviteToken,
					&team.CaptainName, &team.MemberCount,
				); err != nil {
					continue
				}
				teams = append(teams, team)
			}
		}
		// Fetch member names for all teams in one query
		if len(teams) > 0 {
			teamIDs := make([]int64, len(teams))
			for i, te := range teams {
				teamIDs[i] = te.ID
			}
			memberMap := make(map[int64][]string, len(teams))
			memberRows, _ := h.pool.Query(r.Context(), `
				SELECT tm.team_id, COALESCE(NULLIF(u.username,''), u.discord_id) AS display_name
				FROM team_members tm
				JOIN users u ON u.id = tm.user_id
				WHERE tm.team_id = ANY($1)
				ORDER BY tm.team_id, tm.joined_at
			`, teamIDs)
			if memberRows != nil {
				defer memberRows.Close()
				for memberRows.Next() {
					var teamID int64
					var name string
					if err := memberRows.Scan(&teamID, &name); err == nil {
						memberMap[teamID] = append(memberMap[teamID], name)
					}
				}
			}
			for _, te := range teams {
				te.Members = memberMap[te.ID]
			}
		}

		// Find the user's team
		if userID != 0 {
			_ = h.pool.QueryRow(r.Context(), `
				SELECT tm.team_id FROM team_members tm
				JOIN teams te ON te.id = tm.team_id
				WHERE te.tournament_id = $1 AND tm.user_id = $2
			`, id, userID).Scan(&userTeamID)
		}
	}

	bv := groupBracket(matches, t.Format)
	if bv.IsRoundRobin && bracketID != nil {
		bv.Standings, _ = loadStandings(r.Context(), h.pool, *bracketID)
	}

	// Count pending matches so the admin bar can show progress / enable Complete.
	pendingMatches := 0
	if bracketID != nil && t.Status == models.StatusActive {
		for _, m := range matches {
			if m.Status != models.MatchCompleted {
				pendingMatches++
			}
		}
	}

	render(w, r, h.tmpls, "tournament_detail.html", map[string]interface{}{
		"Tournament":     t,
		"BracketID":      bracketID,
		"Bracket":        bv,
		"Participants":   participants,
		"Teams":          teams,
		"UserTeamID":     userTeamID,
		"IsRegistered":   isRegistered,
		"PendingMatches": pendingMatches,
	})
}

// bracketView holds matches grouped for Challonge-style column rendering.
type bracketView struct {
	Winners        [][]*models.Match // index = round-1
	Losers         [][]*models.Match
	GrandFinal     *models.Match
	HasMatches     bool
	IsRoundRobin   bool
	Standings      []StandingRow
	CurrentMatches []SpotlightMatch
	NextMatches    []SpotlightMatch
}

// SpotlightMatch is a compact match summary for the "Now Playing / Up Next" banner.
type SpotlightMatch struct {
	PlayerA string
	PlayerB string
	Label   string
}

// StandingRow is one row of a round-robin standings table.
type StandingRow struct {
	ParticipantID int64
	Name          string
	Played        int
	Wins          int
	Losses        int
	Diff          int
}

// loadStandings computes the round-robin standings for a bracket. Each row
// counts a participant's wins/losses/played and their point differential
// (sum of (their score − opponent's score) across completed matches).
// Sorted by wins DESC, then losses ASC, then diff DESC, then name.
func loadStandings(ctx context.Context, pool *pgxpool.Pool, bracketID int64) ([]StandingRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT p.id,
		       COALESCE(u.username, te.name, '') AS name,
		       COALESCE(SUM(CASE WHEN m.status = 'completed' THEN 1 ELSE 0 END), 0) AS played,
		       COALESCE(SUM(CASE WHEN m.winner_id = p.id THEN 1 ELSE 0 END), 0) AS wins,
		       COALESCE(SUM(CASE WHEN m.loser_id  = p.id THEN 1 ELSE 0 END), 0) AS losses,
		       COALESCE(SUM(CASE
		           WHEN m.participant_a_id = p.id THEN COALESCE(m.score_a, 0) - COALESCE(m.score_b, 0)
		           WHEN m.participant_b_id = p.id THEN COALESCE(m.score_b, 0) - COALESCE(m.score_a, 0)
		           ELSE 0
		       END), 0) AS diff
		FROM participants p
		LEFT JOIN users u ON u.id = p.user_id
		LEFT JOIN teams te ON te.id = p.team_id
		LEFT JOIN matches m ON m.bracket_id = $1
		     AND (m.participant_a_id = p.id OR m.participant_b_id = p.id)
		WHERE p.tournament_id = (SELECT tournament_id FROM brackets WHERE id = $1)
		GROUP BY p.id, u.username, te.name
		ORDER BY wins DESC, losses ASC, diff DESC, name ASC
	`, bracketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StandingRow
	for rows.Next() {
		var s StandingRow
		if err := rows.Scan(&s.ParticipantID, &s.Name, &s.Played, &s.Wins, &s.Losses, &s.Diff); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func groupBracket(matches []*models.Match, format models.TournamentFormat) *bracketView {
	bv := &bracketView{IsRoundRobin: format == models.FormatRoundRobin}
	if len(matches) == 0 {
		return bv
	}
	bv.HasMatches = true

	// Build an ID → match index for downstream lookups.
	byID := make(map[int64]*models.Match, len(matches))
	for _, m := range matches {
		byID[m.ID] = m
	}

	// Flag each completed winners/losers-bracket match as Editable iff every
	// downstream match we can find in this bracket is still un-completed.
	// Grand-final and reset matches are intentionally not editable through
	// this UI flow — their state is tangled with tournament completion.
	downstreamOK := func(mid *int64) bool {
		if mid == nil {
			return true
		}
		d, ok := byID[*mid]
		if !ok {
			return true // downstream not loaded (e.g. pending_reset is filtered out); assume ok
		}
		return d.Status != models.MatchCompleted
	}
	for _, m := range matches {
		if m.Status != models.MatchCompleted {
			continue
		}
		if m.BracketSide != models.SideWinners && m.BracketSide != models.SideLosers {
			continue
		}
		if downstreamOK(m.NextMatchID) && downstreamOK(m.LoserNextMatchID) {
			m.Editable = true
		}
	}

	winnersByRound := map[int][]*models.Match{}
	losersByRound := map[int][]*models.Match{}
	maxW, maxL := 0, 0
	for _, m := range matches {
		switch m.BracketSide {
		case models.SideWinners:
			winnersByRound[m.Round] = append(winnersByRound[m.Round], m)
			if m.Round > maxW {
				maxW = m.Round
			}
		case models.SideLosers:
			losersByRound[m.Round] = append(losersByRound[m.Round], m)
			if m.Round > maxL {
				maxL = m.Round
			}
		case models.SideGrandFinal:
			gf := m
			bv.GrandFinal = gf
		}
	}
	for r := 1; r <= maxW; r++ {
		bv.Winners = append(bv.Winners, winnersByRound[r])
	}
	for r := 1; r <= maxL; r++ {
		bv.Losers = append(bv.Losers, losersByRound[r])
	}

	if !bv.IsRoundRobin {
		for _, m := range matches {
			switch m.Status {
			case models.MatchReady:
				bv.CurrentMatches = append(bv.CurrentMatches, spotlightFrom(m, maxW, maxL))
			case models.MatchPending:
				if m.ParticipantAID != nil || m.ParticipantBID != nil {
					bv.NextMatches = append(bv.NextMatches, spotlightFrom(m, maxW, maxL))
				}
			}
		}
	}

	return bv
}

func spotlightFrom(m *models.Match, wbTotal, lbTotal int) SpotlightMatch {
	playerA := m.ParticipantAName
	if playerA == "" {
		playerA = "TBD"
	}
	playerB := m.ParticipantBName
	if playerB == "" {
		playerB = "TBD"
	}
	return SpotlightMatch{PlayerA: playerA, PlayerB: playerB, Label: spotlightLabel(m, wbTotal, lbTotal)}
}

func spotlightLabel(m *models.Match, wbTotal, lbTotal int) string {
	switch m.BracketSide {
	case models.SideGrandFinal:
		return "Grand Final"
	case models.SideReset:
		return "Grand Final Reset"
	case models.SideLosers:
		remaining := lbTotal - m.Round
		switch remaining {
		case 0:
			return "Losers · Final"
		case 1:
			return "Losers · Semi-Final"
		}
		return fmt.Sprintf("Losers · Round %d", m.Round)
	default:
		remaining := wbTotal - m.Round
		switch remaining {
		case 0:
			return "Winners · Final"
		case 1:
			return "Winners · Semi-Final"
		case 2:
			return "Winners · Quarter-Final"
		}
		return fmt.Sprintf("Winners · Round %d", m.Round)
	}
}

// GET /tournaments/{id}/bracket — HTMX partial for SSE-triggered refresh
func (h *TournamentHandler) BracketFragment(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var bracketID *int64
	_ = h.pool.QueryRow(r.Context(), `SELECT id FROM brackets WHERE tournament_id = $1`, id).Scan(&bracketID)

	var format models.TournamentFormat
	_ = h.pool.QueryRow(r.Context(), `SELECT format FROM tournaments WHERE id = $1`, id).Scan(&format)

	var matches []*models.Match
	if bracketID != nil {
		matches, _ = tournament.LoadMatchesForBracket(r.Context(), h.pool, *bracketID)
	}

	bv := groupBracket(matches, format)
	if bv.IsRoundRobin && bracketID != nil {
		bv.Standings, _ = loadStandings(r.Context(), h.pool, *bracketID)
	}

	render(w, r, h.tmpls, "bracket_matches", map[string]interface{}{
		"Bracket": bv,
	})
}

// POST /tournaments/{id}/join
func (h *TournamentHandler) Join(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth/discord", http.StatusSeeOther)
		return
	}

	// Guild membership check: only members of the configured Discord server may join.
	if h.guildChecker != nil {
		var discordID string
		_ = h.pool.QueryRow(r.Context(), `SELECT discord_id FROM users WHERE id = $1`, userID).Scan(&discordID)
		if discordID != "" {
			member, err := h.guildChecker.IsGuildMember(r.Context(), discordID)
			if err != nil {
				http.Error(w, "could not verify guild membership", http.StatusServiceUnavailable)
				return
			}
			if !member {
				http.Error(w, "you must be a member of the Discord server to join tournaments", http.StatusForbidden)
				return
			}
		}
	}

	// Check tournament is in registration
	var status models.TournamentStatus
	var maxParts int
	if err := h.pool.QueryRow(r.Context(), `
		SELECT status, max_participants FROM tournaments WHERE id = $1
	`, id).Scan(&status, &maxParts); err != nil {
		http.Error(w, "tournament not found", http.StatusNotFound)
		return
	}
	if status != models.StatusRegistration {
		http.Error(w, "tournament is not open for registration", http.StatusBadRequest)
		return
	}

	// Count current participants
	var count int
	_ = h.pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM participants WHERE tournament_id = $1`, id).Scan(&count)
	if count >= maxParts {
		http.Error(w, "tournament is full", http.StatusBadRequest)
		return
	}

	_, err = h.pool.Exec(r.Context(), `
		INSERT INTO participants (tournament_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, id, userID)
	if err != nil {
		http.Error(w, "failed to join tournament", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/tournaments/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// POST /tournaments/{id}/leave
func (h *TournamentHandler) Leave(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth/discord", http.StatusSeeOther)
		return
	}

	var status models.TournamentStatus
	_ = h.pool.QueryRow(r.Context(), `SELECT status FROM tournaments WHERE id = $1`, id).Scan(&status)
	if status != models.StatusRegistration {
		http.Error(w, "cannot leave after tournament has started", http.StatusBadRequest)
		return
	}

	_, _ = h.pool.Exec(r.Context(), `
		DELETE FROM participants WHERE tournament_id = $1 AND user_id = $2
	`, id, userID)

	http.Redirect(w, r, "/tournaments/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// POST /matches/{id}/result
func (h *TournamentHandler) SubmitResult(w http.ResponseWriter, r *http.Request) {
	matchID, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid match id", http.StatusBadRequest)
		return
	}

	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	isAdmin := auth.IsAdminFromContext(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	winnerID, err := strconv.ParseInt(r.FormValue("winner_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid winner_id", http.StatusBadRequest)
		return
	}

	var scoreA, scoreB *int
	if sa := r.FormValue("score_a"); sa != "" {
		n, err := strconv.Atoi(sa)
		if err == nil {
			scoreA = &n
		}
	}
	if sb := r.FormValue("score_b"); sb != "" {
		n, err := strconv.Atoi(sb)
		if err == nil {
			scoreB = &n
		}
	}
	var scoreDisplay *string
	if sd := r.FormValue("score_display"); sd != "" {
		scoreDisplay = &sd
	}

	// Reject submissions on completed or cancelled tournaments.
	var tournamentStatus models.TournamentStatus
	if err := h.pool.QueryRow(r.Context(), `
		SELECT t.status FROM matches m
		JOIN brackets b ON b.id = m.bracket_id
		JOIN tournaments t ON t.id = b.tournament_id
		WHERE m.id = $1
	`, matchID).Scan(&tournamentStatus); err != nil {
		http.Error(w, "match not found", http.StatusNotFound)
		return
	}
	if tournamentStatus != models.StatusActive {
		http.Error(w, "tournament is not active", http.StatusForbidden)
		return
	}

	if !isAdmin {
		canSubmit, err := tournament.CanSubmitResult(r.Context(), h.pool, matchID, userID)
		if err != nil || !canSubmit {
			// Maybe this is an edit of a completed match by an authorized
			// participant — different rule, same endpoint.
			canEdit, errE := tournament.CanEditResult(r.Context(), h.pool, matchID, userID)
			if errE != nil || !canEdit {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
	}

	if err := tournament.SubmitResult(
		r.Context(), h.pool, h.brokers,
		matchID, winnerID, scoreA, scoreB, scoreDisplay,
		userID, isAdmin,
	); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Get tournament ID for redirect
	var tournamentID int64
	_ = h.pool.QueryRow(r.Context(), `
		SELECT b.tournament_id
		FROM matches m
		JOIN brackets b ON b.id = m.bracket_id
		WHERE m.id = $1
	`, matchID).Scan(&tournamentID)

	http.Redirect(w, r, "/tournaments/"+strconv.FormatInt(tournamentID, 10), http.StatusSeeOther)
}

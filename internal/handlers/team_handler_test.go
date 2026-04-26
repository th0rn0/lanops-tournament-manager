package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/lanops-tournament-manager/internal/auth"
	"github.com/th0rn0/lanops-tournament-manager/internal/handlers"
	"github.com/th0rn0/lanops-tournament-manager/internal/models"
	"github.com/th0rn0/lanops-tournament-manager/internal/testutil"
)

// setupTeamServer builds a chi router with team + tournament routes wired up.
func setupTeamServer(t *testing.T) *testServer {
	t.Helper()
	pool := testutil.NewDB(t)
	tmpls := loadTestTemplates(t)
	store := sessions.NewCookieStore([]byte("test-secret-32-bytes-padded-here-ok"))
	store.Options = &sessions.Options{Path: "/", HttpOnly: true}
	brokers := handlers.NewBracketBrokerMap()

	authMW := auth.NewMiddlewareWithChecker(store, neverAdmin{})
	tournamentH := handlers.NewTournamentHandler(pool, brokers, tmpls, 64, nil)
	teamH := handlers.NewTeamHandler(pool, brokers, tmpls)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authMW.OptionalAuth)
		r.Get("/tournaments/{id}", tournamentH.Detail)
	})
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Post("/tournaments/{id}/teams", teamH.Create)
		r.Get("/tournaments/{id}/teams/{team_id}/join", teamH.Join)
		r.Post("/tournaments/{id}/teams/{team_id}/join", teamH.Join)
		r.Post("/tournaments/{id}/teams/{team_id}/leave", teamH.Leave)
	})

	return &testServer{router: r, store: store, pool: pool}
}

// --- team create ---

func TestTeamCreate_RequiresAuth(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TC-auth", 5, creator)

	rw := ts.do(t, httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/teams", nil))
	assert.Equal(t, http.StatusSeeOther, rw.Code)
	assert.Equal(t, "/auth/discord", rw.Header().Get("Location"))
}

func TestTeamCreate_HappyPath(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TC-ok", 5, creator)

	form := url.Values{"team_name": {"Alpha Squad"}, "open": {"1"}}
	req := httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/teams",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, creator, "creator")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)

	// Captain is added as first member
	var teamID int64
	require.NoError(t, ts.pool.QueryRow(context.Background(),
		`SELECT id FROM teams WHERE tournament_id = $1 AND name = 'Alpha Squad'`, tid).Scan(&teamID))
	assert.Equal(t, 1, testutil.TeamMemberCount(t, ts.pool, teamID))
}

func TestTeamCreate_RejectsAdminAssigned(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	// Regular CreateTournament defaults to admin_assigned
	tid := testutil.CreateTournament(t, ts.pool, "TC-adm", models.FormatSingleElim, creator)
	// Update to team_size > 1 but keep admin_assigned
	_, err := ts.pool.Exec(context.Background(), `UPDATE tournaments SET team_size = 5 WHERE id = $1`, tid)
	require.NoError(t, err)

	form := url.Values{"team_name": {"Sneaky"}, "open": {"1"}}
	req := httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/teams",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, creator, "creator")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusForbidden, rw.Code)
}

func TestTeamCreate_RejectsActiveTournament(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TC-active", 5, creator)
	_, err := ts.pool.Exec(context.Background(), `UPDATE tournaments SET status = 'active' WHERE id = $1`, tid)
	require.NoError(t, err)

	form := url.Values{"team_name": {"Late"}, "open": {"1"}}
	req := httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/teams",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, creator, "creator")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusForbidden, rw.Code)
}

func TestTeamCreate_RejectsMissingName(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TC-noname", 5, creator)

	form := url.Values{"team_name": {""}}
	req := httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/teams",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, creator, "creator")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusBadRequest, rw.Code)
}

// --- team join ---

func TestTeamJoin_OpenTeam(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TJ-open", 5, creator)
	teamID := testutil.CreateTeam(t, ts.pool, tid, creator, "Alpha", true, "", "tok1")

	joiner := testutil.CreateUser(t, ts.pool, "joiner", "joiner")
	req := httptest.NewRequest("POST", teamJoinURL(tid, teamID), nil)
	req = ts.stampSession(t, req, joiner, "joiner")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)
	assert.Equal(t, 2, testutil.TeamMemberCount(t, ts.pool, teamID))
}

func TestTeamJoin_PrivateTeam_CorrectPassword(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TJ-pw-ok", 5, creator)
	teamID := testutil.CreateTeam(t, ts.pool, tid, creator, "Secret", false, "pass123", "tok2")

	joiner := testutil.CreateUser(t, ts.pool, "joiner", "joiner")
	form := url.Values{"join_password": {"pass123"}}
	req := httptest.NewRequest("POST", teamJoinURL(tid, teamID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, joiner, "joiner")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)
	assert.Equal(t, 2, testutil.TeamMemberCount(t, ts.pool, teamID))
}

func TestTeamJoin_PrivateTeam_WrongPassword(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TJ-pw-bad", 5, creator)
	teamID := testutil.CreateTeam(t, ts.pool, tid, creator, "Secret2", false, "realpass", "tok3")

	joiner := testutil.CreateUser(t, ts.pool, "joiner", "joiner")
	form := url.Values{"join_password": {"wrongpass"}}
	req := httptest.NewRequest("POST", teamJoinURL(tid, teamID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, joiner, "joiner")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusForbidden, rw.Code)
	assert.Equal(t, 1, testutil.TeamMemberCount(t, ts.pool, teamID))
}

func TestTeamJoin_ByInviteToken_GET(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TJ-tok", 5, creator)
	teamID := testutil.CreateTeam(t, ts.pool, tid, creator, "Invited", false, "secret", "mytoken123")

	joiner := testutil.CreateUser(t, ts.pool, "joiner", "joiner")
	req := httptest.NewRequest("GET", teamJoinURL(tid, teamID)+"?token=mytoken123", nil)
	req = ts.stampSession(t, req, joiner, "joiner")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)
	assert.Equal(t, 2, testutil.TeamMemberCount(t, ts.pool, teamID))
}

func TestTeamJoin_ByInviteToken_Invalid(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TJ-tok-bad", 5, creator)
	teamID := testutil.CreateTeam(t, ts.pool, tid, creator, "Locked", false, "secret", "goodtoken")

	joiner := testutil.CreateUser(t, ts.pool, "joiner", "joiner")
	req := httptest.NewRequest("GET", teamJoinURL(tid, teamID)+"?token=badtoken", nil)
	req = ts.stampSession(t, req, joiner, "joiner")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusForbidden, rw.Code)
	assert.Equal(t, 1, testutil.TeamMemberCount(t, ts.pool, teamID))
}

func TestTeamJoin_PreventsDuplicateMembership(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TJ-dup", 5, creator)
	team1 := testutil.CreateTeam(t, ts.pool, tid, creator, "TeamA", true, "", "tokA")
	joiner := testutil.CreateUser(t, ts.pool, "joiner", "joiner")

	// Join team1
	req := httptest.NewRequest("POST", teamJoinURL(tid, team1), nil)
	req = ts.stampSession(t, req, joiner, "joiner")
	rw := ts.do(t, req)
	require.Equal(t, http.StatusSeeOther, rw.Code)

	// Create a second team
	other := testutil.CreateUser(t, ts.pool, "other", "other")
	team2 := testutil.CreateTeam(t, ts.pool, tid, other, "TeamB", true, "", "tokB")

	// Attempt to join team2 while already in team1
	req2 := httptest.NewRequest("POST", teamJoinURL(tid, team2), nil)
	req2 = ts.stampSession(t, req2, joiner, "joiner")
	rw2 := ts.do(t, req2)
	assert.Equal(t, http.StatusBadRequest, rw2.Code)
	// Still only in team1
	assert.Equal(t, team1, testutil.UserTeamID(t, ts.pool, tid, joiner))
}

func TestTeamJoin_RequiresAuth(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TJ-auth", 5, creator)
	teamID := testutil.CreateTeam(t, ts.pool, tid, creator, "Open", true, "", "tok")

	rw := ts.do(t, httptest.NewRequest("POST", teamJoinURL(tid, teamID), nil))
	assert.Equal(t, http.StatusSeeOther, rw.Code)
	assert.Equal(t, "/auth/discord", rw.Header().Get("Location"))
}

// --- team leave ---

func TestTeamLeave_RemovesMember(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TL-ok", 5, creator)
	teamID := testutil.CreateTeam(t, ts.pool, tid, creator, "Alpha", true, "", "tok")

	// Add a second member
	member := testutil.CreateUser(t, ts.pool, "member", "member")
	_, err := ts.pool.Exec(context.Background(),
		`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, teamID, member)
	require.NoError(t, err)
	require.Equal(t, 2, testutil.TeamMemberCount(t, ts.pool, teamID))

	leaveURL := "/tournaments/" + strconv.FormatInt(tid, 10) + "/teams/" + strconv.FormatInt(teamID, 10) + "/leave"
	req := httptest.NewRequest("POST", leaveURL, nil)
	req = ts.stampSession(t, req, member, "member")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)
	assert.Equal(t, 1, testutil.TeamMemberCount(t, ts.pool, teamID))
}

// --- tournament detail with teams ---

func TestDetail_PlayerCreatedTeams_ShowsTeamsSection(t *testing.T) {
	ts := setupTeamServer(t)
	creator := testutil.CreateUser(t, ts.pool, "creator", "creator")
	tid := testutil.CreateTeamTournament(t, ts.pool, "TDetail-teams", 5, creator)
	testutil.CreateTeam(t, ts.pool, tid, creator, "Alpha Squad", true, "", "tok1")

	req := httptest.NewRequest("GET", "/tournaments/"+strconv.FormatInt(tid, 10), nil)
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusOK, rw.Code)
	body := rw.Body.String()
	assert.Contains(t, body, "Alpha Squad", "team name appears in detail page")
	assert.Contains(t, body, "Teams", "teams heading appears")
}

// helpers

func teamJoinURL(tournamentID, teamID int64) string {
	return "/tournaments/" + strconv.FormatInt(tournamentID, 10) +
		"/teams/" + strconv.FormatInt(teamID, 10) + "/join"
}

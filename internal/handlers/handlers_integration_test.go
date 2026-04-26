package handlers_test

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"html/template"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/sessions"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/lanops-tournament-manager/internal/auth"
	"github.com/th0rn0/lanops-tournament-manager/internal/handlers"
	"github.com/th0rn0/lanops-tournament-manager/internal/models"
	"github.com/th0rn0/lanops-tournament-manager/internal/testutil"
	"github.com/th0rn0/lanops-tournament-manager/internal/tournament"
	"github.com/th0rn0/lanops-tournament-manager/web"
)

// alwaysAdmin satisfies auth.AdminChecker — used to make the session's user
// look like an admin without hitting Discord.
type alwaysAdmin struct{}

func (alwaysAdmin) IsAdmin(context.Context, string) (bool, error) { return true, nil }

// neverAdmin is the opposite — ordinary participant.
type neverAdmin struct{}

func (neverAdmin) IsAdmin(context.Context, string) (bool, error) { return false, nil }

type testServer struct {
	router *chi.Mux
	store  sessions.Store
	pool   *pgxpool.Pool
}

// loadTestTemplates mirrors cmd/server/main.go's loadTemplates but with the
// minimum template funcs needed for the pages we exercise here.
func loadTestTemplates(t *testing.T) map[string]*template.Template {
	t.Helper()
	baseBytes, err := fs.ReadFile(web.TemplatesFS, "templates/base.html")
	require.NoError(t, err)

	funcs := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"dict": func(values ...any) map[string]any {
			m := make(map[string]any, len(values)/2)
			for i := 0; i+1 < len(values); i += 2 {
				key, _ := values[i].(string)
				m[key] = values[i+1]
			}
			return m
		},
		"roundLabel": func(round, total int, kind string) string {
			return "Round " + strconv.Itoa(round)
		},
		"formatName": func(f string) string { return f },
	}

	tmpls := map[string]*template.Template{}
	err = fs.WalkDir(web.TemplatesFS, "templates", func(path string, d fs.DirEntry, _ error) error {
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".html") || name == "base.html" {
			return nil
		}
		parts := strings.Split(path, "/")
		var key string
		if len(parts) >= 3 {
			key = parts[len(parts)-2] + "_" + parts[len(parts)-1]
		} else {
			key = name
		}
		page, err := fs.ReadFile(web.TemplatesFS, path)
		require.NoError(t, err)
		tp, err := template.New("").Funcs(funcs).Parse(string(baseBytes))
		require.NoError(t, err)
		_, err = tp.New("_page_").Parse(string(page))
		require.NoError(t, err)
		tmpls[key] = tp
		for _, sub := range tp.Templates() {
			n := sub.Name()
			if n == "" || n == "_page_" || n == key {
				continue
			}
			if _, taken := tmpls[n]; taken {
				continue
			}
			tmpls[n] = tp
		}
		return nil
	})
	require.NoError(t, err)
	return tmpls
}

// setupTestServer wires up the subset of main.go we need: a fresh DB schema,
// session store, templates, the three handler groups, and a chi router
// without csrf (tests pass raw form POSTs).
func setupTestServer(t *testing.T, admin bool) *testServer {
	t.Helper()
	pool := testutil.NewDB(t)
	tmpls := loadTestTemplates(t)
	store := sessions.NewCookieStore([]byte("test-secret-32-bytes-padded-here-ok"))
	store.Options = &sessions.Options{Path: "/", HttpOnly: true}

	brokers := handlers.NewBracketBrokerMap()

	var checker auth.AdminChecker = neverAdmin{}
	if admin {
		checker = alwaysAdmin{}
	}
	authMW := auth.NewMiddlewareWithChecker(store, checker)

	tournamentH := handlers.NewTournamentHandler(pool, brokers, tmpls, 64)
	adminH := handlers.NewAdminHandler(pool, tmpls, brokers, 64, false)
	leaderboardH := handlers.NewLeaderboardHandler(pool, tmpls)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authMW.OptionalAuth)
		r.Get("/tournaments", tournamentH.List)
		r.Get("/tournaments/{id}", tournamentH.Detail)
		r.Get("/tournaments/{id}/bracket", tournamentH.BracketFragment)
		r.Get("/leaderboard", leaderboardH.Show)
	})
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Post("/tournaments/{id}/join", tournamentH.Join)
		r.Post("/tournaments/{id}/leave", tournamentH.Leave)
		r.Post("/matches/{id}/result", tournamentH.SubmitResult)
	})
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireAdmin)
		r.Get("/admin", adminH.Dashboard)
		r.Get("/admin/tournaments/new", adminH.NewTournamentForm)
		r.Post("/admin/tournaments", adminH.CreateTournament)
		r.Get("/admin/tournaments/{id}", adminH.TournamentDetail)
		r.Post("/admin/tournaments/{id}/bracket-generate", adminH.GenerateBracket)
		r.Post("/admin/tournaments/{id}/cancel", adminH.CancelTournament)
		r.Post("/admin/matches/{id}/result", adminH.OverrideResult)
	})

	return &testServer{router: r, store: store, pool: pool}
}

// stampSession attaches a signed session cookie for the given user to req.
func (ts *testServer) stampSession(t *testing.T, req *http.Request, userID int64, discordID string) *http.Request {
	t.Helper()
	sess, _ := ts.store.Get(req, auth.SessionName)
	sess.Values[auth.SessionUserID] = userID
	sess.Values[auth.SessionDiscordID] = discordID
	w := httptest.NewRecorder()
	_ = sess.Save(req, w)
	out := req.Clone(req.Context())
	for _, c := range w.Result().Cookies() {
		out.AddCookie(c)
	}
	return out
}

func (ts *testServer) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	ts.router.ServeHTTP(rw, req)
	return rw
}

// --- tests ---

func TestList_UnauthenticatedRenders(t *testing.T) {
	ts := setupTestServer(t, false)
	rw := ts.do(t, httptest.NewRequest("GET", "/tournaments", nil))
	assert.Equal(t, http.StatusOK, rw.Code)
	assert.Contains(t, rw.Body.String(), "Tournament Manager")
}

func TestJoin_RequiresAuth(t *testing.T) {
	ts := setupTestServer(t, false)
	uid := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "Tj", models.FormatSingleElim, uid)

	// No session → redirect
	rw := ts.do(t, httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/join", nil))
	assert.Equal(t, http.StatusSeeOther, rw.Code)
	assert.Equal(t, "/auth/discord", rw.Header().Get("Location"))
}

func TestJoin_HappyPath(t *testing.T) {
	ts := setupTestServer(t, false)
	creator := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "Tj-ok", models.FormatSingleElim, creator)
	joiner := testutil.CreateUser(t, ts.pool, "joiner", "joiner")

	req := httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/join", nil)
	req = ts.stampSession(t, req, joiner, "joiner")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)

	// Verify the participant row landed.
	var cnt int
	err := ts.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM participants WHERE tournament_id = $1 AND user_id = $2`, tid, joiner).Scan(&cnt)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)
}

func TestJoin_RejectsAfterRegistrationClosed(t *testing.T) {
	ts := setupTestServer(t, false)
	creator := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "Tj-closed", models.FormatSingleElim, creator)
	// Move to active — registration is closed.
	_, err := ts.pool.Exec(context.Background(), `UPDATE tournaments SET status = 'active' WHERE id = $1`, tid)
	require.NoError(t, err)

	user := testutil.CreateUser(t, ts.pool, "late", "late")
	req := httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/join", nil)
	req = ts.stampSession(t, req, user, "late")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusBadRequest, rw.Code)
	assert.Contains(t, rw.Body.String(), "not open for registration")
}

func TestLeave_HappyPath(t *testing.T) {
	ts := setupTestServer(t, false)
	creator := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "Tleave", models.FormatSingleElim, creator)
	user := testutil.CreateUser(t, ts.pool, "leaver", "leaver")
	testutil.RegisterParticipants(t, ts.pool, tid, []int64{user})

	req := httptest.NewRequest("POST", "/tournaments/"+strconv.FormatInt(tid, 10)+"/leave", nil)
	req = ts.stampSession(t, req, user, "leaver")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)

	var cnt int
	require.NoError(t, ts.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM participants WHERE tournament_id = $1 AND user_id = $2`, tid, user).Scan(&cnt))
	assert.Equal(t, 0, cnt)
}

func TestCreateTournament_ValidationRejects(t *testing.T) {
	ts := setupTestServer(t, true) // admin checker
	admin := testutil.CreateUser(t, ts.pool, "admin_uid", "admin_uid")

	cases := []struct {
		name string
		form url.Values
		want int
	}{
		{"missing name", url.Values{"name": {""}, "format": {"single_elimination"}}, http.StatusBadRequest},
		{"bogus format", url.Values{"name": {"X"}, "format": {"bogus"}}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/admin/tournaments", strings.NewReader(c.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req = ts.stampSession(t, req, admin, "admin")
			rw := ts.do(t, req)
			assert.Equal(t, c.want, rw.Code)
		})
	}
}

func TestCreateTournament_RoundRobinAccepted(t *testing.T) {
	ts := setupTestServer(t, true)
	admin := testutil.CreateUser(t, ts.pool, "admin_uid", "admin_uid")

	form := url.Values{
		"name":             {"RR"},
		"format":           {"round_robin"},
		"team_size":        {"1"},
		"max_participants": {"8"},
	}
	req := httptest.NewRequest("POST", "/admin/tournaments", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, admin, "admin")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)

	var format string
	require.NoError(t, ts.pool.QueryRow(context.Background(),
		`SELECT format FROM tournaments WHERE name = 'RR'`).Scan(&format))
	assert.Equal(t, "round_robin", format)
}

func TestDetail_RendersWithBracket(t *testing.T) {
	ts := setupTestServer(t, false)
	ctx := context.Background()
	admin := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "Tdetail", models.FormatSingleElim, admin)
	users := testutil.CreateUsers(t, ts.pool, 4)
	testutil.RegisterParticipants(t, ts.pool, tid, users)
	_, err := tournament.GenerateBracket(ctx, ts.pool, tid, 64)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/tournaments/"+strconv.FormatInt(tid, 10), nil)
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusOK, rw.Code)
	assert.Contains(t, rw.Body.String(), "Tdetail")
	assert.Contains(t, rw.Body.String(), "Bracket")
}

func TestDetail_RoundRobinRendersStandings(t *testing.T) {
	ts := setupTestServer(t, false)
	ctx := context.Background()
	admin := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "rr-detail", models.FormatRoundRobin, admin)
	users := testutil.CreateUsers(t, ts.pool, 4)
	testutil.RegisterParticipants(t, ts.pool, tid, users)
	_, err := tournament.GenerateBracket(ctx, ts.pool, tid, 64)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/tournaments/"+strconv.FormatInt(tid, 10), nil)
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusOK, rw.Code)
	body := rw.Body.String()
	assert.Contains(t, body, "Standings", "RR detail page includes a standings section")
}

func TestBracketFragment_RendersWithoutFullPage(t *testing.T) {
	ts := setupTestServer(t, false)
	ctx := context.Background()
	admin := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "frag", models.FormatSingleElim, admin)
	users := testutil.CreateUsers(t, ts.pool, 4)
	testutil.RegisterParticipants(t, ts.pool, tid, users)
	_, err := tournament.GenerateBracket(ctx, ts.pool, tid, 64)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/tournaments/"+strconv.FormatInt(tid, 10)+"/bracket", nil)
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusOK, rw.Code)
	// Returns the bracket_matches partial — no <html> wrapper
	assert.NotContains(t, rw.Body.String(), "<html")
}

func TestSubmitResult_HappyPath(t *testing.T) {
	ts := setupTestServer(t, false)
	ctx := context.Background()
	admin := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "Tsub", models.FormatSingleElim, admin)
	users := testutil.CreateUsers(t, ts.pool, 2)
	testutil.RegisterParticipants(t, ts.pool, tid, users)
	bracketID, err := tournament.GenerateBracket(ctx, ts.pool, tid, 64)
	require.NoError(t, err)

	matches := testutil.LoadMatches(t, ts.pool, bracketID)
	require.Len(t, matches, 1)
	m := matches[0]

	form := url.Values{
		"winner_id": {strconv.FormatInt(*m.ParticipantAID, 10)},
		"score_a":   {"5"},
		"score_b":   {"2"},
	}
	req := httptest.NewRequest("POST", "/matches/"+strconv.FormatInt(m.ID, 10)+"/result", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, users[0], "user0")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)

	matches = testutil.LoadMatches(t, ts.pool, bracketID)
	assert.Equal(t, models.MatchCompleted, matches[0].Status)
	require.NotNil(t, matches[0].WinnerID)
	assert.Equal(t, *m.ParticipantAID, *matches[0].WinnerID)
}

func TestSubmitResult_RejectsOutsider(t *testing.T) {
	ts := setupTestServer(t, false)
	ctx := context.Background()
	admin := testutil.CreateUser(t, ts.pool, "admin", "admin")
	tid := testutil.CreateTournament(t, ts.pool, "Tsub-out", models.FormatSingleElim, admin)
	users := testutil.CreateUsers(t, ts.pool, 2)
	testutil.RegisterParticipants(t, ts.pool, tid, users)
	bracketID, err := tournament.GenerateBracket(ctx, ts.pool, tid, 64)
	require.NoError(t, err)

	matches := testutil.LoadMatches(t, ts.pool, bracketID)
	m := matches[0]

	outsider := testutil.CreateUser(t, ts.pool, "out", "outsider")
	form := url.Values{"winner_id": {strconv.FormatInt(*m.ParticipantAID, 10)}}
	req := httptest.NewRequest("POST", "/matches/"+strconv.FormatInt(m.ID, 10)+"/result", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = ts.stampSession(t, req, outsider, "outsider")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusForbidden, rw.Code)
}

func TestGenerateBracket_AdminPath(t *testing.T) {
	ts := setupTestServer(t, true)
	ctx := context.Background()
	admin := testutil.CreateUser(t, ts.pool, "admin_uid", "admin_uid")
	tid := testutil.CreateTournament(t, ts.pool, "Tgb", models.FormatSingleElim, admin)
	users := testutil.CreateUsers(t, ts.pool, 4)
	testutil.RegisterParticipants(t, ts.pool, tid, users)

	req := httptest.NewRequest("POST", "/admin/tournaments/"+strconv.FormatInt(tid, 10)+"/bracket-generate", nil)
	req = ts.stampSession(t, req, admin, "admin_uid")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)

	// Verify bracket + matches landed.
	var bracketID int64
	require.NoError(t, ts.pool.QueryRow(ctx, `SELECT id FROM brackets WHERE tournament_id = $1`, tid).Scan(&bracketID))
	matches := testutil.LoadMatches(t, ts.pool, bracketID)
	assert.Len(t, matches, 3, "4-player SE = 3 matches")
}

func TestCancelTournament_MarksCancelled(t *testing.T) {
	ts := setupTestServer(t, true)
	ctx := context.Background()
	admin := testutil.CreateUser(t, ts.pool, "admin_uid", "admin_uid")
	tid := testutil.CreateTournament(t, ts.pool, "Tcancel", models.FormatSingleElim, admin)

	req := httptest.NewRequest("POST", "/admin/tournaments/"+strconv.FormatInt(tid, 10)+"/cancel", nil)
	req = ts.stampSession(t, req, admin, "admin_uid")
	rw := ts.do(t, req)
	assert.Equal(t, http.StatusSeeOther, rw.Code)

	var status string
	require.NoError(t, ts.pool.QueryRow(ctx, `SELECT status FROM tournaments WHERE id = $1`, tid).Scan(&status))
	assert.Equal(t, "cancelled", status)
}

func TestLeaderboard_EmptyRenders(t *testing.T) {
	ts := setupTestServer(t, false)
	rw := ts.do(t, httptest.NewRequest("GET", "/leaderboard", nil))
	assert.Equal(t, http.StatusOK, rw.Code)
	assert.Contains(t, rw.Body.String(), "Leaderboard")
}

func TestLeaderboard_ShowsPlayerStats(t *testing.T) {
	ts := setupTestServer(t, false)
	ctx := context.Background()

	admin := testutil.CreateUser(t, ts.pool, "admin_uid", "admin_uid")
	tid := testutil.CreateTournament(t, ts.pool, "Tlb", models.FormatSingleElim, admin)
	users := testutil.CreateUsers(t, ts.pool, 2)
	testutil.RegisterParticipants(t, ts.pool, tid, users)
	bracketID, err := tournament.GenerateBracket(ctx, ts.pool, tid, 64)
	require.NoError(t, err)
	// Complete the only match so the leaderboard has data
	matches := testutil.LoadMatches(t, ts.pool, bracketID)
	require.Len(t, matches, 1)
	m := matches[0]
	_, err = ts.pool.Exec(ctx, `
		UPDATE matches
		SET status = 'completed', winner_id = $1, score_a = 3, score_b = 1
		WHERE id = $2
	`, m.ParticipantAID, m.ID)
	require.NoError(t, err)

	rw := ts.do(t, httptest.NewRequest("GET", "/leaderboard", nil))
	assert.Equal(t, http.StatusOK, rw.Code)
	body := rw.Body.String()
	assert.Contains(t, body, "user0", "winner appears on leaderboard")
}

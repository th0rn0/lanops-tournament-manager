package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"html/template"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/csrf"
	"github.com/gorilla/sessions"
	"github.com/th0rn0/lanops-tournament-manager/internal/auth"
	"github.com/th0rn0/lanops-tournament-manager/internal/bot"
	"github.com/th0rn0/lanops-tournament-manager/internal/config"
	"github.com/th0rn0/lanops-tournament-manager/internal/db"
	"github.com/th0rn0/lanops-tournament-manager/internal/handlers"
	"github.com/th0rn0/lanops-tournament-manager/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Database
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	log.Println("database migrations up to date")

	// Templates
	tmpls, err := loadTemplates()
	if err != nil {
		log.Fatalf("load templates: %v", err)
	}

	// Session store
	store := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 30, // 30 days
		HttpOnly: true,
		Secure:   cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	}

	// Discord OAuth + admin checks
	discordAuth := auth.NewDiscord(&auth.DiscordConfig{
		ClientID:     cfg.DiscordClientID,
		ClientSecret: cfg.DiscordClientSecret,
		RedirectURL:  cfg.DiscordRedirectURL,
		BotToken:     cfg.DiscordBotToken,
		AdminRoleID:  cfg.DiscordAdminRoleID,
		GuildID:      cfg.DiscordGuildID,
	})

	// Auth middleware. DEV_LOGIN swaps in a checker that recognises dev-prefixed
	// discord IDs so /dev/login users can access admin routes without hitting Discord.
	if !cfg.SecureCookies {
		log.Println("WARNING: SECURE_COOKIES=false — session and CSRF cookies lack the Secure flag. Set SECURE_COOKIES=true when running behind TLS in production.")
	}

	var checker auth.AdminChecker = discordAuth
	if cfg.DevLogin {
		log.Println("WARNING: DEV_LOGIN=true — /dev/login is enabled and Discord OAuth is bypassable. Do not run this in production.")
		checker = &auth.DevAdminChecker{Inner: discordAuth}
	}
	authMW := auth.NewMiddlewareWithChecker(store, checker)

	// SSE brokers
	brokers := handlers.NewBracketBrokerMap()

	// Handlers
	authHandler := handlers.NewAuthHandler(discordAuth, store, pool)
	tournamentHandler := handlers.NewTournamentHandler(pool, brokers, tmpls, cfg.MaxParticipants)
	adminHandler := handlers.NewAdminHandler(pool, tmpls, brokers, cfg.MaxParticipants, cfg.DevLogin)
	leaderboardHandler := handlers.NewLeaderboardHandler(pool, tmpls)
	teamHandler := handlers.NewTeamHandler(pool, brokers, tmpls)

	// CSRF key (must be 32 bytes)
	csrfKey := []byte(cfg.CSRFAuthKey)
	if len(csrfKey) < 32 {
		log.Fatal("CSRF_AUTH_KEY must be at least 32 bytes")
	}
	csrfMiddleware := csrf.Protect(csrfKey[:32],
		csrf.Secure(cfg.SecureCookies),
		csrf.SameSite(csrf.SameSiteLaxMode),
		// Pin cookie path to / so the same csrf cookie (and masked token) is
		// used app-wide. Without this, gorilla/csrf scopes the cookie to the
		// URL it was first issued from (e.g. /admin/tournaments), and tokens
		// from one path fail to validate on a different path.
		csrf.Path("/"),
	)
	// gorilla/csrf v1.7.3 defaults to scheme=https when comparing the request
	// URL against the Origin header. Apply PlaintextHTTPRequest (scheme=http)
	// only when the connection to the app is plain HTTP AND there is no upstream
	// TLS proxy (X-Forwarded-Proto: https). This covers dev (plain HTTP) without
	// breaking production deployments behind a TLS-terminating reverse proxy,
	// where the browser sends Origin: https://... even though the app receives
	// plain HTTP.
	prev := csrfMiddleware
	csrfMiddleware = func(h http.Handler) http.Handler {
		wrapped := prev(h)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := r
			if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
				req = csrf.PlaintextHTTPRequest(r)
			}
			wrapped.ServeHTTP(w, req)
		})
	}

	// Router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(securityHeaders(cfg.SecureCookies))
	r.Use(csrfMiddleware)

	// Static files
	staticSub, err := fs.Sub(web.StaticFS, "static")
	if err != nil {
		log.Fatalf("static fs: %v", err)
	}
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// PWA: service workers can only control pages within their own URL scope.
	// Serving the SW from /service-worker.js (not /static/...) gives it app-wide
	// scope. We read the same embedded file under the hood.
	r.Get("/service-worker.js", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache") // always revalidate, bumps should stick fast
		http.ServeFileFS(w, req, staticSub, "service-worker.js")
	})
	r.Get("/manifest.webmanifest", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		http.ServeFileFS(w, req, staticSub, "manifest.webmanifest")
	})

	// Auth routes (no auth required)
	r.Get("/auth/discord", authHandler.Login)
	r.Get("/auth/callback", authHandler.Callback)
	r.Post("/auth/logout", authHandler.Logout)

	if cfg.DevLogin {
		devLogin := handlers.NewDevLoginHandler(store, pool)
		r.Get("/dev/login", devLogin.Form)
		r.Post("/dev/login", devLogin.Submit)
	}

	// Public routes (optional auth)
	r.Group(func(r chi.Router) {
		r.Use(authMW.OptionalAuth)
		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/tournaments", http.StatusSeeOther)
		})
		r.Get("/tournaments", tournamentHandler.List)
		r.Get("/tournaments/{id}", tournamentHandler.Detail)
		r.Get("/tournaments/{id}/bracket", tournamentHandler.BracketFragment)
		r.Get("/tournaments/{id}/events", handlers.SSEHandler(brokers))
		r.Get("/leaderboard", leaderboardHandler.Show)
	})

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Post("/tournaments/{id}/join", tournamentHandler.Join)
		r.Post("/tournaments/{id}/leave", tournamentHandler.Leave)
		r.Post("/matches/{id}/result", tournamentHandler.SubmitResult)
		r.Post("/tournaments/{id}/teams", teamHandler.Create)
		r.Get("/tournaments/{id}/teams/{team_id}/join", teamHandler.Join)
		r.Post("/tournaments/{id}/teams/{team_id}/join", teamHandler.Join)
		r.Post("/tournaments/{id}/teams/{team_id}/leave", teamHandler.Leave)
	})

	// Admin routes
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireAdmin)
		r.Get("/admin", adminHandler.Dashboard)
		r.Get("/admin/tournaments/new", adminHandler.NewTournamentForm)
		r.Post("/admin/tournaments", adminHandler.CreateTournament)
		r.Get("/admin/tournaments/{id}", adminHandler.TournamentDetail)
		r.Post("/admin/tournaments/{id}/bracket-generate", adminHandler.GenerateBracket)
		r.Post("/admin/tournaments/{id}/complete", adminHandler.CompleteTournament)
		r.Post("/admin/tournaments/{id}/cancel", adminHandler.CancelTournament)
		r.Post("/admin/matches/{id}/result", adminHandler.OverrideResult)

		if cfg.DevLogin {
			devSeed := handlers.NewDevSeedHandler(pool)
			r.Post("/dev/tournaments/{id}/seed", devSeed.Seed)
			r.Post("/dev/seed-all", devSeed.SeedAll)
		}
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 90 * time.Second, // longer for SSE
		IdleTimeout:  120 * time.Second,
	}

	// Start Discord bot in background goroutine
	botCtx, botCancel := context.WithCancel(context.Background())
	defer botCancel()

	discordBot, err := bot.New(cfg, pool, brokers)
	if err != nil {
		log.Printf("warn: failed to create Discord bot: %v", err)
	} else {
		go func() {
			if err := discordBot.Start(botCtx); err != nil && err != context.Canceled {
				log.Printf("Discord bot error: %v", err)
			}
		}()
	}

	// Start HTTP server
	go func() {
		log.Printf("server listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	log.Println("shutting down...")
	botCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	log.Println("shutdown complete")
}

// loadTemplates builds a per-page template map. Each entry is a template set
// containing base.html plus one page file, preventing "content" block collisions.
func loadTemplates() (map[string]*template.Template, error) {
	baseContent, err := fs.ReadFile(web.TemplatesFS, "templates/base.html")
	if err != nil {
		return nil, fmt.Errorf("read base template: %w", err)
	}

	funcs := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"formatName": func(f string) string {
			switch f {
			case "single_elimination":
				return "Single Elimination"
			case "double_elimination":
				return "Double Elimination"
			case "round_robin":
				return "Round Robin"
			default:
				return f
			}
		},
		"dict": func(values ...any) map[string]any {
			m := make(map[string]any, len(values)/2)
			for i := 0; i+1 < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					continue
				}
				m[key] = values[i+1]
			}
			return m
		},
		"roundLabel": func(round, total int, kind string) string {
			switch kind {
			case "losers":
				return fmt.Sprintf("LB Round %d", round)
			case "round_robin":
				return fmt.Sprintf("Round %d", round)
			}
			// Elimination: label the last three rounds by name; everything
			// earlier gets "Round N".
			remaining := total - round
			switch remaining {
			case 0:
				return "Final"
			case 1:
				return "Semi-Final"
			case 2:
				return "Quarter-Final"
			}
			return fmt.Sprintf("Round %d", round)
		},
	}

	result := make(map[string]*template.Template)

	err = fs.WalkDir(web.TemplatesFS, "templates", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if len(name) < 5 || name[len(name)-5:] != ".html" || name == "base.html" {
			return nil
		}

		// Derive the template define name from the path:
		// "templates/tournament/list.html" → "tournament_list.html"
		// "templates/admin/dashboard.html" → "admin_dashboard.html"
		parts := strings.Split(path, "/")
		var key string
		if len(parts) >= 3 {
			key = parts[len(parts)-2] + "_" + parts[len(parts)-1]
		} else {
			key = name
		}

		pageContent, err := fs.ReadFile(web.TemplatesFS, path)
		if err != nil {
			return fmt.Errorf("read template %s: %w", path, err)
		}

		t, err := template.New("").Funcs(funcs).Parse(string(baseContent))
		if err != nil {
			return fmt.Errorf("parse base for %s: %w", key, err)
		}
		// Use "_page_" as a throw-away name; the {{define "key"}} blocks inside
		// the file register the real named templates in the set.
		if _, err := t.New("_page_").Parse(string(pageContent)); err != nil {
			return fmt.Errorf("parse template %s: %w", key, err)
		}
		result[key] = t
		// Also register each inner {{define "foo"}} block as a top-level key
		// pointing at the same set. Lets handlers render partials (e.g.
		// "bracket_matches" inside tournament_detail.html for HTMX swaps)
		// without needing to know which file defined them.
		for _, sub := range t.Templates() {
			n := sub.Name()
			if n == "" || n == "_page_" || n == key {
				continue
			}
			if _, taken := result[n]; taken {
				continue
			}
			result[n] = t
		}
		return nil
	})

	return result, err
}

// securityHeaders adds defensive HTTP headers to every response.
// HSTS is only set when secureCookies is true (i.e. the app is behind TLS).
// The CSP allows scripts from self and unpkg.com (htmx CDN), images from Discord
// and ui-avatars CDNs, and same-origin SSE connections. Inline scripts are not
// permitted — all scripts must be served from files.
func securityHeaders(secureCookies bool) func(http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' https://unpkg.com; " +
		"style-src 'self'; " +
		"img-src 'self' https://cdn.discordapp.com https://ui-avatars.com data:; " +
		"connect-src 'self'; " +
		"frame-ancestors 'none'"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", csp)
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
			if secureCookies {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

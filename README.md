# LanOps Tournament Manager

A self-hosted Discord-native tournament management app. Run brackets for your gaming community without handing data to a third party.

Single binary. Postgres. Discord OAuth. Real-time bracket updates via SSE.

## Features

- **Three tournament formats**: single elimination, double elimination, round robin
- **Team tournaments**: admin-assigned teams or player-created teams with open join, password, or invite link
- **Game field**: display which game each tournament is running
- **Leaderboard**: per-player stats across all tournaments (played, tournament wins, match W/L, win%)
- **Discord OAuth** login — players join with their Discord account
- **Player avatars**: Discord avatars on match cards and participant lists (UI Avatars fallback)
- **Real-time updates** — bracket refreshes live for all viewers via Server-Sent Events + HTMX
- **Discord bot** — slash commands for join/leave/results alongside the web UI
- **Admin panel** — generate brackets, override results, cancel tournaments
- **CSRF-protected** forms throughout
- **Security-hardened** — `html/template` XSS escaping, CSP / HSTS / X-Frame-Options headers, SRI hashes on CDN assets
- **PWA** — installable web app with offline shell

## Screenshots

### Tournament List

```
┌─────────────────────────────────────────────────────────┐
│ LanOps Tournament Manager  Tournaments   Login with Discord│
├─────────────────────────────────────────────────────────┤
│                                                         │
│  Summer Championship 2026                               │
│  single_elimination · registration · 6/8 participants  │
│  An epic 8-player single elimination tournament.       │
│                                                         │
│  Spring Cup                                             │
│  double_elimination · active · 8/8 participants        │
│  Double elimination bracket — lose once, stay in.      │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Tournament Detail (Registration Open)

```
┌─────────────────────────────────────────────────────────┐
│  Summer Championship 2026                               │
│  single_elimination · registration                      │
│                                                         │
│  Registration (6/8)                 [Login to Join]     │
│  ─────────────────────────────────────────────────────  │
│  • xXDragonSlayerXx                                     │
│  • ProGamer99                                           │
│  • NightOwl42                                           │
│  • StarPlayer                                           │
│  • SwiftKnight                                          │
│  • TurboAce                                             │
└─────────────────────────────────────────────────────────┘
```

### Bracket View (Live, Double Elimination)

```
┌─────────────────────────────────────────────────────────┐
│  Spring Cup                                             │
│                                                         │
│  Bracket                                                │
│  ─────────────────────────────────────────────────────  │
│  [WB R1 M1 ✓]  AdminUser vs xXDragonSlayerXx  READY   │
│  [WB R1 M2  ]  TBD       vs ProGamer99         pending │
│  [WB R1 M3  ]  TBD       vs NightOwl42         pending │
│  [WB R1 M4  ]  TBD       vs StarPlayer         pending │
│  [WB R2 M1  ]  TBD       vs TBD                pending │
│  [LB R1 M1  ]  TBD       vs TBD                pending │
│  [Grand Final] TBD       vs TBD                pending │
└─────────────────────────────────────────────────────────┘
```

### Admin Dashboard

```
┌─────────────────────────────────────────────────────────┐
│  Admin Dashboard                    [New Tournament]    │
│  ─────────────────────────────────────────────────────  │
│  ID  Name                    Format   Status    Parts   │
│  1   Summer Championship     single   registr.  6/8     │
│                               [Generate Bracket] [View] │
│  2   Spring Cup               double   active    8/8    │
│                               [View] [Cancel]           │
└─────────────────────────────────────────────────────────┘
```

## Architecture

```
cmd/server/          — HTTP server entrypoint (chi router, gorilla/sessions, gorilla/csrf)
internal/
  auth/              — Discord OAuth2 flow, admin role cache (5-min TTL), middleware
  bot/               — Discord slash command bot (runs as a goroutine alongside HTTP)
  config/            — Env var loading and validation
  db/                — pgxpool connection + golang-migrate embedded migrations
  handlers/          — HTTP handlers (tournament, admin, auth, team, leaderboard, SSE)
  models/            — Domain structs (Tournament, Match, Participant, User, Team)
  tournament/        — Bracket generation, result submission, advancement logic
  testutil/          — Per-test isolated Postgres schemas for integration tests
web/
  templates/         — Go HTML templates (base + page templates, per-page sets)
  static/            — CSS + bracket.js (CSRF injection, HTMX SSE wiring)
```

Key design points:

- **Fail-closed admin**: Discord API unreachable → 503, not open access
- **Two-pass bracket insert**: matches inserted first, then FK links updated to avoid BIGSERIAL chicken-and-egg
- **Per-page template sets**: each page gets its own `*template.Template` with base.html + one page file, preventing Go template `{{define "content"}}` collisions
- **SSE broker per tournament**: `BracketBrokerMap` holds one goroutine-safe broker per tournament ID; non-blocking send (select/default) prevents slow clients from blocking the result submission path

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) + [Docker Compose](https://docs.docker.com/compose/install/) (recommended path)
- Go 1.26+ and PostgreSQL 15+ (for manual builds without Docker)
- A Discord application with OAuth2 + bot token ([discord.com/developers](https://discord.com/developers/applications))

### Install Docker & Docker Compose

**macOS / Windows:** Install [Docker Desktop](https://www.docker.com/products/docker-desktop/) — it bundles Docker Compose.

**Linux (Ubuntu / Debian):**
```bash
# Install Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER   # allow running docker without sudo (re-login after)

# Compose is bundled with Docker Engine v2 — verify:
docker compose version
```

**Arch / CachyOS:**
```bash
sudo pacman -S docker docker-compose
sudo systemctl enable --now docker
sudo usermod -aG docker $USER
```

Verify everything is working:
```bash
docker run --rm hello-world
docker compose version
```

## Quick Start (Docker)

```bash
cp .env.example .env
# Edit .env — fill in your Discord credentials (see Discord Setup below)
docker compose up
```

App runs at `http://localhost:8080`.

The first run downloads images and builds the app binary (~1 minute). Subsequent starts are fast.

## Manual Build

```bash
# Install deps
go mod download

# Build
go build -o lanops-tournament-manager ./cmd/server

# Run (requires a Postgres instance and .env)
export $(cat .env | xargs)
./lanops-tournament-manager
```

Or with Make:

```bash
make build
make run       # requires .env
make dev       # go run, hot enough for dev
make test      # integration tests (requires TEST_DATABASE_URL)
```

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | yes | Postgres connection string |
| `DISCORD_CLIENT_ID` | yes | OAuth2 app client ID |
| `DISCORD_CLIENT_SECRET` | yes | OAuth2 app client secret |
| `DISCORD_REDIRECT_URL` | yes | OAuth2 callback URL (e.g. `http://localhost:8080/auth/callback`) |
| `DISCORD_BOT_TOKEN` | yes | Bot token for slash commands + admin role checks |
| `DISCORD_ADMIN_ROLE_ID` | yes | Role ID that grants admin access |
| `DISCORD_GUILD_ID` | yes | Guild (server) ID for bot commands |
| `SESSION_SECRET` | yes | 64-hex-char random string for cookie signing |
| `CSRF_AUTH_KEY` | yes | 64-hex-char random string (must be exactly 32 bytes) |
| `PORT` | no | HTTP port (default `8080`) |
| `HOST` | no | Bind host (default `localhost`) |
| `SECURE_COOKIES` | no | Set `true` in production (behind TLS) to add the `Secure` flag to session and CSRF cookies (default `false`) |
| `MAX_PARTICIPANTS` | no | Tournament size cap (default `64`, max `256`) |

Generate secrets:
```bash
openssl rand -hex 32  # SESSION_SECRET
openssl rand -hex 32  # CSRF_AUTH_KEY
```

## Discord Setup

1. Create an application at [discord.com/developers](https://discord.com/developers/applications)
2. Under **OAuth2**, add redirect URL: `http://your-host/auth/callback`
3. Under **Bot**, enable the bot and copy the token
4. Invite the bot to your server with `bot` + `applications.commands` scopes
5. Copy the role ID you want to use for admin access (Enable Developer Mode → right-click role)
6. Copy your server (guild) ID (right-click server → Copy Server ID)

## Slash Commands

| Command | Description |
|---|---|
| `/tournament list` | List open tournaments |
| `/tournament join <id>` | Join a tournament |
| `/tournament leave <id>` | Leave a tournament |
| `/tournament info <id>` | Show bracket status |
| `/match result <match_id> <winner_id>` | Submit a match result (participants only) |
| `/admin bracket-generate <tournament_id>` | Generate bracket (admin role required) |
| `/admin tournament-create` | Create a tournament via bot |
| `/admin result-override <match_id> <winner_id>` | Override any match result |

## Running Tests

Integration tests require a Postgres instance (they create isolated schemas per test and clean up automatically):

```bash
TEST_DATABASE_URL="postgres://user:pass@localhost:5432/testdb?sslmode=disable" go test ./...
```

Without `TEST_DATABASE_URL` set, integration tests are skipped cleanly.

## Deployment

The app is a single stateless binary. It runs DB migrations on startup (idempotent via golang-migrate). Suitable for any container environment.

See `Dockerfile` for a multi-stage build (Go 1.26-alpine builder, Alpine runtime). `docker-compose.yml` includes a Postgres 16 service with a health check so the app waits for the database to be ready.

For production:
- Use a real `DATABASE_URL` with TLS (`sslmode=require`)
- Generate strong random values for `SESSION_SECRET` and `CSRF_AUTH_KEY`
- Put a reverse proxy (nginx/Caddy) in front for TLS termination
- Set `DISCORD_REDIRECT_URL` to your public URL
- Set `SECURE_COOKIES=true` so session and CSRF cookies carry the `Secure` flag
- The app automatically sets `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options`, and `Strict-Transport-Security` (when `SECURE_COOKIES=true`) headers on every response

## CI / CD

CI runs on a self-hosted [Drone](https://www.drone.io/) server. Every push and pull request runs:

1. `golangci-lint` — static analysis (errcheck, govet, staticcheck, ineffassign, unused)
2. `go test -race ./...` — integration tests against a live Postgres 16 service
3. Docker build check (dry-run on PRs)

Merges to `main` additionally push a Docker image to `th0rn0/lanops-tournament-manager` on Docker Hub.

## Contributing

See [TODOS.md](TODOS.md) for the feature backlog and known open work. See [CHANGELOG.md](CHANGELOG.md) for release history.

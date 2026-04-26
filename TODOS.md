# LanOps Tournament Manager — Backlog

Product-level TODOs that don't belong in a single PR. Grouped by theme; each
item includes enough context that future-you (or a contributor) can pick it up
cold.

## Tournament formats

Currently supports: **single elimination**, **double elimination**. Benchmark
sites — [Challonge](https://kb.challonge.com/en/article/learn-about-challonge-competition-formats-1f8j1cf/),
[start.gg](https://help.start.gg/article/bracket-setup),
[Battlefy](https://help.battlefy.com/en/articles/9396943-bracket-settings-and-ordering) —
all offer several more. Adding them unlocks non-knockout formats and multi-stage
events.

- [x] **Round Robin** — every participant plays every other N times (1x / 2x / 3x).
  Standings by win/loss record (and tiebreakers: head-to-head, game diff, then
  seed). Good for group stages and small leagues. Schema-wise this is a new
  bracket_format + a standings view; no tree wiring needed.
- [ ] **Swiss** — fixed number of rounds (≈ log2(N) + 1). Each round pairs
  players with similar records, never repeating a matchup. First round pairs
  top half vs bottom half of the seeding. Needs a pairing engine that runs
  between rounds; otherwise stateless.
- [ ] **Group stage → knockout** (two-stage) — N groups play round-robin, top K
  from each group advance to a single/double elim bracket. The meta-event
  composition is the new piece; the sub-brackets reuse existing formats.
- [ ] **Free-for-all / multi-player matches** — matches with >2 participants,
  single result per match (1st, 2nd, ...). Schema currently hard-codes
  participant_a_id + participant_b_id; would need match_participants join table
  with placement ranks.
- [ ] **Best-of-N series** — a single match row can already carry score_a /
  score_b; a series would be modelled as multiple match rows with a parent
  match_id. Optional for now.

Ordering: Round Robin → Swiss → Two-stage → FFA. Round Robin is the cheapest
win (group-stage events are common, model is simple) and unblocks Two-stage.

## Discord bot

`internal/bot/` already has slash commands for tournament list/join/leave and
admin commands. Expand into a proper channel-aware bot:

- [ ] **Auto-announce bracket events** — post to the configured guild channel
  when a tournament enters `active`, when a match is ready, when a round
  completes, when the tournament completes. notifications.go already has the
  scaffolding (NotifyBracketGenerated etc. — three helpers) but they're not
  wired to the lifecycle.
- [ ] **Rich embed match cards** — the ready/in-progress embed includes player
  names + avatars + current score; updates in place via `s.ChannelMessageEdit`
  when the result lands. Ties into the SSE broadcast path we already have.
- [ ] **Result submission from Discord** — `/result <match_id> <winner> [scoreA]
  [scoreB]`. The web endpoint enforces participant/captain auth; reuse that.
  Helper already partly exists in `commands.go`.
- [ ] **Tournament creation wizard via DM** — for admins: `/tournament-create`
  opens a DM flow (format → max participants → description), posts the
  registration announcement back in the guild channel when done.
- [ ] **Admin role sync** — the web app already checks Discord role for admin.
  The bot should react to Discord role changes (`GUILD_MEMBER_UPDATE` event) by
  invalidating the admin cache entry immediately instead of waiting the
  5-minute TTL.

Ordering: auto-announce (lowest effort, highest user value) → rich embeds →
DM result submission → creation wizard → role sync.

## Project / Infrastructure

- [x] **Rename project to `lanops-tournament-manager`** — Go module path updated,
  all import paths renamed, binary renamed, Docker image renamed to
  `th0rn0/lanops-tournament-manager`. GitHub repo renamed via GitHub settings.
- [x] **Drone CI** — self-hosted Drone pipeline at `ci.th0rn0.co.uk` replaces
  GitHub Actions (GHA hit the private-repo free-tier minute cap). Pipeline:
  lint → test (with Postgres service) → Docker build check → push to Docker Hub
  on `main` merges.
- [x] **GitHub Actions removed** — `.github/workflows/ci.yml` deleted (v0.1.1).

## Shipped (v0.1.1 — security + CI)

- [x] **XSS protection** — all Go templates switched to `html/template`; dev login
  handler converted from raw string substitution to `html/template`.
- [x] **Security headers** — CSP, X-Frame-Options, X-Content-Type-Options,
  Referrer-Policy, Permissions-Policy, HSTS (when `SECURE_COOKIES=true`).
- [x] **CSP inline scripts** — service worker and admin form scripts moved to
  external files; CDN scripts carry SRI integrity hashes.
- [x] **Template buffering** — template errors return HTTP 500 instead of a
  truncated 200 response.
- [x] **CODEOWNERS** — `.github/CODEOWNERS` now covers the entire `.github/`
  directory (not just `workflows/`).
- [x] Drone CI pipeline live; GitHub Actions removed.

## Shipped (v0.1.0 — feat/round-robin)

- [x] Dev login gated by `DEV_LOGIN`, fake-player seeder, LB progression fix,
  score modal + edit-past-matches, PWA shell, LanOps branding.
- [x] **Round robin** tournament format — standings table, round-labelled matches,
  `no-connectors` bracket canvas layout.
- [x] **Game field** — tournaments carry a `game` column shown as a styled badge
  on cards and detail pages.
- [x] **Leaderboard** (`/leaderboard`) — cross-tournament per-player stats table
  (played, tournament wins, match W/L, win%).
- [x] **Player avatars** — Discord CDN avatars on match cards and participant
  lists; UI Avatars placeholder for dev/seeded users.
- [x] **Player-created team tournaments** — `team_mode: player_created` lets
  players create teams (open / password-protected) with invite links during
  registration. `team_mode: admin_assigned` preserves existing behaviour.
- [x] **Admin UX polish** — action buttons fill and align in admin table rows;
  Cancel button disabled (not hidden) for completed/cancelled tournaments;
  match winner select aligned in admin tournament detail.
- [x] Integration tests for all new handlers (team create/join/leave, leaderboard)
  and new testutil fixtures (`CreateTeamTournament`, `CreateTeam`, `TeamMemberCount`, etc.).

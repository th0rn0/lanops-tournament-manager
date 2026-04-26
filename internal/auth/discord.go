package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	discordAPIBase = "https://discord.com/api/v10"
	adminCacheTTL  = 5 * time.Minute
)

type DiscordUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Discriminator string `json:"discriminator"`
	Avatar        string `json:"avatar"`
}

type DiscordConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	BotToken     string
	AdminRoleID  string
	GuildID      string
}

type adminCacheEntry struct {
	isAdmin   bool
	expiresAt time.Time
}

type Discord struct {
	cfg       *DiscordConfig
	oauthCfg  *oauth2.Config
	cacheMu   sync.Mutex
	adminCache map[string]adminCacheEntry
}

func NewDiscord(cfg *DiscordConfig) *Discord {
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       []string{"identify"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://discord.com/api/oauth2/authorize",
			TokenURL: "https://discord.com/api/oauth2/token",
		},
	}

	return &Discord{
		cfg:        cfg,
		oauthCfg:   oauthCfg,
		adminCache: make(map[string]adminCacheEntry),
	}
}

func (d *Discord) AuthCodeURL(state string) string {
	return d.oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

func (d *Discord) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	return d.oauthCfg.Exchange(ctx, code)
}

func (d *Discord) GetUser(ctx context.Context, token *oauth2.Token) (*DiscordUser, error) {
	client := d.oauthCfg.Client(ctx, token)
	resp, err := client.Get(discordAPIBase + "/users/@me")
	if err != nil {
		return nil, fmt.Errorf("fetch discord user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("discord user API returned %d: %s", resp.StatusCode, string(body))
	}

	var user DiscordUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode discord user: %w", err)
	}

	return &user, nil
}

// IsAdmin checks whether a Discord user has the configured admin role in the guild.
// Uses the bot token (never expires). Fail-closed: returns false + error if Discord is unreachable.
// Results cached for 5 minutes per user.
// IsGuildMember returns true when the user is a member of the configured guild.
// It returns false (not an error) when the user is simply not in the guild.
func (d *Discord) IsGuildMember(ctx context.Context, discordUserID string) (bool, error) {
	url := fmt.Sprintf("%s/guilds/%s/members/%s", discordAPIBase, d.cfg.GuildID, discordUserID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+d.cfg.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("discord guild member API unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("discord guild member API returned %d: %s", resp.StatusCode, string(body))
	}
	return true, nil
}

func (d *Discord) IsAdmin(ctx context.Context, discordUserID string) (bool, error) {
	d.cacheMu.Lock()
	if entry, ok := d.adminCache[discordUserID]; ok && time.Now().Before(entry.expiresAt) {
		d.cacheMu.Unlock()
		return entry.isAdmin, nil
	}
	d.cacheMu.Unlock()

	url := fmt.Sprintf("%s/guilds/%s/members/%s", discordAPIBase, d.cfg.GuildID, discordUserID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+d.cfg.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("discord guild member API unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// User not in guild
		d.setCacheEntry(discordUserID, false)
		return false, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("discord guild member API returned %d: %s", resp.StatusCode, string(body))
	}

	var member struct {
		Roles []string `json:"roles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&member); err != nil {
		return false, fmt.Errorf("decode guild member: %w", err)
	}

	isAdmin := false
	for _, role := range member.Roles {
		if role == d.cfg.AdminRoleID {
			isAdmin = true
			break
		}
	}

	d.setCacheEntry(discordUserID, isAdmin)
	return isAdmin, nil
}

func (d *Discord) setCacheEntry(discordUserID string, isAdmin bool) {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	d.adminCache[discordUserID] = adminCacheEntry{
		isAdmin:   isAdmin,
		expiresAt: time.Now().Add(adminCacheTTL),
	}
}

// InvalidateAdminCache removes a specific user's cached admin status.
func (d *Discord) InvalidateAdminCache(discordUserID string) {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	delete(d.adminCache, discordUserID)
}

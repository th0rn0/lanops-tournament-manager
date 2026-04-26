package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	Port string
	Host string

	// Database
	DatabaseURL string

	// Discord OAuth2
	DiscordClientID              string
	DiscordClientSecret          string
	DiscordRedirectURL           string
	DiscordBotToken              string
	DiscordAdminRoleID           string
	DiscordGuildID               string
	DiscordAnnouncementChannelID string

	// Session
	SessionSecret string

	// CSRF
	CSRFAuthKey string

	// Security
	SecureCookies bool

	// Dev
	DevLogin bool

	// Tournament
	MaxParticipants int
}

func Load() (*Config, error) {
	// Load .env if present (ignored in production)
	_ = godotenv.Load()

	cfg := &Config{
		Port: getEnv("PORT", "8080"),
		Host: getEnv("HOST", "localhost"),

		DatabaseURL: getEnv("DATABASE_URL", ""),

		DiscordClientID:              getEnv("DISCORD_CLIENT_ID", ""),
		DiscordClientSecret:          getEnv("DISCORD_CLIENT_SECRET", ""),
		DiscordRedirectURL:           getEnv("DISCORD_REDIRECT_URL", ""),
		DiscordBotToken:              getEnv("DISCORD_BOT_TOKEN", ""),
		DiscordAdminRoleID:           getEnv("DISCORD_ADMIN_ROLE_ID", ""),
		DiscordGuildID:               getEnv("DISCORD_GUILD_ID", ""),
		DiscordAnnouncementChannelID: getEnv("DISCORD_ANNOUNCEMENT_CHANNEL_ID", ""),

		SessionSecret: getEnv("SESSION_SECRET", ""),
		CSRFAuthKey:   getEnv("CSRF_AUTH_KEY", ""),

		SecureCookies:   getEnvBool("SECURE_COOKIES", false),
		DevLogin:        getEnvBool("DEV_LOGIN", false),
		MaxParticipants: getEnvInt("MAX_PARTICIPANTS", 64),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.DiscordClientID == "" {
		return nil, fmt.Errorf("DISCORD_CLIENT_ID is required")
	}
	if cfg.DiscordClientSecret == "" {
		return nil, fmt.Errorf("DISCORD_CLIENT_SECRET is required")
	}
	if cfg.DiscordRedirectURL == "" {
		return nil, fmt.Errorf("DISCORD_REDIRECT_URL is required")
	}
	if cfg.DiscordBotToken == "" {
		return nil, fmt.Errorf("DISCORD_BOT_TOKEN is required")
	}
	if cfg.DiscordAdminRoleID == "" {
		return nil, fmt.Errorf("DISCORD_ADMIN_ROLE_ID is required")
	}
	if cfg.DiscordGuildID == "" {
		return nil, fmt.Errorf("DISCORD_GUILD_ID is required")
	}
	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("SESSION_SECRET is required")
	}
	if cfg.CSRFAuthKey == "" {
		return nil, fmt.Errorf("CSRF_AUTH_KEY is required")
	}

	if cfg.MaxParticipants > 256 {
		cfg.MaxParticipants = 256
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "true" || v == "1" || v == "yes"
	}
	return fallback
}

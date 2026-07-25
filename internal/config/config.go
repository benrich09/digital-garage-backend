// Package config centralizes all environment-driven settings. Everything
// is read once at startup via Viper and passed down explicitly — no
// package reaches back into the environment later, which keeps behavior
// predictable and easy to unit test.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds every tunable setting for the service. Field names map to
// SCREAMING_SNAKE_CASE env vars (e.g. Port -> PORT).
type Config struct {
	Env  string // "development" | "production"
	Port string

	// DatabaseURL is the direct Postgres connection string to Supabase,
	// e.g. postgresql://postgres:pass@db.xxxx.supabase.co:5432/postgres
	// Use the "Session" pooler mode string here if the VM keeps a small
	// number of long-lived connections (recommended on 1GB RAM).
	DatabaseURL string

	// DBMaxConns caps the pgxpool size. Kept small deliberately — each
	// Postgres connection costs ~5-10MB of backend memory and Supabase's
	// free/small tiers also cap total connections, so a 1 vCPU/1GB VM
	// should never try to hold more than a handful open at once.
	DBMaxConns int32

	// DBMinConns keeps a couple of warm connections so the first request
	// after idle time isn't stuck paying handshake latency.
	DBMinConns int32

	// SupabaseServiceRoleKey is used server-side only (e.g. for Storage
	// admin operations or settling payments) — never expose to clients.
	SupabaseServiceRoleKey string
	SupabaseURL            string

	// SupabaseJWTSecret verifies the access tokens Supabase Auth issues
	// to the mobile apps (Project Settings -> API -> JWT Secret). The
	// Go backend trusts a token's `sub` claim as the authenticated
	// profile id once the signature checks out — no round-trip to
	// Supabase needed per request.
	SupabaseJWTSecret  string
	CORSAllowedOrigins string

	LogLevel string // "debug" | "info" | "warn" | "error"

	ShutdownTimeout time.Duration

	// M-Pesa (Safaricom/Vodacom Daraja, Lipa Na M-Pesa Online / STK Push)
	// is one of two mobile money rails this app supports. CallbackURL
	// must be a publicly reachable HTTPS URL pointing at this API's
	// /webhooks/mpesa route — Daraja will not call back to localhost.
}

// Load reads configuration from environment variables (optionally backed
// by a local .env file for development) and returns a populated Config.
func Load() (*Config, error) {
	v := viper.New()

	// Sensible defaults for local development.
	v.SetDefault("ENV", "development")
	v.SetDefault("PORT", "8080")
	v.SetDefault("DB_MAX_CONNS", 4)
	v.SetDefault("DB_MIN_CONNS", 1)
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("SHUTDOWN_TIMEOUT_SECONDS", 15)
	v.SetDefault("CORS_ALLOWED_ORIGINS", "*")
	v.SetDefault("MPESA_BASE_URL", "https://sandbox.safaricom.co.ke")
	v.SetDefault("SELCOM_BASE_URL", "https://apigwtest.selcommobile.com")

	// Allow a .env file in the working directory for local dev; in
	// production (Docker/systemd) real env vars are used and this
	// silently no-ops if the file doesn't exist.
	v.SetConfigName(".env")
	v.SetConfigType("env")
	v.AddConfigPath(".")
	_ = v.ReadInConfig() // ignore error: absence is fine outside dev

	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	cfg := &Config{
		Env:                    v.GetString("ENV"),
		Port:                   v.GetString("PORT"),
		DatabaseURL:            v.GetString("DATABASE_URL"),
		DBMaxConns:             v.GetInt32("DB_MAX_CONNS"),
		DBMinConns:             v.GetInt32("DB_MIN_CONNS"),
		SupabaseServiceRoleKey: v.GetString("SUPABASE_SERVICE_ROLE_KEY"),
		SupabaseURL:            v.GetString("SUPABASE_URL"),
		SupabaseJWTSecret:      v.GetString("SUPABASE_JWT_SECRET"),
		CORSAllowedOrigins:     v.GetString("CORS_ALLOWED_ORIGINS"),
		LogLevel:               v.GetString("LOG_LEVEL"),
		ShutdownTimeout:        time.Duration(v.GetInt("SHUTDOWN_TIMEOUT_SECONDS")) * time.Second,
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}

	return cfg, nil
}

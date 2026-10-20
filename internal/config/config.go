package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL string
	Port        string

	// operator display settings, served at GET /config
	Units    string
	Timezone string
	Language string

	// service area, used to validate coordinates
	MinLat, MaxLat float64
	MinLng, MaxLng float64

	// map opening view
	CenterLat, CenterLng float64
	DefaultZoom          int

	// address search for delivery stops (GET /geocode). Nominatim by
	// default: its policy wants an identifying User-Agent, and an email
	// for contact if you send more than a little traffic.
	GeocoderURL       string
	GeocoderUserAgent string
	GeocoderEmail     string

	// Browser origins allowed to call the API and open the position socket.
	// CORS_ORIGINS, comma-separated, e.g. "https://dispatch.example.com".
	AllowedOrigins []string

	// AGENT_API_KEY: the dispatch agent's service key, sent as
	// "Authorization: Bearer <key>". Empty turns agent access off.
	AgentAPIKey string
	// COOKIE_SECURE: send the session cookie over HTTPS only. On by default;
	// browsers still accept it on http://localhost. Set false only for a
	// non-localhost dev setup without HTTPS.
	CookieSecure bool
	// How long a sign-in lasts.
	DispatcherSessionTTL time.Duration
	DriverSessionTTL     time.Duration
}

func Load(logger *slog.Logger) (*Config, error) {
	cfg := &Config{
		DatabaseURL: env("DATABASE_URL", "postgres://fleet:fleet_dev_pw@127.0.0.1:5434/fleet?sslmode=disable"),
		Port:        env("PORT", "8080"),
		Units:       env("OPERATOR_UNITS", "metric"),
		Timezone:    env("OPERATOR_TIMEZONE", "UTC"),
		Language:    env("OPERATOR_LANGUAGE", "en"),

		GeocoderURL:       env("GEOCODER_URL", "https://nominatim.openstreetmap.org"),
		GeocoderUserAgent: env("GEOCODER_USER_AGENT", "trucking-logistics-dispatch/0.1"),
		GeocoderEmail:     env("GEOCODER_EMAIL", ""),
	}

	cfg.AllowedOrigins = splitList(env("CORS_ORIGINS", "http://localhost:3000,http://localhost:3471"))
	for _, o := range cfg.AllowedOrigins {
		if u, err := url.Parse(o); err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("CORS_ORIGINS entry %q must be a full origin like https://dispatch.example.com", o)
		}
	}

	cfg.AgentAPIKey = os.Getenv("AGENT_API_KEY")
	if cfg.AgentAPIKey != "" && len(cfg.AgentAPIKey) < 32 {
		return nil, fmt.Errorf("AGENT_API_KEY must be at least 32 characters (try: openssl rand -hex 32)")
	}
	if cfg.AgentAPIKey == "" {
		logger.Warn("AGENT_API_KEY is not set; the dispatch agent can't call the API")
	}
	cfg.CookieSecure = env("COOKIE_SECURE", "true") != "false"
	var err error
	if cfg.DispatcherSessionTTL, err = envDuration("DISPATCHER_SESSION_TTL", 12*time.Hour); err != nil {
		return nil, err
	}
	if cfg.DriverSessionTTL, err = envDuration("DRIVER_SESSION_TTL", 14*24*time.Hour); err != nil {
		return nil, err
	}

	if cfg.Units != "metric" && cfg.Units != "imperial" {
		return nil, fmt.Errorf("OPERATOR_UNITS must be metric or imperial, got %q", cfg.Units)
	}

	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return nil, fmt.Errorf("OPERATOR_TIMEZONE %q is not a valid IANA name: %w", cfg.Timezone, err)
	}

	if cfg.MinLat, err = envFloat("SERVICE_MIN_LAT", -90); err != nil {
		return nil, err
	}
	if cfg.MaxLat, err = envFloat("SERVICE_MAX_LAT", 90); err != nil {
		return nil, err
	}
	if cfg.MinLng, err = envFloat("SERVICE_MIN_LNG", -180); err != nil {
		return nil, err
	}
	if cfg.MaxLng, err = envFloat("SERVICE_MAX_LNG", 180); err != nil {
		return nil, err
	}
	if cfg.CenterLat, err = envFloat("MAP_CENTER_LAT", 0); err != nil {
		return nil, err
	}
	if cfg.CenterLng, err = envFloat("MAP_CENTER_LNG", 0); err != nil {
		return nil, err
	}
	if cfg.DefaultZoom, err = envInt("MAP_DEFAULT_ZOOM", 11); err != nil {
		return nil, err
	}

	if cfg.MinLat >= cfg.MaxLat {
		return nil, fmt.Errorf("SERVICE_MIN_LAT (%v) must be less than SERVICE_MAX_LAT (%v)", cfg.MinLat, cfg.MaxLat)
	}
	if cfg.MinLng >= cfg.MaxLng {
		return nil, fmt.Errorf("SERVICE_MIN_LNG (%v) must be less than SERVICE_MAX_LNG (%v)", cfg.MinLng, cfg.MaxLng)
	}

	if os.Getenv("SERVICE_MIN_LAT") == "" {
		logger.Warn("no service area configured; coordinate validation is effectively global",
			"hint", "set SERVICE_MIN_LAT, SERVICE_MAX_LAT, SERVICE_MIN_LNG, SERVICE_MAX_LNG")
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimRight(strings.TrimSpace(p), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// OriginHosts is AllowedOrigins without the scheme, the form the websocket
// origin check wants ("localhost:3000").
func (c *Config) OriginHosts() []string {
	out := make([]string, 0, len(c.AllowedOrigins))
	for _, o := range c.AllowedOrigins {
		if u, err := url.Parse(o); err == nil {
			out = append(out, u.Host)
		}
	}
	return out
}

func envFloat(key string, fallback float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", key, v)
	}
	return f, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a duration like 12h, got %q", key, v)
	}
	return d, nil
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", key, v)
	}
	return n, nil
}

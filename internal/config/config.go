package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
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

	if cfg.Units != "metric" && cfg.Units != "imperial" {
		return nil, fmt.Errorf("OPERATOR_UNITS must be metric or imperial, got %q", cfg.Units)
	}

	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return nil, fmt.Errorf("OPERATOR_TIMEZONE %q is not a valid IANA name: %w", cfg.Timezone, err)
	}

	var err error
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

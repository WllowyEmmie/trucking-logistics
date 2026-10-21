package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Settings are what dispatchers change on the Settings page. They're
// stored in the database (one row) and take effect at once; the
// environment only supplies the defaults until someone saves.
type Settings struct {
	// ISO country the settings were last filled from ("NG"); informational,
	// and the default country for phone numbers.
	Country string `json:"country"`

	Units    string `json:"units"`
	Timezone string `json:"timezone"`
	Language string `json:"language"`

	CenterLat   float64 `json:"center_lat"`
	CenterLng   float64 `json:"center_lng"`
	DefaultZoom int     `json:"default_zoom"`

	// Service area: stops and GPS positions outside it are refused.
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLng float64 `json:"min_lng"`
	MaxLng float64 `json:"max_lng"`

	OperatorName  string `json:"operator_name"`
	SupportPhone  string `json:"support_phone"`
	DispatchPhone string `json:"dispatch_phone"`
	// The usual pickup, pre-filled on new deliveries. Optional.
	Depot        *Depot `json:"depot"`
	AgentEnabled bool   `json:"agent_enabled"`

	// Alert thresholds.
	StaleMinutes        int `json:"stale_minutes"`
	PendingMinutes      int `json:"pending_minutes"`
	FailedWindowMinutes int `json:"failed_window_minutes"`
	PingSeconds         int `json:"ping_seconds"`

	// Arrival estimates: average road speed in the operator's traffic, and
	// time spent at each stop (loading, signing, finding the gate).
	EtaSpeedKph int `json:"eta_speed_kph"`
	StopMinutes int `json:"stop_minutes"`

	// How long completed and cancelled deliveries stay on the console
	// before they're only in History.
	BoardHours int `json:"board_hours"`

	// Proof of delivery at each drop-off: the receiver's name is always
	// required from a driver; these add a photo and a signature.
	PodPhotoRequired     bool `json:"pod_photo_required"`
	PodSignatureRequired bool `json:"pod_signature_required"`

	// Shown to customers on the tracking page (Help and Delivery terms tabs)
	// and given to the agent that answers them. {company} and
	// {support_phone} are filled in where they're shown.
	CustomerTerms string    `json:"customer_terms"`
	CustomerFAQ   []FAQItem `json:"customer_faq"`

	// Auto mode: the agent supervises instead of proposing. It gives
	// drivers on shift with no truck a free truck, and assigns new
	// deliveries to the nearest ready truck: within AutoRadiusKm of the
	// pickup if there is one, otherwise the nearest anywhere.
	AutoAssign   bool    `json:"auto_assign"`
	AutoRadiusKm float64 `json:"auto_radius_km"`

	// The public website: the booking page and how customers reach dispatch.
	// Prices are in naira: base fee + per km of the estimated road
	// distance, never below the minimum. Paying needs PAYSTACK_SECRET_KEY.
	BookingEnabled bool   `json:"booking_enabled"`
	DispatchEmail  string `json:"dispatch_email"`
	PriceBase      int    `json:"price_base"`
	PricePerKm     int    `json:"price_per_km"`
	PriceMinimum   int    `json:"price_minimum"`
}

type FAQItem struct {
	Q string `json:"q"`
	A string `json:"a"`
}

type Depot struct {
	Address string  `json:"address"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

// DefaultSettings: the environment's values, and sensible defaults for
// what the environment doesn't set.
func (c *Config) DefaultSettings() Settings {
	return Settings{
		Units:               c.Units,
		Timezone:            c.Timezone,
		Language:            c.Language,
		CenterLat:           c.CenterLat,
		CenterLng:           c.CenterLng,
		DefaultZoom:         c.DefaultZoom,
		MinLat:              c.MinLat,
		MaxLat:              c.MaxLat,
		MinLng:              c.MinLng,
		MaxLng:              c.MaxLng,
		OperatorName:        "Dispatch",
		AgentEnabled:        true,
		StaleMinutes:        20,
		PendingMinutes:      120,
		FailedWindowMinutes: 60,
		PingSeconds:         15,
		EtaSpeedKph:         25,
		StopMinutes:         10,
		BoardHours:          12,
		// The receiver signs on the driver's phone; a photo is optional.
		PodSignatureRequired: true,
		CustomerTerms:        DefaultTerms,
		CustomerFAQ:          DefaultFAQ(),
		AutoRadiusKm:         2,
		PriceBase:            5000,
		PricePerKm:           400,
		PriceMinimum:         10000,
	}
}

var (
	languageRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	countryRE  = regexp.MustCompile(`^[A-Z]{2}$`)
	phoneRE    = regexp.MustCompile(`^\+[0-9]{7,15}$`)
	emailRE    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// Validate reports the first problem, worded for the person who typed it.
func (s *Settings) Validate() error {
	s.OperatorName = strings.TrimSpace(s.OperatorName)
	s.SupportPhone = strings.TrimSpace(s.SupportPhone)
	s.DispatchPhone = strings.TrimSpace(s.DispatchPhone)
	s.DispatchEmail = strings.TrimSpace(s.DispatchEmail)
	switch {
	case s.Country != "" && !countryRE.MatchString(s.Country):
		return errors.New("country must be a two-letter code like NG")
	case s.Units != "metric" && s.Units != "imperial":
		return errors.New("units must be metric or imperial")
	case !languageRE.MatchString(s.Language):
		return errors.New("language must look like en-NG")
	case s.DefaultZoom < 1 || s.DefaultZoom > 19:
		return errors.New("zoom must be between 1 and 19")
	case s.MinLat < -90 || s.MaxLat > 90 || s.MinLng < -180 || s.MaxLng > 180:
		return errors.New("service area must be within latitude -90 to 90 and longitude -180 to 180")
	case s.MinLat >= s.MaxLat:
		return errors.New("service area: the southern latitude must be less than the northern one")
	case s.MinLng >= s.MaxLng:
		return errors.New("service area: the western longitude must be less than the eastern one")
	case !s.Contains(s.CenterLat, s.CenterLng):
		return errors.New("the map centre must be inside the service area")
	case s.OperatorName == "" || len(s.OperatorName) > 80:
		return errors.New("the company name must be 1 to 80 characters")
	case s.SupportPhone != "" && !phoneRE.MatchString(s.SupportPhone):
		return errors.New("the customer support phone must be an international number like +2348031234567")
	case s.DispatchPhone != "" && !phoneRE.MatchString(s.DispatchPhone):
		return errors.New("the dispatch phone must be an international number like +2348031234567")
	case s.StaleMinutes < 1 || s.StaleMinutes > 1440:
		return errors.New("silent after must be 1 to 1440 minutes")
	case s.PendingMinutes < 1 || s.PendingMinutes > 10080:
		return errors.New("pending alert must be 1 to 10080 minutes")
	case s.FailedWindowMinutes < 5 || s.FailedWindowMinutes > 10080:
		return errors.New("failed deliveries stay listed for 5 to 10080 minutes")
	case s.PingSeconds < 5 || s.PingSeconds > 300:
		return errors.New("the driver position interval must be 5 to 300 seconds")
	case s.EtaSpeedKph < 5 || s.EtaSpeedKph > 120:
		return errors.New("the average speed for estimates must be 5 to 120 km/h")
	case s.StopMinutes < 0 || s.StopMinutes > 240:
		return errors.New("the time at each stop must be 0 to 240 minutes")
	case s.BoardHours < 1 || s.BoardHours > 168:
		return errors.New("finished deliveries stay on the console for 1 to 168 hours")
	case len(s.CustomerTerms) > 10000:
		return errors.New("the delivery terms are too long (10,000 characters at most)")
	case len(s.CustomerFAQ) > 20:
		return errors.New("20 questions at most")
	case s.AutoRadiusKm < 0.1 || s.AutoRadiusKm > 100:
		return errors.New("the auto mode distance must be 0.1 to 100 km")
	case s.DispatchEmail != "" && !emailRE.MatchString(s.DispatchEmail):
		return errors.New("the dispatch email doesn't look like an email address")
	case s.PriceBase < 0 || s.PricePerKm < 0 || s.PriceMinimum < 0 || s.PriceBase > 10_000_000 || s.PricePerKm > 1_000_000 || s.PriceMinimum > 10_000_000:
		return errors.New("prices must be between 0 and 10,000,000")
	case s.BookingEnabled && s.PriceBase+s.PricePerKm+s.PriceMinimum == 0:
		return errors.New("online booking needs a price: set a base fee, a rate per km or a minimum")
	}
	for i := range s.CustomerFAQ {
		f := &s.CustomerFAQ[i]
		f.Q, f.A = strings.TrimSpace(f.Q), strings.TrimSpace(f.A)
		if f.Q == "" || f.A == "" || len(f.Q) > 200 || len(f.A) > 2000 {
			return fmt.Errorf("question %d needs a question (up to 200 characters) and an answer (up to 2,000)", i+1)
		}
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" {
		return fmt.Errorf("%q isn't a time zone name like Africa/Lagos", s.Timezone)
	}
	if s.Depot != nil {
		s.Depot.Address = strings.TrimSpace(s.Depot.Address)
		if s.Depot.Address == "" {
			return errors.New("the depot needs an address, or leave the depot out")
		}
		if !s.Contains(s.Depot.Lat, s.Depot.Lng) {
			return errors.New("the depot must be inside the service area")
		}
	}
	return nil
}

func (s *Settings) Contains(lat, lng float64) bool {
	return lat >= s.MinLat && lat <= s.MaxLat && lng >= s.MinLng && lng <= s.MaxLng
}

// Global: no service area set (the whole world).
func (s *Settings) Global() bool {
	return s.MinLat <= -90 && s.MaxLat >= 90 && s.MinLng <= -180 && s.MaxLng >= 180
}

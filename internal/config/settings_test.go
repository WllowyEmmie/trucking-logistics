package config

import "testing"

func nigeria() Settings {
	return Settings{
		Country: "NG", Units: "metric", Timezone: "Africa/Lagos", Language: "en-NG",
		CenterLat: 6.5244, CenterLng: 3.3792, DefaultZoom: 11,
		MinLat: 4.2, MaxLat: 13.9, MinLng: 2.6, MaxLng: 14.7,
		OperatorName: "Kestrel", SupportPhone: "+2348031234567",
		StaleMinutes: 20, PendingMinutes: 120, FailedWindowMinutes: 60, PingSeconds: 15,
		EtaSpeedKph: 25, StopMinutes: 10, BoardHours: 12,
		Depot: &Depot{Address: "Apapa Wharf", Lat: 6.44, Lng: 3.36},
	}
}

func TestSettingsValidate(t *testing.T) {
	ok := nigeria()
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid settings refused: %v", err)
	}
	for name, mut := range map[string]func(*Settings){
		"units":        func(s *Settings) { s.Units = "furlongs" },
		"eta speed":    func(s *Settings) { s.EtaSpeedKph = 0 },
		"stop minutes": func(s *Settings) { s.StopMinutes = -1 },
		"timezone":     func(s *Settings) { s.Timezone = "Lagos" },
		"language":     func(s *Settings) { s.Language = "English" },
		"lat order":    func(s *Settings) { s.MinLat, s.MaxLat = 13.9, 4.2 },
		"centre":       func(s *Settings) { s.CenterLat = 51.5 },
		"depot":        func(s *Settings) { s.Depot.Lat = 51.5 },
		"depot addr":   func(s *Settings) { s.Depot.Address = " " },
		"phone":        func(s *Settings) { s.SupportPhone = "0803 123 4567" },
		"name":         func(s *Settings) { s.OperatorName = "  " },
		"stale":        func(s *Settings) { s.StaleMinutes = 0 },
		"ping":         func(s *Settings) { s.PingSeconds = 1 },
		"zoom":         func(s *Settings) { s.DefaultZoom = 30 },
		"country code": func(s *Settings) { s.Country = "Nigeria" },
	} {
		s := nigeria()
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s: bad value accepted", name)
		}
	}
}

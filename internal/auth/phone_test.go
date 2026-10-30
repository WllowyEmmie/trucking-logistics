package auth

import "testing"

func TestCanonicalPhone(t *testing.T) {
	for _, tc := range []struct{ in, country, want string }{
		{"0803 123 4567", "NG", "+2348031234567"},
		{"08031234567", "NG", "+2348031234567"},
		{"+234 803 123 4567", "NG", "+2348031234567"},
		{"2348031234567", "NG", "+2348031234567"},
		{"8031234567", "NG", "+2348031234567"},
		{"+44 7700 900123", "NG", "+447700900123"}, // already international: kept
		{"0803", "NG", "0803"},                     // too short to be a number here: left alone
		{"08031234567", "", "08031234567"},         // no country: left alone
		{"", "NG", ""},
	} {
		if got := CanonicalPhone(tc.in, tc.country); got != tc.want {
			t.Errorf("CanonicalPhone(%q, %q) = %q, want %q", tc.in, tc.country, got, tc.want)
		}
	}
}

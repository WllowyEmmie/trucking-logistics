package api

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// The console and the driver app keep separate cookies, so one browser can be
// signed in to both.
func TestSessionCookiesPerApp(t *testing.T) {
	for _, tc := range []struct {
		path, app string
		want      []string
	}{
		{"/auth/me", "driver", []string{driverSessionCookie}},
		{"/auth/me", "console", []string{sessionCookie}},
		{"/auth/me", "", []string{sessionCookie, driverSessionCookie}},
		{"/ws/driver", "", []string{driverSessionCookie}},
		{"/ws", "", []string{sessionCookie, driverSessionCookie}},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		if tc.app != "" {
			r.Header.Set(headerApp, tc.app)
		}
		if got := sessionCookies(r); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s with %s=%q: %v, want %v", tc.path, headerApp, tc.app, got, tc.want)
		}
	}
	if cookieFor(roleDriver) == cookieFor(roleDispatcher) {
		t.Error("drivers and dispatchers share a cookie")
	}
}

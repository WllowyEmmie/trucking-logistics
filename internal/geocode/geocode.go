// Package geocode turns an address into coordinates for delivery stops.
//
// The API serves it at GET /geocode so the console doesn't need a provider
// of its own, and so swapping the provider (Nominatim today; Mapbox or
// Google with a key later) is one more Geocoder, not a frontend change.
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Place is one match: a label to show and where it is.
type Place struct {
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
}

// Bounds is the operator's service area. Results outside it are dropped.
type Bounds struct {
	MinLat, MaxLat, MinLng, MaxLng float64
}

func (b Bounds) contains(lat, lng float64) bool {
	return lat >= b.MinLat && lat <= b.MaxLat && lng >= b.MinLng && lng <= b.MaxLng
}

// global is true when no service area is configured (the config defaults).
func (b Bounds) global() bool {
	return b.MinLat <= -90 && b.MaxLat >= 90 && b.MinLng <= -180 && b.MaxLng >= 180
}

type Geocoder interface {
	Search(ctx context.Context, q string, b Bounds, lang string) ([]Place, error)
}

const (
	maxResults = 5
	// Nominatim's usage policy: at most one request a second, from the whole app.
	minInterval = time.Second
	cacheTTL    = 24 * time.Hour
	cacheMax    = 1000
)

type cached struct {
	places []Place
	at     time.Time
}

// Nominatim is OpenStreetMap's free geocoder. Its policy asks for an
// identifying User-Agent, one request a second at most, and caching; all
// three are done here. https://operations.osmfoundation.org/policies/nominatim/
type Nominatim struct {
	baseURL   string
	userAgent string
	email     string
	http      *http.Client

	// Held across the wait, so concurrent searches queue one second apart.
	throttle sync.Mutex
	last     time.Time

	cacheMu sync.Mutex
	cache   map[string]cached
}

func NewNominatim(baseURL, userAgent, email string) *Nominatim {
	return &Nominatim{
		baseURL:   strings.TrimRight(baseURL, "/"),
		userAgent: userAgent,
		email:     email,
		http:      &http.Client{Timeout: 8 * time.Second},
		cache:     make(map[string]cached),
	}
}

func (n *Nominatim) Search(ctx context.Context, q string, b Bounds, lang string) ([]Place, error) {
	key := fmt.Sprintf("%s|%v|%s", lang, b, strings.ToLower(q))
	if places, ok := n.fromCache(key); ok {
		return places, nil
	}

	params := url.Values{
		"q":               {q},
		"format":          {"jsonv2"},
		"limit":           {strconv.Itoa(maxResults)},
		"accept-language": {lang},
	}
	if !b.global() {
		// viewbox is left,top,right,bottom; bounded=1 keeps results inside it.
		params.Set("viewbox", fmt.Sprintf("%g,%g,%g,%g", b.MinLng, b.MaxLat, b.MaxLng, b.MinLat))
		params.Set("bounded", "1")
	}
	if n.email != "" {
		params.Set("email", n.email)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.baseURL+"/search?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", n.userAgent)

	if err := n.wait(ctx); err != nil {
		return nil, err
	}
	res, err := n.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nominatim: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nominatim: %s", res.Status)
	}

	var raw []struct {
		DisplayName string `json:"display_name"`
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("nominatim: decode: %w", err)
	}

	places := make([]Place, 0, len(raw))
	for _, r := range raw {
		lat, err1 := strconv.ParseFloat(r.Lat, 64)
		lng, err2 := strconv.ParseFloat(r.Lon, 64)
		// bounded=1 already does this; checking again means a provider quirk
		// can't hand the console a stop the API would refuse.
		if err1 != nil || err2 != nil || !b.contains(lat, lng) {
			continue
		}
		places = append(places, Place{Label: r.DisplayName, Lat: lat, Lng: lng})
	}

	n.toCache(key, places)
	return places, nil
}

// wait blocks until a second has passed since the last request.
func (n *Nominatim) wait(ctx context.Context) error {
	n.throttle.Lock()
	defer n.throttle.Unlock()
	if d := minInterval - time.Since(n.last); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	n.last = time.Now()
	return nil
}

func (n *Nominatim) fromCache(key string) ([]Place, bool) {
	n.cacheMu.Lock()
	defer n.cacheMu.Unlock()
	c, ok := n.cache[key]
	if !ok || time.Since(c.at) > cacheTTL {
		return nil, false
	}
	return c.places, true
}

func (n *Nominatim) toCache(key string, places []Place) {
	n.cacheMu.Lock()
	defer n.cacheMu.Unlock()
	if len(n.cache) >= cacheMax {
		// Crude, but a dispatcher's searches are few: start over rather
		// than grow without bound.
		n.cache = make(map[string]cached)
	}
	n.cache[key] = cached{places: places, at: time.Now()}
}

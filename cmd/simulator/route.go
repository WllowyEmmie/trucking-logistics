package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// router finds a road route between two points with OSRM. An empty baseURL
// means straight lines only; any failure falls back to a straight line too,
// so a route problem never stops a truck.
type router struct {
	baseURL string
	http    *http.Client
}

func newRouter(baseURL string) *router {
	return &router{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

// route returns the path from a to b, and an error explaining a fallback
// to a straight line (the path is usable either way).
func (r *router) route(ctx context.Context, a, b position) (*path, error) {
	straight := newPath([]position{a, b}, false)
	if r.baseURL == "" {
		return straight, nil
	}
	pts, err := r.osrm(ctx, a, b)
	if err != nil {
		return straight, err
	}
	// OSRM snaps the ends to the nearest road; start and finish exactly
	// where the truck and the stop are.
	pts = append(append([]position{a}, pts...), b)
	return newPath(pts, true), nil
}

func (r *router) osrm(ctx context.Context, a, b position) ([]position, error) {
	url := fmt.Sprintf("%s/route/v1/driving/%f,%f;%f,%f?overview=full&geometries=geojson",
		r.baseURL, a.Lng, a.Lat, b.Lng, b.Lat)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "trucking-logistics-simulator")
	res, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("road router answered %s", res.Status)
	}
	var body struct {
		Code   string `json:"code"`
		Routes []struct {
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("road router: %w", err)
	}
	if body.Code != "Ok" || len(body.Routes) == 0 || len(body.Routes[0].Geometry.Coordinates) == 0 {
		return nil, fmt.Errorf("road router found no route (%s)", body.Code)
	}
	coords := body.Routes[0].Geometry.Coordinates
	pts := make([]position, len(coords))
	for i, c := range coords {
		pts[i] = position{Lat: c[1], Lng: c[0]}
	}
	return pts, nil
}

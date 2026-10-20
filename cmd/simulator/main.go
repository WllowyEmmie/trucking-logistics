// The simulator plays the drivers: for every delivery assigned to a truck it
// starts the delivery after a short wait, drives the truck along the roads
// to each stop (posting its position as a phone would), waits there a
// moment, completes the stop, finishes the delivery, and drives back to the
// depot (when Settings has one).
//
// It's for development and demos. It refuses an API that isn't on this
// machine unless you pass -allow-remote, so it can't feed made-up positions
// into a real company's system by accident.
//
//	go run ./cmd/simulator                 # every assigned truck
//	go run ./cmd/simulator -plates KF-01   # only these trucks
//	go run ./cmd/simulator -speed 20       # faster
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

func main() {
	_ = godotenv.Load()

	var (
		baseURL     = flag.String("api", envOr("SEED_API_URL", "http://localhost:8080"), "API base URL")
		speed       = flag.Float64("speed", 5, "speed multiplier; 1 is real time")
		kph         = flag.Float64("kph", 40, "road speed in km/h before the multiplier")
		tick        = flag.Duration("tick", 3*time.Second, "how often each truck reports its position")
		rescan      = flag.Duration("rescan", 5*time.Second, "how often to look for newly assigned deliveries")
		startAfter  = flag.Duration("start-after", 10*time.Second, "how long after assignment the driver starts")
		dwell       = flag.Duration("dwell", 20*time.Second, "time spent at each stop")
		failRate    = flag.Float64("fail-rate", 0, "chance (0 to 1) a delivery fails at a drop-off, customer unavailable")
		plates      = flag.String("plates", "", "comma-separated plates to move; empty moves every assigned truck")
		osrm        = flag.String("osrm", envOr("OSRM_URL", "https://router.project-osrm.org"), "OSRM server for road routes; empty for straight lines")
		allowRemote = flag.Bool("allow-remote", false, "allow an API that isn't on this machine")
		shifts      = flag.Bool("shifts", true, "put the drivers of the trucks it moves on shift, so auto mode uses them")
	)
	flag.Parse()

	if err := checkLocal(*baseURL, *allowRemote); err != nil {
		log.Fatal(err)
	}
	if *speed <= 0 || *kph <= 0 || *tick <= 0 || *failRate < 0 || *failRate > 1 {
		log.Fatal("speed, kph and tick must be positive, and fail-rate between 0 and 1")
	}

	c := client.New(*baseURL)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := c.Get(ctx, "/health", nil); err != nil {
		log.Fatalf("API not reachable at %s: %v", *baseURL, err)
	}
	// It starts deliveries and moves every truck, so it signs in as dispatch.
	// Make an account with: go run ./cmd/adduser
	email, password := os.Getenv("SEED_EMAIL"), os.Getenv("SEED_PASSWORD")
	if email == "" || password == "" {
		log.Fatal("set SEED_EMAIL and SEED_PASSWORD to a dispatcher account (create one with: go run ./cmd/adduser)")
	}
	if _, err := c.Login(ctx, email, password); err != nil {
		log.Fatalf("sign in as %s: %v", email, err)
	}

	cfg, err := c.GetConfig(ctx)
	if err != nil {
		log.Fatalf("read settings: %v", err)
	}
	home := position{Lat: cfg.CenterLat, Lng: cfg.CenterLng}
	var depot *position
	if cfg.Depot != nil {
		home = position{Lat: cfg.Depot.Lat, Lng: cfg.Depot.Lng}
		depot = &home
	}

	s := &sim{
		c:          c,
		routes:     newRouter(*osrm),
		now:        time.Now,
		rng:        rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 7)),
		kph:        *kph * *speed,
		metres:     *kph * *speed * 1000 / 3600 * tick.Seconds(),
		startAfter: *startAfter,
		dwell:      *dwell,
		failRate:   *failRate,
		plates:     plateSet(*plates),
		shifts:     *shifts,
		home:       home,
		depot:      depot,
		area:       &cfg.ServiceArea,
		runs:       map[string]*run{},
		at:         map[string]position{},
		returns:    map[string]*homeward{},
	}

	only := "every assigned truck"
	if len(s.plates) > 0 {
		only = "trucks " + *plates
	}
	roads := "roads from " + *osrm
	if *osrm == "" {
		roads = "straight lines"
	}
	fmt.Printf("simulator: %s, %.0f km/h (x%g), %s; starts %s after assignment, %s at each stop\nctrl-c to stop\n",
		only, s.kph, *speed, roads, *startAfter, *dwell)

	ticker := time.NewTicker(*tick)
	defer ticker.Stop()
	var jobs []work
	var lastScan time.Time
	for {
		if time.Since(lastScan) >= *rescan {
			if w, err := s.scan(ctx); err != nil {
				log.Printf("scan: %v", err)
			} else {
				jobs = w
			}
			lastScan = time.Now()
		}
		for _, w := range jobs {
			if err := s.step(ctx, w); err != nil {
				// One truck's problem is not the whole run's problem.
				log.Printf("%s: %v", w.d.Reference, err)
			}
		}
		s.drive(ctx)
		select {
		case <-ctx.Done():
			fmt.Println("\nstopped")
			return
		case <-ticker.C:
		}
	}
}

// checkLocal refuses an API on another machine unless allowed: a real
// company's system must never get made-up truck positions by accident.
func checkLocal(raw string, allowRemote bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("-api %q isn't a URL like http://localhost:8080", raw)
	}
	if allowRemote || isLocalHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("the API at %s isn't on this machine. The simulator posts made-up truck positions and completes deliveries; pass -allow-remote only for a test server", u.Host)
}

func isLocalHost(h string) bool {
	h = strings.ToLower(h)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func plateSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out[p] = true
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

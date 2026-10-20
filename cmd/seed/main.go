package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

var places = []struct {
	Name string
	Lat  float64
	Lng  float64
}{
	{"Apapa Wharf", 6.4433, 3.3647},
	{"Tin Can Island Port", 6.4389, 3.3344},
	{"Lekki Phase 1", 6.4474, 3.4736},
	{"Victoria Island", 6.4281, 3.4219},
	{"Ikeja GRA", 6.5833, 3.3500},
	{"Oshodi", 6.5556, 3.3458},
	{"Surulere", 6.4969, 3.3481},
	{"Yaba", 6.5095, 3.3711},
	{"Ikorodu", 6.6194, 3.5106},
	{"Agege", 6.6152, 3.3225},
	{"Festac Town", 6.4653, 3.2808},
	{"Ajah", 6.4667, 3.5667},
}

var trucks = []struct {
	Plate string
	Model string
}{
	{"LAG-204-KJ", "Isuzu NPR"},
	{"LAG-318-XA", "Mitsubishi Canter"},
	{"LAG-451-BC", "Hino 300"},
	{"LAG-577-ME", "Mercedes-Benz Atego"},
	{"LAG-690-QT", "Isuzu FVR"},
	{"LAG-712-ZD", "MAN TGM"},
}

var drivers = []struct {
	Name  string
	Phone string
}{
	{"Chidi Okafor", "+2348012345001"},
	{"Amaka Obi", "+2348012345002"},
	{"Tunde Bakare", "+2348012345003"},
	{"Ngozi Eze", "+2348012345004"},
	{"Segun Adeyemi", "+2348012345005"},
	{"Fatima Yusuf", "+2348012345006"},
}
var plan = []struct {
	Target string
	Stops  int
}{
	{"pending", 2},
	{"pending", 3},
	{"pending", 2},
	{"assigned", 2},
	{"assigned", 4},
	{"in_transit", 2},
	{"in_transit", 3},
	{"completed", 2},
	{"cancelled", 2},
	{"failed", 3},
}

func main() {
	_ = godotenv.Load()

	var (
		baseURL = flag.String("api", envOr("SEED_API_URL", "http://localhost:8080"), "API base URL")
		doReset = flag.Bool("reset", false, "wipe all data before seeding")
	)
	flag.Parse()

	ctx := context.Background()
	c := client.New(*baseURL)

	if err := c.Get(ctx, "/health", nil); err != nil {
		log.Fatalf("API not reachable at %s: %v\n\nStart it with: go run ./cmd/api", *baseURL, err)
	}

	// The API needs a dispatcher sign-in. Make one with:
	//   go run ./cmd/adduser -email you@example.com -name "Your Name"
	email, password := os.Getenv("SEED_EMAIL"), os.Getenv("SEED_PASSWORD")
	if email == "" || password == "" {
		log.Fatal("set SEED_EMAIL and SEED_PASSWORD to a dispatcher account (create one with: go run ./cmd/adduser)")
	}
	if _, err := c.Login(ctx, email, password); err != nil {
		log.Fatalf("sign in as %s: %v", email, err)
	}

	if *doReset {
		dsn := envOr("DATABASE_URL", "")
		if err := reset(ctx, dsn); err != nil {
			log.Fatalf("reset: %v", err)
		}
		fmt.Println("wiped existing data")
	}

	if err := seed(ctx, c); err != nil {
		log.Fatalf("seed: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// The only place the seeder touches Postgres directly: there is no delete
// endpoint, and there shouldn't be one.
func reset(ctx context.Context, dsn string) error {
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}
	if !strings.Contains(dsn, "127.0.0.1") && !strings.Contains(dsn, "localhost") {
		return fmt.Errorf("refusing to reset a non-local database: %s", dsn)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		TRUNCATE locations, delivery_events, delivery_stops, deliveries,
		         trucks, drivers, proposals
		RESTART IDENTITY CASCADE`)
	if err != nil {
		return err
	}
	// Drivers are gone, so are their sign-ins. Dispatcher accounts stay.
	if _, err = pool.Exec(ctx, `DELETE FROM sessions WHERE role = 'driver'`); err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `ALTER SEQUENCE delivery_ref_seq RESTART 10001`)
	return err
}

// The PIN every seeded driver gets. Dev data only.
var seedPIN = envOr("SEED_DRIVER_PIN", "1234")

var customers = []string{
	"Chidi Okafor", "Adebayo Logistics", "Zainab Trading Co",
	"Lagos Steel Works", "Emeka Distributors", "Folake Imports",
}

var goods = []string{
	"Building materials", "Frozen goods", "Electronics", "Pharmaceuticals",
}

func buildDelivery(n int, stops int) client.CreateDeliveryInput {
	in := client.CreateDeliveryInput{
		CustomerName: customers[n%len(customers)],
	}
	g := goods[n%len(goods)]
	in.GoodsDesc = &g

	for i := 0; i < stops; i++ {
		p := places[(n*3+i)%len(places)]
		s := client.StopInput{
			StopType: "dropoff",
			Address:  p.Name,
			Lat:      p.Lat,
			Lng:      p.Lng,
		}
		if i == 0 {
			s.StopType = "pickup"
		}
		in.Stops = append(in.Stops, s)
	}
	return in
}
func pickAvailable(ctx context.Context, c *client.Client) (string, error) {
	ts, err := c.ListTrucks(ctx)
	if err != nil {
		return "", fmt.Errorf("list trucks: %w", err)
	}
	for _, t := range ts {
		if t.Status == "available" {
			return t.ID, nil
		}
	}
	return "", errors.New("no available trucks left; the seed plan needs more trucks or fewer concurrent deliveries")
}

// walkTo drives a freshly created delivery to the target state by calling the
// same endpoints a dispatcher and driver would. A state that can't be reached
// is a bug in the seed plan, not something to paper over.
func walkTo(ctx context.Context, c *client.Client, d client.CreateDeliveryResult, target, truckID string) error {
	id := d.Delivery.ID

	if target == "pending" {
		return nil
	}

	if target == "cancelled" {
		return c.CancelDelivery(ctx, id)
	}

	if err := c.AssignTruck(ctx, id, truckID); err != nil {
		return fmt.Errorf("assign: %w", err)
	}
	if target == "assigned" {
		return nil
	}

	if err := c.StartDelivery(ctx, id); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if target == "in_transit" {
		return nil
	}

	if target == "failed" {
		return c.FailDelivery(ctx, id, "breakdown")
	}

	// completed: every stop must be walked arrive → complete, in order,
	// before the delivery itself can complete.
	for _, s := range d.Stops {
		if err := c.ArriveAtStop(ctx, id, s.ID); err != nil {
			return fmt.Errorf("arrive at stop %d: %w", s.Sequence, err)
		}
		if err := c.CompleteStop(ctx, id, s.ID); err != nil {
			return fmt.Errorf("complete stop %d: %w", s.Sequence, err)
		}
	}
	return c.CompleteDelivery(ctx, id)
}
func seed(ctx context.Context, c *client.Client) error {
	truckIDs := make([]string, 0, len(trucks))
	driverIDs := make([]string, 0, len(drivers))
	for _, t := range trucks {
		model := t.Model
		created, err := c.CreateTruck(ctx, t.Plate, &model)
		if err != nil {
			return fmt.Errorf("create truck %s: %w", t.Plate, err)
		}
		truckIDs = append(truckIDs, created.ID)
	}
	for _, d := range drivers {
		created, err := c.CreateDriver(ctx, d.Name, d.Phone)
		if err != nil {
			return fmt.Errorf("create driver %s: %w", d.Name, err)
		}
		driverIDs = append(driverIDs, created.ID)
		// So the driver app can be tried with any seeded driver.
		if err := c.SetDriverPIN(ctx, created.ID, seedPIN); err != nil {
			return fmt.Errorf("set PIN for %s: %w", d.Name, err)
		}
	}
	fmt.Printf("seeded drivers sign in to the app with their phone number and PIN %s\n", seedPIN)

	for i := range truckIDs {
		if err := c.UpdateTruck(ctx, truckIDs[i], &driverIDs[i], nil); err != nil {
			return fmt.Errorf("link driver to truck %s: %w", trucks[i].Plate, err)
		}
	}
	for i, p := range plan {
		in := buildDelivery(i, p.Stops)
		d, err := c.CreateDelivery(ctx, in)
		if err != nil {
			return fmt.Errorf("create delivery %d: %w", i+1, err)
		}

		truckID := ""
		if p.Target != "pending" && p.Target != "cancelled" {
			truckID, err = pickAvailable(ctx, c)
			if err != nil {
				return err
			}
		}

		if err := walkTo(ctx, c, d, p.Target, truckID); err != nil {
			return fmt.Errorf("%s -> %s: %w", d.Delivery.Reference, p.Target, err)
		}
	}
	fmt.Printf("seeded %d trucks, %d drivers, %d deliveries\n",
		len(truckIDs), len(driverIDs), len(plan))
	return nil
}

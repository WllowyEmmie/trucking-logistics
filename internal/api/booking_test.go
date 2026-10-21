package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
	"github.com/WllowyEmmie/trucking-logistics/internal/config"
)

// fakePaystack: initialize hands out a checkout address and remembers the
// amount; verify says what the test set.
type fakePaystack struct {
	mu     sync.Mutex
	amount map[string]int64
	status map[string]string
}

func (f *fakePaystack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer sk_test_x" {
		w.WriteHeader(401)
		w.Write([]byte(`{"status":false,"message":"Invalid key"}`))
		return
	}
	switch {
	case r.URL.Path == "/transaction/initialize":
		var b struct {
			Amount    int64  `json:"amount"`
			Reference string `json:"reference"`
			Callback  string `json:"callback_url"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		f.amount[b.Reference] = b.Amount
		json.NewEncoder(w).Encode(map[string]any{"status": true, "message": "ok",
			"data": map[string]any{"authorization_url": "https://checkout.example/" + b.Reference, "reference": b.Reference}})
	case strings.HasPrefix(r.URL.Path, "/transaction/verify/"):
		ref := strings.TrimPrefix(r.URL.Path, "/transaction/verify/")
		st := f.status[ref]
		if st == "" {
			st = "ongoing"
		}
		json.NewEncoder(w).Encode(map[string]any{"status": true, "message": "ok",
			"data": map[string]any{"status": st, "amount": f.amount[ref], "currency": "NGN"}})
	default:
		w.WriteHeader(404)
	}
}

func booking() map[string]any {
	return map[string]any{
		"name": "Ada Eze", "email": "ada@example.com", "phone": "+2348031234567",
		"goods_desc": "Two fridges", "accept_terms": true,
		"stops": []map[string]any{
			{"stop_type": "pickup", "address": "Apapa Wharf", "lat": 6.4433, "lng": 3.3647},
			{"stop_type": "dropoff", "address": "Ikeja", "lat": 6.6018, "lng": 3.3515, "contact_name": "Bola"},
		},
	}
}

func TestQuote(t *testing.T) {
	st := &config.Settings{PriceBase: 5000, PricePerKm: 400, PriceMinimum: 10000}
	lat1, lng1, lat2, lng2 := 6.4433, 3.3647, 6.6018, 3.3515
	q := quote(st, []stopInput{{Lat: &lat1, Lng: &lng1}, {Lat: &lat2, Lng: &lng2}})
	// About 17.7 km in a straight line, so ~23 km by road: 5000 + 400×23 ≈ 14,200.
	if q.DistanceKm < 22 || q.DistanceKm > 24 || q.Amount%100 != 0 || q.Amount < 13800 || q.Amount > 14700 {
		t.Fatalf("quote %+v", q)
	}
	st.PriceMinimum = 50000
	if q := quote(st, []stopInput{{Lat: &lat1, Lng: &lng1}, {Lat: &lat2, Lng: &lng2}}); q.Amount != 50000 {
		t.Fatalf("minimum: %+v", q)
	}
}

// Book, pay, come back: one pending delivery, with a tracking link for the
// customer. Coming back twice, or the webhook as well, doesn't make two.
func TestBookAndPay(t *testing.T) {
	pay := &fakePaystack{amount: map[string]int64{}, status: map[string]string{}}
	ps := httptest.NewServer(pay)
	defer ps.Close()
	t.Setenv("PAYSTACK_URL", ps.URL)
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_x")
	e := setup(t)
	ctx := context.Background()
	public := client.New(e.srv.URL)

	if got := status(public.Post(ctx, "/book", booking(), nil)); got != 503 {
		t.Fatalf("booking closed: %d", got)
	}
	st := *e.s.st()
	st.BookingEnabled = true
	e.s.settings.Store(&st)

	var quoted quoteOut
	if err := public.Post(ctx, "/book/quote", map[string]any{"stops": booking()["stops"]}, &quoted); err != nil || quoted.Amount == 0 {
		t.Fatalf("quote: %+v %v", quoted, err)
	}
	noTerms := booking()
	noTerms["accept_terms"] = false
	if got := status(public.Post(ctx, "/book", noTerms, nil)); got != 400 {
		t.Fatalf("terms not accepted: %d", got)
	}

	var made struct {
		Reference   string `json:"reference"`
		CheckoutURL string `json:"checkout_url"`
		Amount      int64  `json:"amount"`
	}
	if err := public.Post(ctx, "/book", booking(), &made); err != nil || !strings.HasPrefix(made.Reference, "bk_") ||
		made.CheckoutURL != "https://checkout.example/"+made.Reference || made.Amount != quoted.Amount {
		t.Fatalf("book: %+v %v", made, err)
	}
	if pay.amount[made.Reference] != quoted.Amount*100 {
		t.Fatalf("paystack asked for %d kobo, quote was ₦%d", pay.amount[made.Reference], quoted.Amount)
	}

	var got bookingStatusOut
	if err := public.Get(ctx, "/book/"+made.Reference, &got); err != nil || got.Status != "awaiting_payment" || got.TrackingToken != nil {
		t.Fatalf("before paying: %+v %v", got, err)
	}
	pay.mu.Lock()
	pay.status[made.Reference] = "success"
	pay.mu.Unlock()
	for i := 0; i < 2; i++ {
		if err := public.Get(ctx, "/book/"+made.Reference, &got); err != nil || got.Status != "paid" || got.TrackingToken == nil || got.Delivery == nil {
			t.Fatalf("after paying: %+v %v", got, err)
		}
	}
	// The webhook for the same payment changes nothing.
	body := []byte(`{"event":"charge.success","data":{"reference":"` + made.Reference + `","amount":` +
		jsonInt(quoted.Amount*100) + `,"currency":"NGN","status":"success"}}`)
	if code := postWebhook(t, e, body, sign(body)); code != 200 {
		t.Fatalf("webhook: %d", code)
	}
	pending, err := e.c.ListDeliveries(ctx, "pending")
	if err != nil || len(pending) != 1 || pending[0].Reference != *got.Delivery {
		t.Fatalf("pending deliveries: %+v %v", pending, err)
	}
	detail, err := e.c.GetDelivery(ctx, pending[0].ID)
	if err != nil || len(detail.Stops) != 2 {
		t.Fatalf("delivery: %+v %v", detail, err)
	}
}

func TestWebhookSignatureAndAmount(t *testing.T) {
	pay := &fakePaystack{amount: map[string]int64{}, status: map[string]string{}}
	ps := httptest.NewServer(pay)
	defer ps.Close()
	t.Setenv("PAYSTACK_URL", ps.URL)
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_x")
	e := setup(t)
	ctx := context.Background()
	st := *e.s.st()
	st.BookingEnabled = true
	e.s.settings.Store(&st)
	var made struct {
		Reference string `json:"reference"`
		Amount    int64  `json:"amount"`
	}
	if err := client.New(e.srv.URL).Post(ctx, "/book", booking(), &made); err != nil {
		t.Fatal(err)
	}
	ev := func(amount int64) []byte {
		return []byte(`{"event":"charge.success","data":{"reference":"` + made.Reference + `","amount":` + jsonInt(amount) + `,"currency":"NGN"}}`)
	}
	if code := postWebhook(t, e, ev(made.Amount*100), "00ff"); code != 401 {
		t.Fatalf("forged webhook: %d", code)
	}
	short := ev(100)
	postWebhook(t, e, short, sign(short))
	if ps, _ := e.c.ListDeliveries(ctx, "pending"); len(ps) != 0 {
		t.Fatal("a payment for the wrong amount made a delivery")
	}
	good := ev(made.Amount * 100)
	if code := postWebhook(t, e, good, sign(good)); code != 200 {
		t.Fatalf("webhook: %d", code)
	}
	if ps, _ := e.c.ListDeliveries(ctx, "pending"); len(ps) != 1 {
		t.Fatalf("after the webhook: %d pending", len(ps))
	}
}

func sign(body []byte) string {
	m := hmac.New(sha512.New, []byte("sk_test_x"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func postWebhook(t *testing.T, e *env, body []byte, sig string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+"/paystack/webhook", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-paystack-signature", sig)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

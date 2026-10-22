package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/config"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/WllowyEmmie/trucking-logistics/internal/eta"
)

// The public booking page: anyone can get a price and book a delivery,
// paying with Paystack. A paid booking becomes a pending delivery, and
// auto mode or a dispatcher takes it from there.
//
//	GET  /book/geocode?q=   address search (limited per visitor)
//	POST /book/quote        {stops} → distance and price
//	POST /book              the booking → Paystack checkout address
//	GET  /book/{reference}  where it stands; checks with Paystack, and
//	                        once paid gives the tracking link
//	POST /paystack/webhook  Paystack says a payment went through

// Straight lines are shorter than roads: the quote multiplies by this.
const roadFactor = 1.3

var (
	bookPhoneRE = regexp.MustCompile(`^\+[0-9]{7,15}$`)
	bookEmailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

type bookingInput struct {
	Name        string      `json:"name"`
	Email       string      `json:"email"`
	Phone       string      `json:"phone"`
	GoodsDesc   *string     `json:"goods_desc"`
	Stops       []stopInput `json:"stops"`
	AcceptTerms bool        `json:"accept_terms"`
}

type quoteOut struct {
	DistanceKm float64 `json:"distance_km"`
	// Naira.
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// quote: base + per km over the estimated road distance through the stops
// in order, at least the minimum, rounded up to the next ₦100.
func quote(st *config.Settings, stops []stopInput) quoteOut {
	km := 0.0
	for i := 1; i < len(stops); i++ {
		km += eta.KM(eta.Point{Lat: *stops[i-1].Lat, Lng: *stops[i-1].Lng}, eta.Point{Lat: *stops[i].Lat, Lng: *stops[i].Lng})
	}
	km = math.Round(km*roadFactor*10) / 10
	price := float64(st.PriceBase) + float64(st.PricePerKm)*km
	price = math.Max(price, float64(st.PriceMinimum))
	return quoteOut{DistanceKm: km, Amount: int64(math.Ceil(price/100) * 100), Currency: "NGN"}
}

// visitor: who's asking, for the per-visitor limits. Behind Vercel and
// Render the first X-Forwarded-For entry is the browser's address.
func visitor(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allow counts a use against each key and says whether it's within limits.
func allow(t *throttle, keys ...string) bool {
	if t.blocked(keys...) {
		return false
	}
	t.fail(keys...)
	return true
}

func tooMany(w http.ResponseWriter) {
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many tries from here; wait a few minutes"})
}

// bookGeocode is the address search for the booking page: the console's,
// limited so the public can't run up the free geocoder's limits.
func (s *Server) bookGeocode(w http.ResponseWriter, r *http.Request) {
	if !allow(s.bookGeoLimit, "ip:"+visitor(r)) || !allow(s.bookGeoGlobal, "all") {
		tooMany(w)
		return
	}
	s.geocodeAddress(w, r)
}

func (s *Server) checkBookingStops(stops []stopInput) string {
	return s.validateDeliveryInput(createDeliveryInput{CustomerName: "-", Stops: stops})
}

func (s *Server) bookQuote(w http.ResponseWriter, r *http.Request) {
	if !allow(s.bookQuoteLimit, "ip:"+visitor(r)) {
		tooMany(w)
		return
	}
	var body struct {
		Stops []stopInput `json:"stops"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if len(body.Stops) > 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at most 5 drop-offs"})
		return
	}
	for i := range body.Stops {
		if body.Stops[i].Address == "" {
			body.Stops[i].Address = "-" // the address isn't needed to price it
		}
	}
	if msg := s.checkBookingStops(body.Stops); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, quote(s.st(), body.Stops))
}

func (s *Server) book(w http.ResponseWriter, r *http.Request) {
	st := s.st()
	switch {
	case !st.BookingEnabled:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "online booking is closed; please contact us instead"})
		return
	case s.paystack == nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "online payment isn't set up yet; please contact us instead"})
		return
	case !allow(s.bookLimit, "ip:"+visitor(r)):
		tooMany(w)
		return
	}
	var in bookingInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	in.Name, in.Email, in.Phone = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Email)), strings.TrimSpace(in.Phone)
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	switch {
	case in.Name == "" || len(in.Name) > 120:
		bad("your name is needed (up to 120 characters)")
		return
	case !bookEmailRE.MatchString(in.Email) || len(in.Email) > 200:
		bad("an email address is needed, for the receipt")
		return
	case !bookPhoneRE.MatchString(in.Phone):
		bad("a phone number is needed, like +2348031234567")
		return
	case in.GoodsDesc != nil && len(*in.GoodsDesc) > 500:
		bad("the goods description is too long (500 characters at most)")
		return
	case len(in.Stops) > 6:
		bad("at most 5 drop-offs")
		return
	case !in.AcceptTerms:
		bad("please accept the delivery terms")
		return
	}
	for i := range in.Stops {
		in.Stops[i].Address = strings.TrimSpace(in.Stops[i].Address)
		if len(in.Stops[i].Address) > 300 {
			bad("an address is too long")
			return
		}
	}
	if msg := s.checkBookingStops(in.Stops); msg != "" {
		bad(msg)
		return
	}

	q := quote(st, in.Stops)
	token, err := newTrackingToken()
	if err != nil {
		s.respondErr(w, err, "booking reference", "could not take the booking")
		return
	}
	stops, _ := json.Marshal(in.Stops)
	b, err := s.queries.InsertBooking(r.Context(), db.Booking{
		Reference: "bk_" + token, CustomerName: in.Name, Email: in.Email, Phone: in.Phone,
		GoodsDesc: in.GoodsDesc, Stops: stops, DistanceKm: q.DistanceKm, Amount: q.Amount * 100, Currency: q.Currency,
	})
	if err != nil {
		s.respondErr(w, err, "save booking", "could not take the booking")
		return
	}
	checkout, err := s.paystack.Initialize(r.Context(), b.Email, b.Amount, b.Currency, b.Reference,
		s.cfg.PublicSiteURL+"/book/done", map[string]any{"customer": b.CustomerName, "distance_km": b.DistanceKm})
	if err != nil {
		s.logger.Error("paystack initialize", "err", err, "reference", b.Reference)
		_ = s.queries.MarkBookingFailed(r.Context(), b.ID)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the payment page couldn't be opened; try again in a moment"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"reference": b.Reference, "checkout_url": checkout,
		"amount": q.Amount, "currency": q.Currency, "distance_km": q.DistanceKm,
	})
}

type bookingStatusOut struct {
	Reference string  `json:"reference"`
	Status    string  `json:"status"`
	Amount    int64   `json:"amount"` // naira
	Currency  string  `json:"currency"`
	Delivery  *string `json:"delivery_reference,omitempty"`
	// The customer's tracking page: /t/{token} on the website.
	TrackingToken *string `json:"tracking_token,omitempty"`
}

// bookingStatus is GET /book/{reference}: the payment page sends the
// customer back here. Paystack is asked directly, since arriving back
// doesn't prove anything was paid.
func (s *Server) bookingStatus(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	b, err := s.queries.GetBooking(r.Context(), ref)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "booking not found"})
		return
	}
	if err != nil {
		s.respondErr(w, err, "get booking", "could not check the booking")
		return
	}
	if b.Status == "awaiting_payment" && s.paystack != nil {
		pay, err := s.paystack.Verify(r.Context(), b.Reference)
		if err != nil {
			// Not paid yet, or Paystack can't be reached: say it's waiting.
			s.logger.Info("paystack verify", "err", err, "reference", b.Reference)
		} else if pay.Status == "success" {
			if b, err = s.settleBooking(r.Context(), b.Reference, pay.Amount, pay.Currency); err != nil {
				s.respondErr(w, err, "settle booking", "could not confirm the booking")
				return
			}
		} else if pay.Status == "failed" || pay.Status == "abandoned" {
			_ = s.queries.MarkBookingFailed(r.Context(), b.ID)
			b.Status = "failed"
		}
	}
	out := bookingStatusOut{Reference: b.Reference, Status: b.Status, Amount: b.Amount / 100, Currency: b.Currency}
	if b.DeliveryID != nil {
		dref, token, err := s.queries.DeliveryLink(r.Context(), *b.DeliveryID)
		if err == nil {
			out.Delivery, out.TrackingToken = &dref, &token
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// paystackWebhook is POST /paystack/webhook. Only a correctly signed
// charge.success counts; anything else is acknowledged and ignored.
func (s *Server) paystackWebhook(w http.ResponseWriter, r *http.Request) {
	if s.paystack == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || !s.paystack.ValidSignature(body, r.Header.Get("x-paystack-signature")) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad signature"})
		return
	}
	var ev struct {
		Event string `json:"event"`
		Data  struct {
			Reference string `json:"reference"`
			Amount    int64  `json:"amount"`
			Currency  string `json:"currency"`
			Status    string `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &ev) == nil && ev.Event == "charge.success" && strings.HasPrefix(ev.Data.Reference, "bk_") {
		if _, err := s.settleBooking(r.Context(), ev.Data.Reference, ev.Data.Amount, ev.Data.Currency); err != nil {
			s.logger.Error("paystack webhook", "err", err, "reference", ev.Data.Reference)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// settleBooking turns a paid booking into a pending delivery, once: the
// webhook and the customer's return may both get here.
func (s *Server) settleBooking(ctx context.Context, reference string, paid int64, currency string) (db.Booking, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return db.Booking{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)
	b, err := qtx.GetBookingForUpdate(ctx, reference)
	if err != nil {
		return b, err
	}
	if b.Status == "paid" {
		return b, nil
	}
	if paid != b.Amount || !strings.EqualFold(currency, b.Currency) {
		return b, fmt.Errorf("booking %s: paid %d %s, expected %d %s", reference, paid, currency, b.Amount, b.Currency)
	}
	var stops []stopInput
	if err := json.Unmarshal(b.Stops, &stops); err != nil {
		return b, err
	}
	phone := b.Phone
	note := fmt.Sprintf("booked online by %s (%s), paid ₦%s, %s", b.CustomerName, b.Email, naira(b.Amount/100), b.Reference)
	d, _, err := s.insertDelivery(ctx, qtx, createDeliveryInput{
		CustomerName: b.CustomerName, CustomerPhone: &phone, GoodsDesc: b.GoodsDesc, Stops: stops,
	}, note)
	if err != nil {
		return b, err
	}
	if err := qtx.MarkBookingPaid(ctx, b.ID, d.ID); err != nil {
		return b, err
	}
	if err := tx.Commit(ctx); err != nil {
		return b, err
	}
	now := time.Now()
	b.Status, b.PaidAt, b.DeliveryID = "paid", &now, &d.ID
	s.logger.Info("booking paid", "reference", reference, "delivery", d.Reference)
	// Not a request the announce middleware sees (it can be a GET): tell
	// the consoles and the agent here, so auto mode picks it up at once.
	s.hub.broadcast(wsMessage{Type: "changed", Topics: []string{"deliveries"}, DeliveryID: d.ID.String()})
	return b, nil
}

// naira: 25000 → "25,000".
func naira(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

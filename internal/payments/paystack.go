// Package payments talks to Paystack: start a payment, check one, and
// check that a webhook really came from Paystack.
package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Paystack struct {
	base   string
	secret string
	hc     *http.Client
}

// New returns nil when there's no secret key: payments are off.
func New(base, secret string) *Paystack {
	if secret == "" {
		return nil
	}
	return &Paystack{base: base, secret: secret, hc: &http.Client{Timeout: 15 * time.Second}}
}

type envelope struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (p *Paystack) call(ctx context.Context, method, path string, body any, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.secret)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.hc.Do(req)
	if err != nil {
		return fmt.Errorf("paystack unreachable: %w", err)
	}
	defer res.Body.Close()
	var env envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return fmt.Errorf("paystack %s %s: %d, unreadable answer", method, path, res.StatusCode)
	}
	if res.StatusCode >= 300 || !env.Status {
		return fmt.Errorf("paystack %s %s: %d %s", method, path, res.StatusCode, env.Message)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// Initialize starts a payment and returns the checkout page to send the
// customer to. amount is in kobo.
func (p *Paystack) Initialize(ctx context.Context, email string, amount int64, currency, reference, callbackURL string, metadata map[string]any) (string, error) {
	var data struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	err := p.call(ctx, http.MethodPost, "/transaction/initialize", map[string]any{
		"email": email, "amount": amount, "currency": currency, "reference": reference,
		"callback_url": callbackURL, "metadata": metadata,
	}, &data)
	if err == nil && data.AuthorizationURL == "" {
		err = errors.New("paystack gave no checkout address")
	}
	return data.AuthorizationURL, err
}

// Payment is what Paystack says about a transaction.
type Payment struct {
	Status   string `json:"status"` // "success" once paid
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func (p *Paystack) Verify(ctx context.Context, reference string) (Payment, error) {
	var out Payment
	err := p.call(ctx, http.MethodGet, "/transaction/verify/"+url.PathEscape(reference), nil, &out)
	return out, err
}

// ValidSignature: Paystack signs each webhook body with HMAC-SHA512 using
// the secret key, in the x-paystack-signature header (hex).
func (p *Paystack) ValidSignature(body []byte, signature string) bool {
	given, err := hex.DecodeString(signature)
	if err != nil || len(given) == 0 {
		return false
	}
	mac := hmac.New(sha512.New, []byte(p.secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), given)
}

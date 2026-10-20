package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
	// Sent as "Authorization: Bearer <token>": a session token from Login,
	// or a service key from WithToken.
	token string
}

// WithToken returns a copy that authenticates with token (e.g. the agent's
// service key).
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.token = token
	return &cp
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// APIError carries the server's own message and status.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%d: %s", e.Status, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return &APIError{Status: res.StatusCode, Message: e.Error}
	}

	if path == "/auth/login" || path == "/auth/driver-login" {
		// The session comes back as a cookie; this client sends it as a
		// bearer token instead, so it works over plain http too.
		for _, ck := range res.Cookies() {
			if ck.Name == "session" && ck.Value != "" {
				c.token = ck.Value
			}
		}
	}

	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Me is GET /auth/me.
type Me struct {
	Role    string  `json:"role"`
	ID      *string `json:"id"`
	Name    string  `json:"name"`
	Email   string  `json:"email"`
	TruckID *string `json:"truck_id"`
}

// Login signs in as a dispatcher; later calls on c carry the session.
func (c *Client) Login(ctx context.Context, email, password string) (Me, error) {
	var me Me
	return me, c.Post(ctx, "/auth/login", map[string]string{"email": email, "password": password}, &me)
}

// DriverLogin signs in as a driver with their phone and PIN.
func (c *Client) DriverLogin(ctx context.Context, phone, pin string) (Me, error) {
	var me Me
	return me, c.Post(ctx, "/auth/driver-login", map[string]string{"phone": phone, "pin": pin}, &me)
}

func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}
func (c *Client) Patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, body, out)
}

// Token is the session token or key this client sends, if any.
func (c *Client) Token() string { return c.token }

func (c *Client) Put(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Error classes. The whole resilience design hangs on telling these apart:
//
//	ErrUnavailable -> retry it, and count it against the circuit breaker
//	ErrDeclined    -> never retry, never count (the gateway is healthy!)
var (
	ErrUnavailable = errors.New("payment gateway unavailable")
	ErrDeclined    = errors.New("card declined")
	ErrNotFound    = errors.New("charge not found")
)

type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient: always set a timeout. Without one a hung gateway would hold our
// goroutines (and the user's request) forever and the breaker never learns.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

type ChargeResult struct {
	Charge
	Replayed bool `json:"replayed"` // gateway returned a stored result for this key
}

func (c *Client) Charge(ctx context.Context, idempotencyKey string, amountCents int64) (ChargeResult, error) {
	body, _ := json.Marshal(map[string]int64{"amountCents": amountCents})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/charges", bytes.NewReader(body))
	if err != nil {
		return ChargeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := c.http.Do(req)
	if err != nil {
		// Timeouts and connection errors: the outcome is UNKNOWN. Retrying is
		// safe only because the idempotency key makes the charge exactly-once.
		return ChargeResult{}, fmt.Errorf("%w: %s", ErrUnavailable, describe(err))
	}
	defer resp.Body.Close()

	var res ChargeResult
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(&res.Charge); err != nil {
			return ChargeResult{}, fmt.Errorf("%w: bad response: %v", ErrUnavailable, err)
		}
		res.Replayed = resp.Header.Get("Idempotent-Replayed") == "true"
		return res, nil
	case resp.StatusCode == http.StatusPaymentRequired:
		_ = json.NewDecoder(resp.Body).Decode(&res.Charge)
		return res, ErrDeclined
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return ChargeResult{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	default:
		// Other 4xx are bugs on our side; retrying won't help.
		return ChargeResult{}, fmt.Errorf("gateway rejected request: HTTP %d", resp.StatusCode)
	}
}

func (c *Client) GetCharge(ctx context.Context, idempotencyKey string) (Charge, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/charges/"+idempotencyKey, nil)
	if err != nil {
		return Charge{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Charge{}, fmt.Errorf("%w: %s", ErrUnavailable, describe(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		var ch Charge
		err := json.NewDecoder(resp.Body).Decode(&ch)
		return ch, err
	case resp.StatusCode == http.StatusNotFound:
		return Charge{}, ErrNotFound
	default:
		return Charge{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
}

// describe turns long net/http errors into a short reason for logs and UI.
func describe(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout (no response in time - outcome unknown)"
	}
	return "connection error"
}

func (c *Client) AdminGet(ctx context.Context) (AdminView, error) {
	return c.admin(ctx, http.MethodGet, nil)
}

func (c *Client) AdminSet(ctx context.Context, s Settings) (AdminView, error) {
	return c.admin(ctx, http.MethodPut, &s)
}

func (c *Client) admin(ctx context.Context, method string, s *Settings) (AdminView, error) {
	var body *bytes.Reader
	if s != nil {
		b, _ := json.Marshal(s)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/admin/settings", body)
	if err != nil {
		return AdminView{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return AdminView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return AdminView{}, fmt.Errorf("gateway admin: %s", e.Error)
	}
	var v AdminView
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v, err
}

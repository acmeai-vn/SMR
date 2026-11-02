// Package classifier talks only to ACME AI's classification API. It never sees
// Jev credentials and never logs request state.
package classifier

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	ErrPaymentRequired = errors.New("classifier: payment required")
	ErrUnauthorized    = errors.New("classifier: unauthorized")
	ErrTransient       = errors.New("classifier: transient failure")
	ErrPermanent       = errors.New("classifier: permanent failure")
)

type Response struct {
	RequestID     string             `json:"request_id"`
	Tier          string             `json:"tier"`
	Cache         string             `json:"cache"`
	ChargedVND    int64              `json:"charged_vnd"`
	BalanceVND    int64              `json:"balance_vnd"`
	InputTokens   int64              `json:"input_tokens"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type Account struct {
	BalanceVND      int64    `json:"balance_vnd"`
	Currency        string   `json:"currency"`
	MaxStateChars   int      `json:"max_state_chars"`
	CacheTTLSeconds int64    `json:"cache_ttl_seconds"`
	MinChargeVND    int64    `json:"min_charge_vnd"`
	Tiers           []string `json:"tiers"`
}

// Error keeps the upstream response available for 402 handling while exposing
// a stable error kind to the proxy.
type Error struct {
	Kind    error
	Status  int
	Message string
	Body    []byte
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Kind.Error()
}

func (e *Error) Unwrap() error { return e.Kind }

type Client struct {
	baseURL string
	apiKey  string
	retry   int
	http    *http.Client
}

func New(baseURL, apiKey string, timeout time.Duration, retry int) *Client {
	if retry < 0 {
		retry = 0
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		retry:   retry,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *Client) Classify(ctx context.Context, state, host string) (Response, error) {
	if c.apiKey == "" {
		return Response{}, &Error{Kind: ErrUnauthorized, Status: http.StatusUnauthorized, Message: "ACME AI API key is not configured"}
	}
	idempotencyKey, err := newUUID()
	if err != nil {
		return Response{}, err
	}
	body, err := json.Marshal(map[string]string{"state": state, "host": host})
	if err != nil {
		return Response{}, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.retry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		response, err := c.once(ctx, body, idempotencyKey)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !errors.Is(err, ErrTransient) {
			return Response{}, err
		}
	}
	return Response{}, lastErr
}

func (c *Client) once(ctx context.Context, body []byte, idempotencyKey string) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/classify", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req.Header.Set("User-Agent", "SMR/2")

	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, &Error{Kind: ErrTransient, Message: "ACME AI classifier is unreachable", Body: nil}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Response{}, &Error{Kind: ErrTransient, Message: "ACME AI classifier response could not be read"}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, classifyHTTPError(resp.StatusCode, payload)
	}
	var result Response
	if err := json.Unmarshal(payload, &result); err != nil {
		return Response{}, &Error{Kind: ErrTransient, Message: "ACME AI classifier returned invalid JSON"}
	}
	if !validTier(result.Tier) {
		return Response{}, &Error{Kind: ErrTransient, Message: "ACME AI classifier returned an unknown tier"}
	}
	if result.Cache == "" {
		result.Cache = "miss"
	}
	return result, nil
}

func classifyHTTPError(status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	if len(message) > 500 {
		message = message[:500]
	}
	switch status {
	case http.StatusPaymentRequired:
		return &Error{Kind: ErrPaymentRequired, Status: status, Message: "Not enough ACME AI balance. Top up to continue.", Body: body}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Kind: ErrUnauthorized, Status: status, Message: "ACME AI API key is invalid or revoked.", Body: body}
	case http.StatusConflict:
		return &Error{Kind: ErrTransient, Status: status, Message: "ACME AI request is still in progress.", Body: body}
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return &Error{Kind: ErrTransient, Status: status, Message: "ACME AI classifier is temporarily unavailable.", Body: body}
	default:
		return &Error{Kind: ErrPermanent, Status: status, Message: fmt.Sprintf("ACME AI classifier rejected the request (%d).", status), Body: body}
	}
}

func (c *Client) Account(ctx context.Context) (Account, error) {
	if c.apiKey == "" {
		return Account{}, &Error{Kind: ErrUnauthorized, Status: http.StatusUnauthorized, Message: "ACME AI API key is not configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/account", nil)
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "SMR/2")
	resp, err := c.http.Do(req)
	if err != nil {
		return Account{}, &Error{Kind: ErrTransient, Message: "ACME AI is unreachable"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Account{}, &Error{Kind: ErrTransient, Message: "ACME AI response could not be read"}
	}
	if resp.StatusCode != http.StatusOK {
		return Account{}, classifyHTTPError(resp.StatusCode, body)
	}
	var account Account
	if err := json.Unmarshal(body, &account); err != nil {
		return Account{}, &Error{Kind: ErrTransient, Message: "ACME AI returned invalid JSON"}
	}
	return account, nil
}

func validTier(tier string) bool {
	switch tier {
	case "easy", "lower_medium", "upper_medium", "hard":
		return true
	default:
		return false
	}
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

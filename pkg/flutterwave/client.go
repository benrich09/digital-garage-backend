// Package flutterwave is a minimal client for the one Flutterwave
// endpoint this app needs (mobile money charge initiation) plus webhook
// signature verification. Deliberately not a full SDK — fewer
// dependencies, and the two operations here are all the marketplace
// needs (charge a car owner, verify the async result).
package flutterwave

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	secretKey  string
	baseURL    string
	webhookHash string
	http       *http.Client
}

func NewClient(secretKey, baseURL, webhookHash string) *Client {
	return &Client{
		secretKey:   secretKey,
		baseURL:     baseURL,
		webhookHash: webhookHash,
		http:        &http.Client{Timeout: 15 * time.Second},
	}
}

// MobileMoneyChargeRequest maps to Flutterwave's
// POST /charges?type=mobile_money_tanzania body. Network is one of
// "Vodacom" (M-Pesa), "Tigo" (Tigo Pesa), or "Airtel".
type MobileMoneyChargeRequest struct {
	TxRef       string `json:"tx_ref"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	Email       string `json:"email"`
	PhoneNumber string `json:"phone_number"`
	Network     string `json:"network,omitempty"`
	RedirectURL string `json:"redirect_url,omitempty"`
}

type ChargeResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		ID     int64  `json:"id"`
		TxRef  string `json:"tx_ref"`
		Status string `json:"status"`
	} `json:"data"`
	Meta struct {
		Authorization struct {
			RedirectURL string `json:"redirect"`
			Note        string `json:"note"`
		} `json:"authorization"`
	} `json:"meta"`
}

// InitiateMobileMoneyCharge prompts the car owner's phone for a PIN/USSD
// confirmation (Vodacom M-Pesa / Tigo Pesa / Airtel Money) and returns
// immediately with a "pending" status — final success/failure arrives
// later via VerifyWebhookSignature + the webhook payload, not here.
func (c *Client) InitiateMobileMoneyCharge(ctx context.Context, req MobileMoneyChargeRequest) (*ChargeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal charge request: %w", err)
	}

	url := c.baseURL + "/charges?type=mobile_money_tanzania"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.secretKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call flutterwave: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("flutterwave error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var out ChargeResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &out, nil
}

// VerifyWebhookHash checks the "verif-hash" header Flutterwave sends on
// every webhook call against the static secret configured in the
// dashboard. This is NOT an HMAC signature (Flutterwave doesn't sign
// webhook bodies) — it's a shared-secret string comparison, done in
// constant time to avoid leaking the correct value through timing.
func (c *Client) VerifyWebhookHash(headerValue string) bool {
	if c.webhookHash == "" || headerValue == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(headerValue), []byte(c.webhookHash)) == 1
}

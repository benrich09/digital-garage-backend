// Package selcom is a minimal client for Selcom's Checkout/Wallet API
// (https://developers.selcommobile.com), used here to push a mobile
// money charge request for networks Selcom aggregates in Tanzania.
// Selcom signs every request with an HMAC-SHA256 "Digest" header rather
// than a bearer token — see sign() below.
package selcom

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Client struct {
	vendorID  string
	apiKey    string
	apiSecret string
	baseURL   string // e.g. https://apigwtest.selcommobile.com (sandbox) / https://apigw.selcommobile.com (prod)
	http      *http.Client
}

func NewClient(vendorID, apiKey, apiSecret, baseURL string) *Client {
	return &Client{
		vendorID:  vendorID,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		baseURL:   strings.TrimRight(baseURL, "/"),
		http:      &http.Client{Timeout: 20 * time.Second},
	}
}

// sign computes Selcom's "Digest" header: an HMAC-SHA256 (base64) over
// "key1=value1&key2=value2..." with fields sorted alphabetically by key,
// keyed with the API secret. Selcom also expects the request timestamp
// in the same string.
func (c *Client) sign(fields map[string]string) (digest, timestamp string) {
	timestamp = time.Now().UTC().Format("2006-01-02T15:04:05-0700")
	fields["timestamp"] = timestamp

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, fields[k]))
	}
	raw := strings.Join(pairs, "&")

	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write([]byte(raw))
	digest = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return digest, timestamp
}

func (c *Client) authHeaders(fields map[string]string) map[string]string {
	digest, timestamp := c.sign(fields)
	return map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "SELCOM " + base64.StdEncoding.EncodeToString([]byte(c.apiKey)),
		"Digest-Method": "HS256",
		"Digest":        digest,
		"Timestamp":     timestamp,
		"SignedFields":  strings.Join(sortedKeys(fields), ","),
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "timestamp" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return append(keys, "timestamp")
}

// WalletChargeRequest maps to Selcom's minimal-order / wallet push-USSD
// request, used to prompt a mobile money PIN on the customer's phone.
type WalletChargeRequest struct {
	OrderID     string // our own idempotency key, mirrors payments.provider_tx_ref
	Amount      string
	Currency    string
	PhoneNumber string // MSISDN, e.g. 2557XXXXXXXX
	BuyerEmail  string
	BuyerName   string
}

type WalletChargeResponse struct {
	Result     string `json:"result"` // "SUCCESS" | "FAIL"
	ResultCode string `json:"resultcode"`
	Message    string `json:"message"`
	Data       []struct {
		OrderID      string `json:"order_id"`
		Reference    string `json:"reference"`
		PaymentToken string `json:"payment_token"`
	} `json:"data"`
}

// InitiateWalletCharge starts a push-to-phone mobile money charge.
// Returns immediately; final status arrives via the configured webhook.
func (c *Client) InitiateWalletCharge(ctx context.Context, req WalletChargeRequest) (*WalletChargeResponse, error) {
	body := map[string]any{
		"vendor":      c.vendorID,
		"order_id":    req.OrderID,
		"buyer_email": req.BuyerEmail,
		"buyer_name":  req.BuyerName,
		"buyer_phone": req.PhoneNumber,
		"amount":      req.Amount,
		"currency":    req.Currency,
		"no_of_items": 1,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal charge request: %w", err)
	}

	signFields := map[string]string{
		"vendor":   c.vendorID,
		"order_id": req.OrderID,
	}
	headers := c.authHeaders(signFields)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/checkout/create-order-minimal", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call selcom: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("selcom error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var out WalletChargeResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &out, nil
}

// WebhookPayload covers the fields this app reads out of Selcom's
// payment-status webhook callback.
type WebhookPayload struct {
	OrderID       string `json:"order_id"`
	PaymentStatus string `json:"payment_status"` // "COMPLETED" | "PENDING" | "FAILED"
	Reference     string `json:"reference"`
	TransID       string `json:"transid"`
}

// VerifyWebhookSignature checks the "Digest" header Selcom sends on the
// webhook callback against a recomputed HMAC over the raw body, using
// constant-time comparison.
func (c *Client) VerifyWebhookSignature(digestHeader string, rawBody []byte) bool {
	if digestHeader == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write(rawBody)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(digestHeader)) == 1
}

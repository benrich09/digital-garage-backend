// Package mpesa is a minimal client for Safaricom/Vodacom's M-Pesa Daraja
// API — specifically Lipa Na M-Pesa Online (STK Push), which is all this
// app needs: prompt the car owner's phone for a PIN, then wait for the
// async callback. Deliberately not a full SDK.
package mpesa

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client talks to the Daraja API. Consumer key/secret authenticate the
// app itself (OAuth client-credentials grant); Shortcode + Passkey
// authorize the specific till/paybill the STK push is charged against.
type Client struct {
	consumerKey    string
	consumerSecret string
	shortcode      string
	passkey        string
	baseURL        string // sandbox: https://sandbox.safaricom.co.ke, prod: https://api.safaricom.co.ke
	callbackURL    string
	http           *http.Client

	mu          sync.Mutex
	cachedToken string
	tokenExp    time.Time
}

func NewClient(consumerKey, consumerSecret, shortcode, passkey, baseURL, callbackURL string) *Client {
	return &Client{
		consumerKey:    consumerKey,
		consumerSecret: consumerSecret,
		shortcode:      shortcode,
		passkey:        passkey,
		baseURL:        strings.TrimRight(baseURL, "/"),
		callbackURL:    callbackURL,
		http:           &http.Client{Timeout: 20 * time.Second},
	}
}

type oauthResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
}

// token returns a cached OAuth access token, refreshing it shortly before
// expiry. Daraja tokens are typically valid for ~1 hour.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedToken != "" && time.Now().Before(c.tokenExp) {
		return c.cachedToken, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/oauth/v1/generate?grant_type=client_credentials", nil)
	if err != nil {
		return "", fmt.Errorf("build oauth request: %w", err)
	}
	req.SetBasicAuth(c.consumerKey, c.consumerSecret)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call daraja oauth: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read oauth response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("daraja oauth error (status %d): %s", resp.StatusCode, string(body))
	}

	var out oauthResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse oauth response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("daraja oauth: empty access token")
	}

	c.cachedToken = out.AccessToken
	// Refresh 2 minutes early to avoid racing expiry mid-request.
	c.tokenExp = time.Now().Add(55 * time.Minute)
	return c.cachedToken, nil
}

// STKPushRequest maps to POST /mpesa/stkpush/v1/processrequest.
// PhoneNumber must be in MSISDN format (2547XXXXXXXX / 2557XXXXXXXX,
// no leading +/0).
type STKPushRequest struct {
	PhoneNumber     string
	Amount          string // whole-number string, e.g. "1500"
	AccountRef      string // shows on the customer's STK prompt, e.g. our tx_ref
	TransactionDesc string
}

type STKPushResponse struct {
	MerchantRequestID   string `json:"MerchantRequestID"`
	CheckoutRequestID   string `json:"CheckoutRequestID"`
	ResponseCode        string `json:"ResponseCode"`
	ResponseDescription string `json:"ResponseDescription"`
	CustomerMessage     string `json:"CustomerMessage"`
}

// InitiateSTKPush triggers the PIN prompt on the customer's phone and
// returns immediately with a CheckoutRequestID — final success/failure
// arrives later via the callback URL configured on this client.
func (c *Client) InitiateSTKPush(ctx context.Context, req STKPushRequest) (*STKPushResponse, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	timestamp := time.Now().Format("20060102150405")
	password := base64.StdEncoding.EncodeToString([]byte(c.shortcode + c.passkey + timestamp))

	body := map[string]any{
		"BusinessShortCode": c.shortcode,
		"Password":          password,
		"Timestamp":         timestamp,
		"TransactionType":   "CustomerPayBillOnline",
		"Amount":            req.Amount,
		"PartyA":            req.PhoneNumber,
		"PartyB":            c.shortcode,
		"PhoneNumber":       req.PhoneNumber,
		"CallBackURL":       c.callbackURL,
		"AccountReference":  req.AccountRef,
		"TransactionDesc":   req.TransactionDesc,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal stk push request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/mpesa/stkpush/v1/processrequest", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build stk push request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call daraja stk push: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read stk push response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("daraja stk push error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var out STKPushResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse stk push response: %w", err)
	}
	return &out, nil
}

// CallbackPayload mirrors the body Daraja POSTs to CallBackURL once the
// customer accepts/rejects/times out on the STK prompt.
type CallbackPayload struct {
	Body struct {
		StkCallback struct {
			MerchantRequestID string `json:"MerchantRequestID"`
			CheckoutRequestID string `json:"CheckoutRequestID"`
			ResultCode        int    `json:"ResultCode"`
			ResultDesc        string `json:"ResultDesc"`
			CallbackMetadata  struct {
				Item []struct {
					Name  string `json:"Name"`
					Value any    `json:"Value"`
				} `json:"Item"`
			} `json:"CallbackMetadata"`
		} `json:"stkCallback"`
	} `json:"Body"`
}

// ReceiptNumber extracts the MpesaReceiptNumber from a successful
// callback's metadata, or "" if not present (e.g. on failure).
func (p CallbackPayload) ReceiptNumber() string {
	for _, item := range p.Body.StkCallback.CallbackMetadata.Item {
		if item.Name == "MpesaReceiptNumber" {
			if s, ok := item.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// VerifyCallbackSecret checks a shared secret this app appends as a query
// param on its own CallBackURL (Daraja itself doesn't sign callbacks), so
// we can reject spoofed POSTs to the webhook route before trusting them.
func VerifyCallbackSecret(configured, provided string) bool {
	if configured == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(provided)) == 1
}

// Package fcm sends push notifications via the Firebase Cloud Messaging
// HTTP v1 API. The v1 API (unlike the legacy server-key API) requires an
// OAuth2 access token minted from a Firebase service account — this
// client handles that token exchange (and automatic refresh) via
// golang.org/x/oauth2/google, so callers just call Send().
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const scope = "https://www.googleapis.com/auth/firebase.messaging"

type Client struct {
	projectID  string
	tokenSrc   oauth2.TokenSource
	httpClient *http.Client
}

// NewClientFromServiceAccountFile reads the Firebase service account
// JSON (Firebase console -> Project Settings -> Service Accounts ->
// Generate new private key) from disk and prepares an authenticated
// client. Keep this file OUT of version control — mount it as a secret
// file (or an env-var-decoded temp file) in production, never commit it.
func NewClientFromServiceAccountFile(ctx context.Context, projectID, path string) (*Client, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fcm: read service account file: %w", err)
	}

	creds, err := google.CredentialsFromJSON(ctx, data, scope)
	if err != nil {
		return nil, fmt.Errorf("fcm: parse service account: %w", err)
	}

	return &Client{
		projectID:  projectID,
		tokenSrc:   oauth2.ReuseTokenSource(nil, creds.TokenSource),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Message is a minimal FCM v1 message: a visible notification (title +
// body, rendered by the OS when the app is backgrounded/terminated) plus
// a data payload the app reads to know where to navigate on tap.
type Message struct {
	Token string
	Title string
	Body  string
	Data  map[string]string
}

type fcmRequestBody struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data,omitempty"`
	Android      fcmAndroidConfig  `json:"android"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type fcmAndroidConfig struct {
	Priority string `json:"priority"` // "high" so it wakes a backgrounded/terminated app promptly
}

// Send delivers one message to one device token. Callers loop over a
// user's registered tokens (internal/services/push_service.go) rather
// than this client fanning out itself, so a bad/expired token for one
// device never blocks delivery to the user's other devices.
func (c *Client) Send(ctx context.Context, msg Message) error {
	token, err := c.tokenSrc.Token()
	if err != nil {
		return fmt.Errorf("fcm: get access token: %w", err)
	}

	body := fcmRequestBody{Message: fcmMessage{
		Token:        msg.Token,
		Notification: fcmNotification{Title: msg.Title, Body: msg.Body},
		Data:         msg.Data,
		Android:      fcmAndroidConfig{Priority: "high"},
	}}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("fcm: marshal message: %w", err)
	}

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", c.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("fcm: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fcm: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fcm: send failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

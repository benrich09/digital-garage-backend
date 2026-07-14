package services

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/pkg/fcm"
)

// PushService is the single place every other service calls to notify a
// user by push — it owns "look up this user's device tokens, send to
// each, and clean up tokens FCM reports as dead". Kept separate from
// ws.Manager on purpose: push notifications reach a user even when the
// app is fully closed (that's the whole point), which a WebSocket
// connection fundamentally cannot do.
type PushService struct {
	tokens repository.DeviceTokenRepository
	fcm    *fcm.Client
	log    zerolog.Logger
}

func NewPushService(tokens repository.DeviceTokenRepository, fcmClient *fcm.Client, log zerolog.Logger) *PushService {
	return &PushService{tokens: tokens, fcm: fcmClient, log: log}
}

// Notify sends the same title/body/data to every device registered for
// userID. Best-effort: a failure to reach one device never blocks the
// others, and this never returns an error to its caller — a push
// failing should never fail the underlying business operation (e.g. a
// service request should still get created even if the push to nearby
// garages fails to send).
func (s *PushService) Notify(ctx context.Context, userID uuid.UUID, title, body string, data map[string]string) {
	if s.fcm == nil {
		return // push not configured (e.g. local dev without a service account) — no-op
	}

	tokens, err := s.tokens.ListForUser(ctx, userID)
	if err != nil {
		s.log.Warn().Err(err).Str("user_id", userID.String()).Msg("could not load device tokens for push")
		return
	}

	for _, token := range tokens {
		err := s.fcm.Send(ctx, fcm.Message{Token: token, Title: title, Body: body, Data: data})
		if err != nil {
			s.log.Warn().Err(err).Str("user_id", userID.String()).Msg("push send failed")
			// FCM returns 404/NotRegistered for tokens that are dead
			// (app uninstalled, token rotated without us hearing about
			// it yet) — prune them so we stop paying the retry cost.
			if strings.Contains(err.Error(), "UNREGISTERED") || strings.Contains(err.Error(), "404") {
				_ = s.tokens.Unregister(ctx, token)
			}
		}
	}
}

func (s *PushService) RegisterToken(ctx context.Context, userID uuid.UUID, token, platform string) error {
	return s.tokens.Register(ctx, userID, token, platform)
}

func (s *PushService) UnregisterToken(ctx context.Context, token string) error {
	return s.tokens.Unregister(ctx, token)
}

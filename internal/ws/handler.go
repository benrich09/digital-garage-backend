package ws

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
)

// NOTE ON LIBRARY CHOICE: the ask was for gorilla/websocket specifically.
// gorilla/websocket upgrades a connection via net/http's Hijacker
// interface, which fasthttp (what Fiber runs on) does not implement —
// there's no clean way to hijack a fasthttp connection into a raw TCP
// socket the way gorilla needs. github.com/gofiber/websocket/v2 wraps
// github.com/fasthttp/websocket, which is a fork of gorilla/websocket
// with the same Conn/ReadMessage/WriteJSON API, adapted to work natively
// inside Fiber's router on the same port. Everything below reads and
// behaves like gorilla/websocket usage; only the import path differs.
// If you'd rather run literal gorilla/websocket, the alternative is a
// second, small net/http server bound to its own port just for /ws —
// happy to scaffold that instead if the single-port constraint isn't
// important for your deployment.

// UpgradeCheck must run before the websocket.New handler on the same
// route: it validates the JWT from the query string and stashes the
// user id in fiber.Locals so the handler below can read it after the
// protocol upgrade (headers are gone at that point, only Locals persist).
func UpgradeCheck(jwtSecret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}

		token := c.Query("token")
		if token == "" {
			// also accept Authorization header for non-browser clients
			// (Flutter's ws client can set headers on native platforms)
			auth := c.Get("Authorization")
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing token")
		}

		claims := jwt.MapClaims{}
		parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return []byte(jwtSecret), nil
		})
		if err != nil || !parsed.Valid {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid or expired token")
		}

		sub, ok := claims["sub"].(string)
		if !ok || sub == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "token missing subject")
		}

		c.Locals("ws_user_id", sub)
		return c.Next()
	}
}

// Handler is the actual upgraded-connection loop. Register with:
//   app.Get("/ws", ws.UpgradeCheck(secret), websocket.New(ws.Handler(manager, log)))
func Handler(mgr *Manager, log zerolog.Logger) func(*websocket.Conn) {
	return func(c *websocket.Conn) {
		userID, _ := c.Locals("ws_user_id").(string)
		if userID == "" {
			_ = c.Close()
			return
		}

		conn := mgr.Register(userID, c)
		defer func() {
			mgr.Unregister(userID, conn)
			_ = c.Close()
		}()

		// The read loop's only real job is detecting disconnects (and
		// answering client pings/keepalives if the client sends any
		// application-level ones). We don't expect clients to send
		// commands over this socket — all writes happen server ->
		// client; the REST API is still how clients mutate state.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				log.Debug().Str("user_id", userID).Err(err).Msg("ws read loop ended")
				return
			}
		}
	}
}

// Package logger configures zerolog, chosen specifically because it does
// zero-allocation structured logging by default — cheap on both CPU and
// memory compared to reflection-based loggers, which matters on a 1 vCPU
// box handling concurrent requests.
package logger

import (
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// New builds the global logger. In development it uses zerolog's pretty
// console writer; in production it emits plain JSON lines (cheaper to
// produce and directly ingestible by any log collector), to stdout so
// the container runtime handles rotation/shipping.
func New(env, level string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)

	var l zerolog.Logger
	if env == "development" {
		l = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
			With().Timestamp().Logger()
	} else {
		l = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
	return l
}

// FiberMiddleware logs one structured line per request (method, path,
// status, latency, request id). Panic recovery is handled separately by
// fiber's own recover middleware (see router.go), so this stays focused
// on logging only.
func FiberMiddleware(log zerolog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		reqID := uuid.NewString()
		c.Locals("request_id", reqID)
		c.Set("X-Request-ID", reqID)

		err := c.Next()

		log.Info().
			Str("request_id", reqID).
			Str("method", c.Method()).
			Str("path", c.Path()).
			Int("status", c.Response().StatusCode()).
			Dur("latency", time.Since(start)).
			Str("client_ip", c.IP()).
			Msg("request handled")

		return err
	}
}

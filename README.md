# Digital Garage — Go API

A Fiber backend for the garage marketplace, talking directly to Supabase
Postgres via pgx + sqlc. Designed to run comfortably on a 1 vCPU / 1GB VM.

## Getting started

```bash
cp .env.example .env   # fill in DATABASE_URL, SUPABASE_JWT_SECRET, etc.
go mod tidy             # populates go.sum and indirect requires
make run
curl localhost:8080/healthz
```

## Folder structure

```
cmd/api/main.go            entrypoint: wires everything, starts the server
internal/config             viper env-var loading
internal/logger              zerolog setup + fiber request-logging middleware
internal/middleware           JWT auth middleware
internal/db                  pgxpool setup
internal/db/queries          hand-written SQL, source of truth for sqlc
internal/db/sqlcgen          sqlc-generated typed Go (regenerate with `sqlc generate`)
internal/repository           wraps sqlcgen queries -> domain models
internal/services             business logic (state-machine transitions, search)
internal/handlers             Fiber HTTP handlers + router
internal/models               plain domain structs used by services/handlers
pkg/geo                      reusable, dependency-free geo helpers
pkg/apierr                   shared JSON error response shape
supabase/migrations           schema (source of truth for the DB itself)
```

`internal/` vs `pkg/`: everything specific to this service's domain lives
under `internal` so the Go compiler enforces it can't be imported by some
other module; the couple of genuinely generic helpers (geo math, error
shape) live in `pkg/` in case you ever split out a second service.

## Why this keeps resource usage low

- **pgx directly, no ORM.** No reflection-heavy struct mapping, no lazy
  loading, no N+1 query surprises to accidentally introduce. sqlc turns
  hand-written SQL into typed Go at compile time, so there's zero
  runtime query-building cost.
- **Small pgxpool (`DB_MAX_CONNS=4`).** Each Postgres connection costs
  memory on both ends. A Gin API on one small VM doesn't need dozens of
  connections open — a handful comfortably covers realistic concurrency
  for this workload, and it keeps the client-side pool's own buffers
  small too.
- **zerolog** does structured logging with (near) zero allocations per
  line, versus reflection-based loggers — cheap on both CPU and GC
  pressure under load.
- **Fiber (fasthttp), not a net/http-based framework.** fasthttp
  aggressively reuses connection and buffer objects instead of
  allocating fresh ones per request the way net/http does — fewer
  allocations per request means less GC pressure, which matters more on
  a single vCPU where GC pauses compete directly with request handling.
  Fiber's own router/middleware chain is minimal (no bundled ORM,
  template engine, or session store you're not using), same as the
  earlier Gin-based design, just with a lighter HTTP layer underneath.
- **PostGIS math done in SQL, not Go.** `ST_DWithin`/`ST_Distance` run
  inside Postgres, using the GIST index — the Go process never has to
  pull every garage into memory and compute distances itself.
- **Distroless, statically-linked Docker image.** `CGO_ENABLED=0` means
  no libc, so the final image is `FROM gcr.io/distroless/static` — a few
  MB, no shell, no package manager, nothing idling in memory besides the
  binary itself. Multi-stage build means the Go toolchain (~300MB) never
  ships in the runtime image.
- **One binary, one process.** No background worker pool, no separate
  migration runner, no sidecar — just an HTTP server and a DB pool. If
  you later add async work (e.g. notification delivery), consider a
  cheap in-process queue before reaching for a separate service; the
  1GB budget doesn't have room for a second long-running process.
- **Graceful shutdown with a bounded timeout** avoids the VM's process
  supervisor (systemd/Docker) having to hard-kill the process on
  deploys, which would otherwise risk leaving Postgres connections in a
  bad state.

## Payments (mobile money via Flutterwave) & Reviews

- `POST /payments/initiate` (car_owner) — starts a Vodacom M-Pesa / Tigo
  Pesa / Airtel Money charge for a **completed** booking via Flutterwave's
  Tanzania mobile money charge endpoint. Returns immediately with
  `status: pending`; the car owner approves via a PIN/USSD prompt on
  their phone.
- `POST /webhooks/flutterwave` (public, signature-verified) — Flutterwave
  calls this when the charge settles. Verifies the `verif-hash` header
  against `FLUTTERWAVE_WEBHOOK_HASH` (a static shared secret you set in
  the Flutterwave dashboard — not an HMAC signature) using a
  constant-time comparison, then marks the payment `paid`/`failed` and
  flips the service request to `paid` on success, notifying the car
  owner over WebSocket.
- `POST /reviews` (car_owner) — rates a garage or mechanic. Rejected if:
  the caller isn't the request's car owner, the request hasn't reached
  `completed`/`paid`/`closed` yet, or a review for that booking/target
  already exists (checked in Go and backed by a DB unique constraint).

Configure `FLUTTERWAVE_SECRET_KEY`, `FLUTTERWAVE_WEBHOOK_HASH` in `.env`
before testing payments — see `.env.example`.

## Important note on RLS vs. this Go backend

Earlier notes described Postgres RLS as "the real security boundary."
That's true for anything calling Supabase directly with the anon/
authenticated key (e.g. Storage uploads, or if you ever add direct
Supabase client reads from the Flutter apps) — but **this Go backend's
own DB connection uses the full `postgres` role from `DATABASE_URL`,
which bypasses RLS entirely** (Supabase's `postgres` user has
`BYPASSRLS`). That means every ownership/role check enforced by this
API (in `internal/middleware` and `internal/services`) is doing 100% of
the authorization work for anything that goes through this API — RLS is
a second line of defense only for traffic that hits Supabase directly,
not for this backend's own queries. Worth keeping in mind if you ever
add a new route: there's no RLS safety net catching a missing ownership
check here the way there would be for a direct-to-Supabase client call.



The files in `internal/db/sqlcgen/` are checked in so the project builds
without the sqlc CLI installed, but they're meant to be regenerated:

```bash
# install once: https://docs.sqlc.dev/en/latest/overview/install.html
make sqlc-generate
```

Run this after editing anything in `internal/db/queries/`.

## Security model

RLS in Postgres (see `supabase/migrations/0002_rls_policies.sql`) is the
real enforcement boundary — the Go layer's ownership checks are a fast
first line of defense and a better UX (clean 4xx vs. a bare Postgres
permission error), not the only line. Even if a service-layer check has
a bug, RLS still blocks a query from ever returning rows outside a
user's own data, so the two layers work as defense in depth rather than
duplicated effort.
# digital-garage-backend

# Digital Garage Backend — Architecture

## Important clarification first: this is a modular monolith, not microservices

You asked for "a list of microservices" — being straight with you: **this backend is
one single deployable Go binary**, not a set of independently-deployed
microservices. There's no service mesh, no inter-service network calls, no
separate databases per domain. Everything in the file list below runs in one
process, talking to one Postgres database.

What it *does* have is clean **internal layering** (handlers → services →
repositories) with each business domain (garages, offers, bookings, payments,
etc.) kept in its own files within that layering — which is why it can *look*
service-like when you scan the folder names. If you specifically want real
microservices (independently deployable, independently scalable, with their
own datastores), that's a genuine architectural rewrite, not a relabeling —
happy to scope that separately if it's actually what you need, but I'd want
to understand why first (most apps at this scale don't benefit from the
operational complexity microservices add, and a modular monolith like this
one can usually be split later, once you know where the actual scaling
pressure is).

## Request flow (how a request actually moves through the code)

```
HTTP request
  → internal/handlers/router.go        (routes + middleware wiring)
  → internal/middleware/auth.go        (verifies Supabase JWT, loads profile, checks is_active)
  → internal/middleware/rbac.go        (checks the caller's role is allowed for this route)
  → internal/handlers/*_handler.go     (parses request, calls the service, shapes the response)
  → internal/services/*_service.go     (business rules, orchestration, WebSocket notifications)
  → internal/repository/*_repository.go (the only layer that writes SQL)
  → internal/db/sqlcgen/*.sql.go       (generated, type-safe query functions — see sqlc.dev)
  → Postgres (via Supabase)
```

Each domain (garages, offers, bookings, etc.) follows this same four-layer
path — that consistency is deliberate, so once you understand one domain,
you understand the pattern for all of them.

## File-by-file structure

### `cmd/api/` — entrypoint
| File | Purpose |
|---|---|
| `main.go` | Wires every dependency together (DB pool, repositories, services, handlers) and starts the Fiber HTTP server. The only file that imports "everything" — everywhere else stays decoupled. |

### `internal/config/` — configuration
| File | Purpose |
|---|---|
| `config.go` | Loads and validates all environment variables (`DATABASE_URL`, Supabase keys, M-Pesa/Selcom credentials, CORS origins, etc.) into one typed struct via Viper. |

### `internal/handlers/` — HTTP layer (parses requests, shapes responses)
| File | Purpose |
|---|---|
| `router.go` | Registers every route, applies CORS + auth + RBAC middleware, wires the WebSocket upgrade endpoint. |
| `admin_handler.go` | Garage approval/rejection (admin/superadmin only). |
| `booking_handler.go` | Booking status transitions (job started/completed). |
| `garage_handler.go` | Garage CRUD, nearby-garage search, verification submission. |
| `health_handler.go` | `GET /healthz` — pings the DB pool, used by deploy platforms' health checks. |
| `mechanic_handler.go` | Mechanic-specific profile/assignment endpoints. |
| `offer_handler.go` | A garage/mechanic sending an offer on a service request; a car owner accepting one. |
| `payment_handler.go` | Initiates M-Pesa/Selcom charges; receives their async webhook callbacks. |
| `review_handler.go` | Post-job reviews (car owner rates the garage/mechanic). |
| `service_request_handler.go` | Car owner creates a service request; nearby-garage matching kicks off here. |

### `internal/services/` — business logic (the only layer with real "rules")
| File | Purpose |
|---|---|
| `booking_service.go` | Validates legal status transitions, notifies the car owner over WebSocket on change. |
| `garage_service.go` | Garage profile + verification workflow logic. |
| `mechanic_service.go` | Mechanic assignment logic. |
| `offer_service.go` | Offer creation/acceptance, booking creation on acceptance. |
| `payment_service.go` | Talks to `pkg/mpesa`/`pkg/selcom`, reconciles their webhook callbacks against pending payments, flips a request to "paid". |
| `review_service.go` | Enforces one review per booking, only after job completion. |
| `service_request_service.go` | Geo-matches a new request to nearby garages (via `pkg/geo`), notifies them over WebSocket. |

### `internal/repository/` — the only layer allowed to write SQL
| File | Purpose |
|---|---|
| `booking_repository.go`, `garage_repository.go`, `mechanic_repository.go`, `offer_repository.go`, `payment_repository.go`, `profile_repository.go`, `review_repository.go`, `service_request_repository.go` | Each wraps the matching `sqlcgen` generated queries behind a small Go interface, so services never touch SQL or the `pgx` driver directly. |

### `internal/db/` — database plumbing
| File | Purpose |
|---|---|
| `pool.go` | Creates the `pgx` connection pool from `DATABASE_URL`. |
| `sqlcgen/*.sql.go` | **Generated code** (via [sqlc](https://sqlc.dev)) from the `.sql` files in `internal/db/queries/` — type-safe Go functions for every query, regenerated with `make sqlc-generate`, never hand-edited. |

### `internal/middleware/` — cross-cutting request checks
| File | Purpose |
|---|---|
| `auth.go` | Verifies the Supabase-issued JWT, loads the caller's profile, rejects suspended (`is_active = false`) accounts on every request, not just at login. |
| `rbac.go` | `RequireRole(...)` — gates a route to specific roles (e.g. only `garage_owner`/`mechanic` can access field-work routes). |

### `internal/models/` — shared types
| File | Purpose |
|---|---|
| `auth.go` | `AuthUser`, role constants (`RoleCarOwner`, `RoleGarageOwner`, `RoleMechanic`, `RoleAdmin`, `RoleSuperAdmin`). |
| `models.go` | Request/response DTOs shared across handlers and services. |

### `internal/ws/` — real-time layer
| File | Purpose |
|---|---|
| `manager.go` | Tracks which user IDs have an open WebSocket connection; `SendToUser(...)` is how every service pushes a live update. |
| `handler.go` | Upgrades an authenticated HTTP request to a WebSocket connection. |
| `events.go` | Typed event payloads (`new_request_match`, `request_accepted`, `status_update`, etc.) so the Flutter apps can deserialize them without guessing shapes. |

### `internal/logger/`
| File | Purpose |
|---|---|
| `logger.go` | Structured JSON logging setup (zerolog) + the Fiber request-logging middleware. |

### `pkg/` — dependency-free helpers (importable outside this project, unlike `internal/`)
| File | Purpose |
|---|---|
| `apierr/apierr.go` | Consistent JSON error-response shape used by every handler. |
| `geo/geo.go` | Haversine distance calculation for "nearby garages" matching. |
| `mpesa/client.go` | Safaricom/Vodacom Daraja STK Push client (OAuth token caching, push initiation, callback payload parsing). |
| `selcom/client.go` | Selcom wallet-charge client (HMAC-signed requests per their spec). |

### `supabase/migrations/` — database schema history (11 migrations)
Chronological, immutable once applied — `0001_init_schema.sql` (core tables +
the `handle_new_user` trigger that creates a `profiles` row on signup)
through `0011_drop_device_tokens.sql` (cleanup after removing Firebase).
Applied via `supabase db push`, never hand-edited after merging.

### `scripts/`
| File | Purpose |
|---|---|
| `seed_user.sh` | Creates a real Supabase Auth user with any role via the Admin API — used for seeding test/demo accounts (superadmin, garage_owner, etc.). |

## Technology stack

| Layer | Technology | Why |
|---|---|---|
| Language | Go 1.22 | Static typing, fast compile/deploy, single static binary |
| HTTP framework | [Fiber v2](https://gofiber.io) | Express-style routing, low overhead, built-in WebSocket support via `gofiber/websocket` |
| Database | PostgreSQL (via [Supabase](https://supabase.com)) | Managed Postgres + built-in Auth + Row-Level Security as a real second enforcement layer, not just app-level checks |
| DB access | [pgx/v5](https://github.com/jackc/pgx) + [sqlc](https://sqlc.dev) | `pgx` is the driver; `sqlc` generates type-safe Go from hand-written SQL, so there's no ORM magic and no runtime query building |
| Auth | Supabase Auth (JWT) | Email/password + Google OAuth on the client side; this backend only verifies the JWT and checks role/`is_active`, never touches passwords |
| Config | [spf13/viper](https://github.com/spf13/viper) | Typed env-var loading with defaults |
| Logging | [rs/zerolog](https://github.com/rs/zerolog) | Structured JSON logs, low allocation overhead |
| Real-time | Native WebSocket (`gofiber/websocket`) | Live status updates to both Flutter apps; no third-party pub/sub needed at this scale |
| Payments | M-Pesa (Safaricom/Vodacom Daraja) + Selcom | Hand-written clients (no SDK exists for either in Go) — see `pkg/mpesa`, `pkg/selcom` |
| API docs | [gofiber/swagger](https://github.com/gofiber/swagger) + swaggo annotations | Auto-generated OpenAPI docs from handler comments, served at `/swagger` |
| Deployment | Docker (distroless static image) | Runs identically on any host that can run a container — bare VPS, Railway, Render, Fly.io |

## If you actually want microservices later

The layering already in place (handlers/services/repositories per domain)
is exactly what makes a *future* split practical, if it's ever warranted:
`payments`, `garages+offers+bookings`, and `reviews` are the three domains
with the cleanest boundaries and the least cross-talk today. But splitting
now — before there's a real scaling or team-ownership reason to — would
trade simplicity for operational overhead (separate deploys, separate DBs
or careful schema ownership, network calls where there are currently just
function calls) with no corresponding benefit yet. Worth revisiting if/when
you have a concrete reason (a specific domain needs independent scaling, or
a separate team needs to own and deploy it independently).

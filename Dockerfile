# --- Stage 1: build -----------------------------------------------------
# golang:1.22-alpine only exists in the build stage; it never ships in
# the final image, so its ~300MB of toolchain doesn't cost the VM
# anything at runtime.
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Cache dependency downloads separately from source changes so
# `docker build` doesn't re-fetch modules on every code edit.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a fully static binary — no libc dependency, so
# the final stage can be `scratch` instead of a full Linux distro. This
# is the single biggest lever for image size and cold-start memory: no
# shared libraries to page in, nothing else running in the container.
# -ldflags strips debug symbols (-s -w), shaving several more MB off.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/api ./cmd/api

# --- Stage 2: run ---------------------------------------------------------
# distroless/static instead of bare `scratch` so we still get CA
# certificates (required for TLS to Supabase) and tzdata, without
# pulling in a shell, package manager, or any other attack surface —
# keeps both the image size (a few MB) and idle memory footprint low.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/api /api

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/api"]

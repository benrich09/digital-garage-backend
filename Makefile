.PHONY: run build docker-build docker-run sqlc-generate tidy

run:
	go run ./cmd/api

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/api ./cmd/api

docker-build:
	docker build -t digital-garage-api:latest .

docker-run:
	docker run --rm -p 8080:8080 --env-file .env digital-garage-api:latest

# Requires the sqlc CLI: https://docs.sqlc.dev/en/latest/overview/install.html
sqlc-generate:
	sqlc generate

tidy:
	go mod tidy

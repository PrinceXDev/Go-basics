.PHONY: up down test test-short bench load fmt web

up:        ## start the whole stack
	docker compose up -d --build

down:
	docker compose down

test-short: ## unit tests only (no Postgres download)
	go test -short ./...

test:      ## unit + Postgres integration tests (downloads an embedded Postgres once)
	go test ./...

bench:     ## database write-path benchmark
	go test ./internal/store -run XXX -bench ApplyBatch -benchtime 60x

load:      ## 30s at 1000 req/s against the local API
	go run ./cmd/loadgen -rps 1000 -duration 30s

fmt:
	gofmt -w .

web:
	cd web && npm install && npm run dev

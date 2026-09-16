# Capstone Makefile. Run from the REPOSITORY ROOT:
#   make -f 47_capstone/Makefile <target>

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X 'main.version=$(VERSION)' \
                    -X 'main.commit=$(COMMIT)' \
                    -X 'main.buildDate=$(BUILD_DATE)'
PKG        := ./47_capstone/...

.PHONY: help run build build-linux test race cover bench lint fmt docker clean

help:
	@echo "run          start the API on :8080"
	@echo "build        build the binary into bin/"
	@echo "build-linux  static linux/amd64 binary"
	@echo "test         go test"
	@echo "race         go test -race"
	@echo "cover        coverage report -> coverage.html"
	@echo "lint         gofmt + go vet + golangci-lint"
	@echo "docker       build the container image"
	@echo "ci           lint + race + docker"

run:
	go run ./47_capstone/cmd/api

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/api ./47_capstone/cmd/api

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/api-linux ./47_capstone/cmd/api

test:
	go test -count=1 $(PKG)

race:
	go test -race -count=1 $(PKG)

cover:
	go test -count=1 -coverprofile=coverage.out -covermode=atomic $(PKG)
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out | tail -1

bench:
	go test -bench=. -benchmem $(PKG)

fmt:
	gofmt -w 47_capstone/

lint:
	@test -z "$$(gofmt -l 47_capstone/)" || { gofmt -l 47_capstone/; echo "run: make fmt"; exit 1; }
	go vet $(PKG)
	golangci-lint run $(PKG)

docker:
	docker build -f 47_capstone/Dockerfile \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t tasks-api:$(VERSION) -t tasks-api:latest .

ci: lint race docker

clean:
	rm -rf bin coverage.out coverage.html

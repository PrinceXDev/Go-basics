# 46 — Docker & Deployment

Files in this lesson:

| file | what it is |
|---|---|
| `main.go` | the service: healthz/readyz/version, ldflags build info, graceful shutdown |
| `Dockerfile` | a production multi-stage build, heavily commented |
| `docker-compose.yml` | local dev: the app + Postgres, with a healthcheck dependency |
| `../.dockerignore` | at the **repo root**, because that's the build context |

---

## Quick start

```bash
go run ./46_docker
```

```bash
curl -s localhost:8080/version | jq
```

Build the image **from the repository root** (the context needs `go.mod`):

```bash
docker build -f 46_docker/Dockerfile -t go-basics:dev .
```

```bash
docker run --rm -p 8080:8080 -e PORT=8080 go-basics:dev
```

With real build metadata injected:

```bash
docker build -f 46_docker/Dockerfile \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t go-basics:$(git rev-parse --short HEAD) .
```

---

## Why Go containers are small

The same `go build` used in the Dockerfile, run locally:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w" -o /tmp/app ./46_docker
```

```
ELF 64-bit LSB executable, x86-64, statically linked, stripped   5.9 MB
```

Statically linked, no libc, no runtime to install. Add the ~2 MB distroless
base and the shipped image is **under 10 MB**. That's the whole reason Go is
so pleasant to deploy.

| flag | effect |
|---|---|
| `CGO_ENABLED=0` | **the critical one** — static binary, no libc. Without it, `scratch`/`distroless` images fail with a baffling "no such file or directory" |
| `-trimpath` | strips absolute build paths from the binary |
| `-ldflags="-s -w"` | strips symbol table + DWARF, ~25% smaller. Omit if you want readable production profiles |
| `-ldflags="-X main.version=…"` | injects strings into package-level `var`s |
| `GOOS` / `GOARCH` | cross-compile. From Windows to Linux in one command, no toolchain to install |

Cross-compiling is a genuine Go superpower:

```bash
GOOS=linux   GOARCH=arm64 go build -o bin/app-linux-arm64  ./46_docker
GOOS=darwin  GOARCH=arm64 go build -o bin/app-macos-arm64  ./46_docker
GOOS=windows GOARCH=amd64 go build -o bin/app.exe          ./46_docker
```

---

## The five mistakes that cost the most

### 1. `ENTRYPOINT /app` instead of `ENTRYPOINT ["/app"]`

The shell form runs your binary under `/bin/sh -c`, so **the shell is PID 1**
and does not forward SIGTERM. Your graceful shutdown (lesson 42) never runs;
every deploy hangs for the grace period and then hard-kills in-flight
requests. Always use the exec form. (On distroless there is no shell at all,
so the shell form simply fails.)

### 2. Copying source before `go mod download`

```dockerfile
COPY go.mod go.sum ./     # ← these change rarely
RUN go mod download       # ← so this layer is cached
COPY . .                  # ← this changes constantly
RUN go build ...
```

Reverse those and every one-character code change re-downloads your entire
dependency tree. This ordering is the difference between a 10-second and a
3-minute build.

### 3. Forgetting `CGO_ENABLED=0`

Symptoms: `exec /app: no such file or directory` on a `scratch` image (the
file *is* there — the dynamic linker isn't), or `exec format error`.

If you genuinely need CGO (some SQLite drivers, some crypto libraries), you
must ship a base with libc — and match it: a glibc binary won't run on
Alpine's musl. The pure-Go `modernc.org/sqlite` from lesson 36 exists
precisely so you don't have to deal with this.

### 4. Binding to `127.0.0.1`

Inside a container, loopback is the container's own loopback. Bind `0.0.0.0`
or nothing will reach you from outside, no matter how you map ports.

### 5. Running as root

`USER nonroot:nonroot`. It costs nothing and stops a container escape from
being a root escape.

---

## GOMAXPROCS and CPU limits

Go sets `GOMAXPROCS` to the number of CPUs it can see — the **host's**, not
your container's limit. A pod with a 1-CPU limit on a 64-core node runs 64
OS threads fighting over 1 CPU's worth of quota, which shows up as severe
latency spikes from CFS throttling.

Two fixes:

```go
import _ "go.uber.org/automaxprocs"  // reads the cgroup limit at startup
```

or set it explicitly in your deployment:

```yaml
env:
  - name: GOMAXPROCS
    valueFrom:
      resourceFieldRef:
        resource: limits.cpu
```

Related: set `GOMEMLIMIT` to ~80% of the container memory limit so the GC
works harder as you approach it, instead of the kernel OOM-killing you.

---

## Base image options

| base | size | shell? | when |
|---|---:|---|---|
| `gcr.io/distroless/static-debian12:nonroot` | ~2 MB | no | **default choice.** CA certs, tzdata, nonroot user |
| `scratch` | 0 B | no | absolute minimum; you must `COPY` the CA bundle and tzdata yourself or TLS and time zones silently break |
| `alpine:3.20` | ~8 MB | yes | when you want `docker exec` for debugging |
| `debian:bookworm-slim` | ~80 MB | yes | only when you actually need glibc (CGO) |

For `scratch`, add to the build stage:

```dockerfile
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

---

## Kubernetes essentials

```yaml
spec:
  terminationGracePeriodSeconds: 30   # must exceed your shutdown timeout
  containers:
    - name: api
      image: go-basics:1.0.0          # never :latest
      ports:
        - containerPort: 8080
      env:
        - name: GOMAXPROCS
          valueFrom: { resourceFieldRef: { resource: limits.cpu } }
      resources:
        requests: { cpu: 100m, memory: 64Mi }
        limits:   { cpu: "1",  memory: 256Mi }
      livenessProbe:                  # "restart me if this fails"
        httpGet: { path: /healthz, port: 8080 }
        periodSeconds: 10
      readinessProbe:                 # "send me traffic if this passes"
        httpGet: { path: /readyz, port: 8080 }
        periodSeconds: 5
      lifecycle:
        preStop:
          exec:
            # Give kube-proxy time to remove this pod from the Service
            # endpoints BEFORE SIGTERM arrives. Without it you still drop a
            # few requests on every deploy, no matter how graceful your
            # shutdown is.
            command: ["sleep", "5"]
      securityContext:
        runAsNonRoot: true
        readOnlyRootFilesystem: true
        allowPrivilegeEscalation: false
        capabilities: { drop: ["ALL"] }
```

**Liveness must not check your database.** If Postgres goes down, failing
liveness restarts every pod in a loop, which helps nobody. Liveness = "is
this process wedged?". Readiness = "can I serve traffic right now?".

---

## Scanning the image

```bash
docker scout cves go-basics:dev
trivy image go-basics:dev
govulncheck ./...            # Go-specific, and call-graph aware (lesson 45)
```

A distroless image usually scans clean, because there's almost nothing in it
to be vulnerable.

---

## A Makefile worth copying

```makefile
VERSION    := $(shell git describe --tags --always --dirty)
COMMIT     := $(shell git rev-parse --short HEAD)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X 'main.version=$(VERSION)' \
                    -X 'main.commit=$(COMMIT)' \
                    -X 'main.buildDate=$(BUILD_DATE)'

.PHONY: build build-linux docker test lint ci

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/app ./46_docker

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/app-linux ./46_docker

docker:
	docker build -f 46_docker/Dockerfile \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t go-basics:$(VERSION) -t go-basics:latest .

test:
	go test -race -count=1 ./...

lint:
	gofmt -l .
	go vet ./...
	golangci-lint run ./...

ci: lint test docker
```

---

## Checklist before you ship

- [ ] Multi-stage build; the final image has no compiler and no source
- [ ] `CGO_ENABLED=0`, `-trimpath`, `-ldflags="-s -w"`
- [ ] `go mod download` layer cached before `COPY . .`
- [ ] `ENTRYPOINT` in **exec form**
- [ ] `USER nonroot`, `readOnlyRootFilesystem`
- [ ] Distroless or scratch base, pinned by tag (never `:latest`)
- [ ] `.dockerignore` excludes `.git`, `.env`, `*.db`
- [ ] Binds `0.0.0.0`, port from `$PORT`
- [ ] Graceful shutdown handles SIGTERM, faster than the grace period
- [ ] `/healthz` and `/readyz` are separate, and liveness doesn't hit the DB
- [ ] `GOMAXPROCS` matches the CPU limit
- [ ] Image scanned; `govulncheck` clean

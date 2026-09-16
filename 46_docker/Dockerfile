# =============================================================================
# A production multi-stage Dockerfile for a Go service.
#
# Build from the REPOSITORY ROOT (the build context must contain go.mod):
#
#   docker build -f 46_docker/Dockerfile -t go-basics:dev .
#
# Stage 1 has the whole Go toolchain (~800 MB). Stage 2 keeps only the
# compiled binary. Nothing from stage 1 — not the source, not the compiler,
# not your build secrets — ends up in the shipped image.
# =============================================================================


# -----------------------------------------------------------------------------
# STAGE 1: BUILD
# -----------------------------------------------------------------------------
# Pin the exact Go version. `golang:latest` makes your builds
# non-reproducible and will break you on a Tuesday. Alpine keeps the build
# stage small; it doesn't affect the final image either way.
FROM golang:1.26-alpine AS build

# Build arguments, passed with --build-arg. The default keeps a plain
# `docker build` working.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

WORKDIR /src

# ---- LAYER CACHING: the single most important thing in this file ----
# Copy ONLY the module files first and download dependencies. Docker caches
# each layer and reuses it while its inputs are unchanged — so as long as
# go.mod/go.sum don't change, this expensive step is skipped on every
# subsequent build, even when you've edited every .go file.
#
# Copying the whole source first would invalidate the cache on every single
# code change and re-download everything. This ordering routinely turns a
# 3-minute build into 10 seconds.
COPY go.mod go.sum ./
RUN go mod download

# NOW copy the source. Everything below this line re-runs when code changes.
COPY . .

# ---- the build itself ----
# CGO_ENABLED=0 is the critical flag. It produces a STATICALLY LINKED binary
# with no libc dependency, which is what lets the final stage be `scratch`
# or `distroless`. Without it you get "exec format error" or
# "no such file or directory" at runtime — a confusing message that actually
# means "the dynamic linker is missing".
#
# -trimpath removes absolute paths from the binary: smaller, and it stops
# leaking your build machine's directory layout into stack traces.
#
# -ldflags:
#   -s -w      strip the symbol table and DWARF debug info (~25% smaller).
#              Do NOT use these if you want readable production profiles.
#   -X         inject build-time values into package-level string vars.
#
# The two --mount=type=cache lines share Go's module and build caches across
# builds (BuildKit, on by default in modern Docker). Big speed-up.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w \
        -X 'main.version=${VERSION}' \
        -X 'main.commit=${COMMIT}' \
        -X 'main.buildDate=${BUILD_DATE}'" \
      -o /out/app \
      ./46_docker

# Fail the build here rather than in production if the binary is somehow
# dynamically linked.
RUN ldd /out/app 2>&1 | grep -q "Not a valid dynamic program\|not a dynamic executable" \
    || (echo "ERROR: binary is dynamically linked; set CGO_ENABLED=0" && exit 1)


# -----------------------------------------------------------------------------
# STAGE 2: RUNTIME
# -----------------------------------------------------------------------------
# gcr.io/distroless/static-debian12 contains: CA certificates, /etc/passwd
# with a nonroot user, tzdata, and nothing else. No shell, no package
# manager, no busybox — so there is nothing for an attacker to pivot into
# even with RCE. ~2 MB.
#
# Alternatives:
#   scratch            absolutely empty (~0 B). You must COPY the CA bundle
#                      and tzdata yourself or TLS and time zones break.
#   alpine:3.20        ~8 MB, has a shell — handy for debugging, slightly
#                      larger attack surface. Note: only needed if CGO is on,
#                      and then you must also handle musl vs glibc.
#   debian:bookworm-slim  ~80 MB. Use only if you genuinely need glibc.
FROM gcr.io/distroless/static-debian12:nonroot

# Labels feed container registries and `docker inspect`.
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="go-basics" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.source="https://example.com/go-basics"

# Run as a non-root user. The distroless :nonroot tag already provides
# uid/gid 65532; being explicit documents it and survives a base-image swap.
USER nonroot:nonroot

COPY --from=build --chown=nonroot:nonroot /out/app /app

# EXPOSE is documentation only — it publishes nothing. `docker run -p` does
# the actual port mapping.
EXPOSE 8080

ENV PORT=8080

# ---- USE THE EXEC FORM ----
# ENTRYPOINT ["/app"]   -> your process is PID 1 and RECEIVES SIGTERM.
# ENTRYPOINT /app       -> runs as `/bin/sh -c /app`; the SHELL is PID 1 and
#                          does not forward signals, so your graceful
#                          shutdown code NEVER RUNS and every deploy kills
#                          in-flight requests after a 10-second hang.
#
# This is the #1 reason "graceful shutdown doesn't work in Docker". It also
# means the shell form cannot work at all on distroless — there is no shell.
ENTRYPOINT ["/app"]

# NOTE ON HEALTHCHECK: you'd normally add
#   HEALTHCHECK CMD curl -f http://localhost:8080/healthz || exit 1
# but distroless has no curl and no shell. Options: let the orchestrator do
# it (Kubernetes httpGet probes — the right answer), or compile a tiny
# health-check mode into your own binary and use
#   HEALTHCHECK CMD ["/app", "-healthcheck"]

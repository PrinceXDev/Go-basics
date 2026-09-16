# Production image for the capstone. See 46_docker/README.md for the full
# explanation of every line here.
#
# Build from the REPOSITORY ROOT:
#   docker build -f 47_capstone/Dockerfile -t tasks-api:dev .

FROM golang:1.26-alpine AS build

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

WORKDIR /src

# Dependencies first, so this layer stays cached across code changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 works here only because the store uses modernc.org/sqlite,
# a PURE-GO driver. mattn/go-sqlite3 would need CGO and a glibc base image.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags="-s -w \
        -X 'main.version=${VERSION}' \
        -X 'main.commit=${COMMIT}' \
        -X 'main.buildDate=${BUILD_DATE}'" \
      -o /out/api ./47_capstone/cmd/api

# Run the tests inside the image build so a broken build can never ship.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go test ./47_capstone/...

FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
LABEL org.opencontainers.image.title="tasks-api" \
      org.opencontainers.image.version="${VERSION}"

USER nonroot:nonroot
COPY --from=build --chown=nonroot:nonroot /out/api /api

# The SQLite file needs a writable location. With readOnlyRootFilesystem
# you must mount an emptyDir or a volume here.
VOLUME ["/data"]
ENV APP_DB_PATH=/data/tasks.db \
    APP_ADDR=0.0.0.0:8080 \
    APP_ENV=prod \
    APP_LOG_LEVEL=info

EXPOSE 8080

# EXEC FORM — so the process is PID 1 and receives SIGTERM.
ENTRYPOINT ["/api"]

# Go (Golang) Learning Roadmap

Tracking progress through a JS/TS developer's Go fundamentals. Each topic gets
its own numbered folder with a heavily-commented `main.go` (explanation +
runnable example), following the pattern established in `01`-`06`.

## Phase 1 — Fundamentals ✅ (done)

- [x] `01_setup_first-program` — toolchain, `package main`, `func main()`
- [x] `02_variables_and_types` — `var`, scope, basic types
- [x] `03_var_vs_short_declare` — `var` vs `:=`
- [x] `04_types_practice` — zero values, hands-on practice
- [x] `05_constants_and_formatting` — `const`, `fmt.Printf` verbs
- [x] `06_control_flow` — `if`/`else`, `for`, `switch`

## Phase 2 — Core language ✅ (done)

- [x] `07_functions` — params, multiple return values, named returns, variadic functions
- [x] `08_error_handling` — the `error` type, `if err != nil`, `errors.New`, `fmt.Errorf`, `%w` wrapping, panic/recover
- [x] `09_arrays_and_slices` — fixed arrays vs dynamic slices, `append`, `len`/`cap`, slicing syntax, shared-array gotcha
- [x] `10_maps` — Go's dictionary/object equivalent, CRUD, `comma-ok` idiom, iteration order (unordered!)
- [x] `11_structs` — custom types, value-type copy semantics
- [x] `12_methods` — functions with receivers (value vs pointer receivers)
- [x] `13_pointers` — `&` and `*`, why Go has pointers but no pointer arithmetic
- [x] `14_interfaces` — structural typing (duck typing), the empty interface `any`, type assertion/switch
- [x] `15_embedding` — struct embedding as Go's answer to inheritance

## Phase 3 — Working with real code

- [x] `16_json` — `encoding/json`, struct tags, `Marshal`/`Unmarshal`, nested JSON, decoding into `map[string]any`
- [x] `17_packages_and_modules` — splitting code into multiple packages, import paths, module vs package
- [x] `18_third_party_modules` — `go get`, `go mod tidy`, semantic versioning, `go.sum`, using `github.com/google/uuid`
- [x] `19_files_and_io` — reading/writing files, `os` package, `bufio`, Windows file-lock gotcha
- [x] `20_testing` — the built-in `testing` package, table-driven tests, `go test`
- [x] `21_generics` — type parameters (Go 1.18+), constraints, generic struct (Stack[T])

## Phase 4 — Concurrency (Go's superpower) ✅ (done)

- [x] `22_goroutines` — lightweight threads, the `go` keyword, `sync.WaitGroup`
- [x] `23_channels` — communicating between goroutines, buffered vs unbuffered, closing/draining
- [x] `24_select_and_sync` — `select`, timeouts, `sync.Mutex`, race condition proven live (992 vs 1000)
- [x] `25_context` — `context.Context` for cancellation/timeouts, WithTimeout/WithCancel/WithValue

## Phase 5 — Building real things ✅ (done)

- [x] `26_cli_tool` — `flag` package, file I/O, word-count CLI, Stderr/exit codes
- [x] `27_http_server` — `net/http`, ServeMux, GET/POST JSON handlers, query params
- [x] `28_http_client_and_json_api` — `http.Get`/`Post`, custom `http.Client` with timeout
- [x] `29_project_structure` — `cmd/`/`internal/`/`pkg/` layout, working example-layout todo API

## Phase 6 — Production Go ✅ (done)

Everything from here on is about shipping, not about the language. Each
section builds on the last, and section D's capstone combines all of them.

### A. Language gaps

- [x] `32_struct_tags_and_validation` — struct tags + reflection, why `json:"x"` works, go-playground/validator, the `required`-on-a-bool trap
- [x] `33_time_and_formatting` — `time.Time`/`Duration`, the reference layout (`2006-01-02`), monotonic clocks, `Equal()` vs `==`, tickers
- [x] `34_sorting_and_stdlib` — `slices`, `maps`, `cmp.Or` multi-key sorts, `strings.Builder`, `strconv`, a JS→Go cheat sheet
- [x] `35_errors_advanced` — sentinels vs custom types, `errors.Is`/`As`/`Join`, `%w` chains through a 3-layer app

### B. Data persistence

- [x] `36_database_sql` — the pool, placeholders, `sql.ErrNoRows`, the `rows.Next`/`rows.Err` loop, transactions, NULL handling
- [x] `37_migrations_and_repository` — a ~60-line migration runner over `//go:embed` SQL, the repository pattern, a swappable in-memory fake
- [x] `38_sqlc_or_gorm` — runnable GORM tour + its gotchas; `README.md` covers sqlc end to end and how to choose

### C. Production HTTP service

- [x] `39_middleware` — `func(http.Handler) http.Handler`, requestID/logging/recover/auth/timeout, and why Recover must sit inside Timeout
- [x] `40_structured_logging` — `log/slog`, JSON vs text, `.With` child loggers, context-scoped loggers, `ReplaceAttr` redaction
- [x] `41_config_and_env` — defaults ← env ← flags, validate-at-startup, `errors.Join` for all failures at once, a `Secret` type that won't print
- [x] `42_graceful_shutdown` — `signal.NotifyContext`, `srv.Shutdown`, draining order (readiness → server → workers → deps), k8s notes
- [x] `43_testing_http` — `httptest.NewRecorder` vs `NewServer`, table-driven handler tests, injected failures, testing a client

### D. Tooling & shipping

- [x] `44_benchmarks_and_profiling` — `b.Loop()`, `-benchmem`, benchstat, pprof, escape analysis; `README.md` is the profiling walkthrough
- [x] `45_race_detector_and_linting` — five real races and their fixes, build tags, `go vet`, `golangci-lint` (`.golangci.yml` at the repo root), CI config
- [x] `46_docker` — multi-stage distroless build, `CGO_ENABLED=0`, ldflags version injection, the exec-form/PID-1 trap, compose, k8s manifest
- [x] `47_capstone` — a deployable tasks API: `cmd/`+`internal/`, migrations, repository, middleware, slog, config, graceful shutdown, integration tests, Dockerfile

### Also present, outside the original plan

- [x] `30_closure` — closures over captured variables
- [x] `31_demo_examples` — scratch/demo code

---

## How to run each lesson

```bash
go run ./NN_folder_name          # most lessons
go test ./20_testing/            # 20, 43, 44 are test packages
go test -bench=. -benchmem ./44_benchmarks_and_profiling/
go run -race ./45_race_detector_and_linting
go run ./47_capstone/cmd/api
```

Whole-repo checks:

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

Lessons 36, 37, 38 and 47 create local `*.db` files; each folder has a
`.gitignore` for them.

---

## Where to go after this

The roadmap is complete — 01 through 47 all run and pass. Beyond it, pick
based on what you actually need:

- **gRPC / protobuf** — for service-to-service APIs
- **Postgres for real** — swap the driver in `47_capstone`, add
  testcontainers-go
- **OpenTelemetry** — metrics and distributed tracing
- **Worker pools, rate limiting, circuit breakers** — `errgroup`,
  `golang.org/x/time/rate`
- **`text/template` + HTMX** — server-rendered UI with no JS build step
- **Read real code** — the stdlib itself (`net/http`, `database/sql`),
  then a well-written service. This is the highest-value thing on the list.

---
**How we work:** each folder = one `package main` you run individually via
`go run ./NN_folder_name`. Concept folders are created directly with
explanation comments + runnable examples — no need to ask before adding the
next one. Update the checkboxes above as topics are completed.

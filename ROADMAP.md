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

## All phases complete

Every topic in this roadmap (01 through 29) has been created, explained, and
verified to run/pass. This is a solid foundation for real-world Go work.
Natural next steps beyond this roadmap, if wanted later: a persistent
database (`database/sql` + a driver, or an ORM like GORM/sqlc), gRPC,
Docker/deployment, or a larger capstone project combining everything above.

---
**How we work:** each folder = one `package main` you run individually via
`go run ./NN_folder_name`. Concept folders are created directly with
explanation comments + runnable examples — no need to ask before adding the
next one. Update the checkboxes above as topics are completed.

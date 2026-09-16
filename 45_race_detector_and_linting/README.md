# 45 — Race Detector & Linting

`main.go` is the race detector half — run it both ways and compare:

```bash
go run ./45_race_detector_and_linting
```

```bash
go run -race ./45_race_detector_and_linting
```

This README covers the static-analysis half: `go vet`, `golangci-lint`, and
what to put in CI.

---

## 1. `go vet` — already installed, already free

`go vet` ships with Go and finds real bugs the compiler allows. It runs
automatically as part of `go test`, but run it explicitly too:

```bash
go vet ./...
```

What it actually catches (all of these compile fine):

| check | the bug |
|---|---|
| `printf` | `fmt.Printf("%d", "hello")` — wrong verb for the type |
| `printf` | `slog.Info("msg", "orphan")` — odd number of key/value args |
| `lostcancel` | a `context.WithCancel` whose `cancel` is never called (leak) |
| `copylocks` | copying a `sync.Mutex` by value — silently stops locking |
| `loopclosure` | the pre-1.22 loop-variable capture bug |
| `httpresponse` | using `resp` before checking `err` |
| `unmarshal` | passing a non-pointer to `json.Unmarshal` |
| `structtag` | a malformed struct tag (lesson 32) |
| `nilfunc`, `unreachable`, `shift`, `atomic`, … | |

Two of these bit me while writing this roadmap: `go vet` caught a
deliberately malformed `slog` call in lesson 40 and a `%v` inside a
`fmt.Println` string in lesson 35. It earns its keep.

See every available check:

```bash
go tool vet help
```

---

## 2. `golangci-lint` — everything else

`golangci-lint` runs ~100 linters in parallel over a shared parse of your
code, so it's far faster than running them individually.

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
golangci-lint run ./...
```

Useful flags:

```bash
golangci-lint run --fix ./...        # auto-fix what can be fixed
golangci-lint run --new-from-rev=HEAD~1   # only NEW issues — key for legacy code
golangci-lint linters                # list every linter and whether it's on
golangci-lint run ./39_middleware/   # one package
```

The config lives in [`.golangci.yml`](../.golangci.yml) at the repo root, and
every linter in it has a comment explaining why.

### The ones that pay for themselves

- **`errcheck`** — the single most valuable Go linter. An ignored `err` is
  the most common real bug in Go code. It's what catches
  `defer resp.Body.Close()` missing, or GORM's `.Error` never being read
  (lesson 38).
- **`staticcheck`** — ~150 checks, very low false-positive rate. In
  golangci-lint v2 it absorbed `gosimple` and `stylecheck`.
- **`bodyclose` / `sqlclosecheck` / `rowserrcheck`** — each catches a
  resource leak that only shows up under production load. `rowserrcheck`
  specifically catches the silent result truncation from lesson 36.
- **`errorlint`** — flags `%v` where you meant `%w`, and `err == ErrX` where
  you meant `errors.Is` (lesson 35).

### Starting on an existing codebase

Turning on 20 linters over a legacy repo gives you 4,000 issues and
everyone turns it back off. Instead:

```bash
golangci-lint run --new-from-rev=origin/main
```

Only new code has to pass. The old code improves as it's touched.

---

## 3. Formatting

There is no debate to have. `gofmt` is the format.

```bash
gofmt -l .        # list files that need formatting (empty output = good)
gofmt -w .        # rewrite them
goimports -w .    # gofmt + fix the import block
```

Set your editor to format on save with `goimports` and never think about it
again. In CI, fail the build if `gofmt -l .` prints anything.

---

## 4. Putting it in CI

The four commands, in the order that gives you the fastest feedback:

```bash
gofmt -l .                  # style     (instant)
go vet ./...                # bugs      (seconds)
golangci-lint run ./...     # more bugs (tens of seconds)
go test -race -count=1 ./...  # behaviour + races (slowest)
```

A GitHub Actions workflow:

```yaml
name: CI
on: [push, pull_request]

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
          cache: true

      - name: Format
        run: |
          test -z "$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w ."; exit 1; }

      - name: Vet
        run: go vet ./...

      - name: Lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: latest

      - name: Test
        run: go test -race -count=1 -coverprofile=coverage.out ./...
```

The same thing as a GitLab job:

```yaml
lint-and-test:
  image: golang:1.26
  script:
    - test -z "$(gofmt -l .)"
    - go vet ./...
    - go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
    - golangci-lint run ./...
    - go test -race -count=1 ./...
```

`-count=1` disables Go's test result cache, so CI always actually runs the
tests.

---

## 5. Other tools worth knowing

| tool | what it does |
|---|---|
| `go test -race` | the race detector (this lesson's `main.go`) |
| `govulncheck ./...` | scans your dependencies for **known CVEs**, and — unlike most scanners — only reports the ones your code actually *calls*. Install: `go install golang.org/x/vuln/cmd/govulncheck@latest` |
| `go mod tidy -diff` | fails if go.mod/go.sum are out of date (good CI check) |
| `go build -gcflags='-m'` | escape analysis (lesson 44) |
| `gopls` | the language server behind every Go editor integration |
| `deadcode ./...` | finds unreachable functions across the whole program |

`govulncheck` in particular should be in your CI and your weekly routine:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

---

## The short version

```
gofmt on save.
go vet + golangci-lint on every commit.
go test -race in CI, always.
govulncheck weekly.
```

Four tools, all free, all in or adjacent to the standard toolchain. Most
Go teams run exactly this and nothing else.

# Idiomatic Go project structure

This lesson has no single `main.go` to run — it's a reference layout,
paired with a small working example under `example-layout/`, that you can
build and run to see the pieces connect.

## Why layout matters (and why it's different from JS/TS)

A typical Node backend often looks like:

```
src/
  routes/
  controllers/
  services/
  models/
```

Go doesn't have an official folder convention, but the ecosystem converged
on a well-known community pattern (the "Standard Go Project Layout"). The
key idea: **folder = package**, and Go's tooling (imports, visibility)
actively enforces boundaries between them — layout isn't just convention,
it changes what code CAN and CANNOT import.

## The three folders that matter most for a beginner

- **`cmd/`** — one subfolder PER EXECUTABLE your project produces. Each
  subfolder is a tiny `package main` whose only job is wiring things
  together and calling into your real logic elsewhere. If your project
  only ever builds one binary, you *can* skip this and put `main.go` at
  the root (like every lesson so far in this course) — `cmd/` earns its
  keep once you have multiple binaries (e.g. an API server + a CLI admin
  tool) sharing the same business logic.

- **`internal/`** — this folder name is SPECIAL to the Go compiler itself,
  not just a convention. Code inside any `internal/` folder can only be
  imported by code that lives inside the same parent tree. It's Go's
  built-in way to say "this is a private implementation detail of THIS
  module" — enforced by the toolchain, not just an honor system. There is
  no equivalent enforced boundary in a typical Node project; you'd rely on
  documentation or lint rules instead.

- **`pkg/`** — code you're comfortable with OTHER projects importing (a
  shared library that happens to live in this repo). Less universally used
  than `internal/`, and somewhat debated in the Go community — some
  maintainers skip it entirely and just use `internal/` plus the module
  root. Understand it, but don't feel obligated to always include it.

## The example under `example-layout/`

```
example-layout/
  go.mod                        <- its own separate module for this demo
  cmd/api/main.go               <- the executable: wires everything together
  internal/store/store.go       <- private business logic (an in-memory todo store)
  pkg/validator/validator.go    <- a reusable validation helper, "public" by intent
```

Build and run it:
```
cd 29_project_structure/example-layout
go run ./cmd/api
```

Trace the imports in `cmd/api/main.go` to see how the pieces connect — that
file is deliberately thin; it should read almost like a table of contents
for the rest of the program. This "thin main, fat internal packages"
shape is the single most important habit to carry into any real Go
project you build after this course.

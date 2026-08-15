# Reading guide — MinDB as a way to learn Go

An ordered path through this codebase, aimed at someone who knows how to
program but is new to Go specifically. Each step names the file(s), what to
read for, and the Go language features it happens to teach — in the order
you'll actually meet them, not a language-manual order. Skipping ahead is
fine, but the concepts do build on each other roughly in this sequence.

Companion docs: [`FEATURES.md`](FEATURES.md) explains *what* each feature does
in depth; this file is about *how to read the Go*. [`DECISIONS.md`](DECISIONS.md)
explains *why* things are built this way.

Before starting, run `go version` — this repo targets Go 1.25. If you don't
have Go installed, get it from https://go.dev/dl/.

---

## Step 0 — orientation

Run these, in this order, before reading any code:

```sh
go build ./...     # does it compile?
go vet ./...        # does the standard linter find anything?
go test ./...        # do the tests pass?
```

A Go **module** is a source tree rooted at a `go.mod` file. `go.mod`
(read it — it's five lines of actual content) declares the module's import
path (`github.com/typicallhavok/mindb`) and its dependencies. A **package**
is just a directory: every `.go` file in a directory must declare the same
`package name` at its top, and that's the unit of code organization Go has
— there's no nested-class or namespace mechanism beyond directories and
package names. `go.sum` is a lockfile with cryptographic hashes of every
dependency version; you never hand-edit it.

Look at the top-level layout:

```
cmd/mindb-server/   the runnable binary (package main)
pkg/math/            vector math + generated SIMD assembly
pkg/core/             the engine: storage, search, snapshots
pkg/api/               gRPC service implementation
pkg/mindb/             generated code (flatc) — never hand-edit
fbs/mindb.fbs           the wire schema, source of pkg/mindb/
```

`cmd/` vs `pkg/` is a convention, not a language rule: `cmd/` holds
`package main` binaries, `pkg/` holds importable libraries. Go enforces
nothing about directory names — this split exists because it's what every
Go programmer expects to see.

---

## Step 1 — `pkg/math/distance.go`

The simplest file in the repo: three free functions, no types. Start here.

**What to read for:** basic Go syntax — function signatures, slices,
`for` loops, panics as the "this is a programmer error, not a runtime
condition" mechanism.

**Go concepts you'll meet:**

- **Slices** (`[]float32`). Not arrays — a slice is a view (pointer + length
  + capacity) over an underlying array. `a[i:]` doesn't copy; it's a new
  slice header pointing into the same memory starting at index `i`. This is
  why `Dot(a, b []float32)` doesn't need `&a`: slices already carry a
  pointer internally, so passing one by value is cheap and the callee sees
  the same underlying data.
- **Multiple return values**, no tuples needed:
  `func Normalize(v []float32) float32` returns one value here, but you'll
  see `(scale, residual float32)`-style multi-returns in `quantize.go` next
  — Go functions can return more than one value natively, which is how Go
  does what other languages use exceptions or `Result<T, E>` for (see
  errors in Step 3).
- **`panic`** used deliberately for a violated precondition
  (`len(a) != len(b)`) rather than returning an error. The convention in Go:
  `panic` for "this is a bug in the caller," `error` returns for "this is
  an expected failure mode." `Dot` panicking on mismatched lengths is a
  judgment call the comments explain — the caller controls both slices, so
  a length mismatch can only be a bug.
- **Type conversions are explicit**: `float64(x)` converts `x` to
  `float64`. Go never does this implicitly, unlike C or Python — you'll see
  this constantly (`float32(c)`, `int32(q)`, etc.) because Go refuses to
  silently narrow or widen numeric types.
- **`(*[8]float32)(s[i:])`** — this one's genuinely advanced, so don't
  worry about it on first read; the comment above it explains the compiler
  benefit (bounds-check elimination). It converts a slice to a pointer to a
  fixed-size array. Come back to this after Step 4.

Read the whole file, then `pkg/math/distance_test.go` — table-driven tests
using a `[]struct{...}` of cases looped over with `for _, tc := range cases`
is the single most common Go testing pattern; you'll see it in every
`_test.go` file in this repo.

---

## Step 2 — `pkg/math/quantize.go`

Still free functions, no types yet, but doing more interesting math.

**Go concepts you'll meet:**

- **Named return values**: `func Quantize(...) (scale, residual float32)`.
  The names `scale` and `residual` are pre-declared local variables inside
  the function; a bare `return` would return their current values (this
  file doesn't use bare returns, but you'll see the pattern elsewhere in
  Go generally — it's worth recognizing).
- **`stdmath "math"`** at the top of the file — a renamed import, because
  this package is *also* named `math` (`package math` at the top of the
  file) and would otherwise collide with the standard library's `math`
  package. Import renaming (`alias "path"`) is how Go resolves that.
- **Zero values**: a freshly-declared `var maxAbs float32` starts at `0`,
  not garbage — every Go type has a well-defined zero value (`0` for
  numbers, `""` for strings, `nil` for pointers/slices/maps/interfaces,
  `false` for bool). This is why you'll rarely see explicit initialization
  to zero in Go code.

Then `DotInt8` in the same file — notice it's structurally identical to
`Dot` from Step 1, just multiplying against `float32(code[i])` instead of
`b[i]` directly. Read `quantize_test.go` alongside it, particularly
`TestBoundHoldsForRandomQueries` — this is the test that encodes the
project's actual mathematical guarantee, worth reading slowly even before
you're fully comfortable with the Go.

---

## Step 3 — `pkg/math/kernel.go`, `kernel_amd64.go`, `kernel_generic.go`

Three small files that together implement runtime dispatch. This is where
Go's **build tags** and **package-level variables** show up.

**Go concepts you'll meet:**

- **Build constraints**: `//go:build amd64` at the top of a file (must be
  followed by a blank line) tells the compiler "only include this file when
  building for amd64." `kernel_generic.go` has `//go:build !amd64` — the
  complement. Only one of the two is ever compiled into a given binary; Go
  picks based on `GOARCH`. This is how the same function name
  (`hasFastInt8`, `dotInt8Impl`) can have a completely different
  implementation per platform with zero runtime branching cost.
- **Package-level `var` with `init()`**: `kernel_amd64.go` declares
  `var dotInt8Impl func(q []float32, code []int8) float32` (a **function
  value** — functions are first-class values in Go, storable in variables,
  just like any other type) and assigns it inside an `init()` function.
  `init()` is a special function name: Go calls every package's `init()`
  automatically, before `main()` runs and before anything in that package
  is used, with no explicit call site anywhere. This is exactly where the
  CPU-feature detection happens — once, at program startup, not on every
  call to `DotInt8`.
- **Function values as a dispatch mechanism**: `DotInt8` in `quantize.go`
  just calls `dotInt8Impl(q, code)`. This is Go's version of a strategy
  pattern — no interfaces needed for a single-function swap, just a
  variable holding a function.

This is a good point to re-read `pkg/math/kernel_avx2_test.go` and notice
`if !hasFastInt8 { t.Skip(...) }` — `t.Skip` marks a test as skipped
(not failed) when its precondition isn't met, which is exactly right for
"this test requires AVX2 hardware."

---

## Step 4 — `pkg/core/engine.go`

The center of the project, and where Go's object-orientation-without-classes
shows up in full. Read this file slowly — everything after it builds on it.

**Go concepts you'll meet:**

- **Structs and methods**: `type Engine struct { ... }` declares a type;
  `func (e *Engine) Insert(...) error` declares a **method** on `*Engine`.
  There's no `class` keyword — a struct plus a set of functions with a
  matching receiver *is* Go's notion of a type with behavior. `(e *Engine)`
  is the **receiver**: inside the method, `e` is how you refer to "this
  instance," playing the role `this`/`self` plays elsewhere.
- **Pointer vs value receivers**: every method here uses `*Engine` (a
  pointer receiver), not `Engine`. A pointer receiver lets the method
  mutate the actual struct the caller has; a value receiver would operate
  on a *copy*. Rule of thumb you'll see followed consistently in this repo:
  if any method on a type needs a pointer receiver (because it mutates
  state, as nearly every `Engine` method does), make *all* its methods
  pointer receivers, for consistency.
- **`sync.RWMutex`**: `e.mu.RLock()` / `e.mu.RUnlock()` for reads,
  `e.mu.Lock()` / `e.mu.Unlock()` for writes. Multiple readers can hold
  `RLock` simultaneously; `Lock` is exclusive against everything. This is
  the entire concurrency story of the engine — see
  [`DECISIONS.md`](DECISIONS.md#concurrency) for why it's this simple.
- **`defer`**: `defer e.mu.Unlock()` immediately after `e.mu.Lock()`
  schedules the unlock to run when the *function* returns, regardless of
  which `return` statement it hits or whether it panics. This is Go's
  primary idiom for "always clean this up," and you'll see it constantly:
  `defer f.Close()`, `defer wg.Done()`, etc. It runs in LIFO order if there
  are multiple defers in one function.
- **Error handling**: Go has no exceptions for expected failures. A
  function that can fail returns `(T, error)`, and the caller checks
  `if err != nil` immediately after every call that can fail — you'll see
  this pattern on nearly every other line of `store`, `Save`, `Load`.
  `errors.New(...)` creates a plain error; `fmt.Errorf("...: %w", err)`
  wraps an existing error while preserving it for `errors.Is`/`errors.As`
  to unwrap later (see `insertError` in `pkg/api/grpc_server.go` for
  `errors.Is` in use). Sentinel errors like `ErrDimensionMismatch` are
  package-level `var`s so callers can compare against them by identity.
- **`sync.Pool`**: `e.heaps sync.Pool`. A pool of reusable objects to cut
  down on garbage-collector pressure — `Get()` returns either a recycled
  object or calls the pool's `New` function to make one, `Put()` returns an
  object for reuse. Crucially, a `sync.Pool` does *not* guarantee an object
  survives — the GC can clear it at any time, so a pool is a cache, not a
  free list you can rely on for correctness (contrast this with `e.free
  []uint32`, which is a real free list the engine's own logic depends on).
- **Goroutines and `sync.WaitGroup`** (in `scan`): `go func(...) { ... }()`
  starts a new goroutine — a lightweight, Go-runtime-managed concurrent
  function call, not an OS thread (though the runtime multiplexes many
  goroutines onto few OS threads for you). `wg.Add(1)` before starting each
  one, `wg.Done()` (usually deferred) inside each one, `wg.Wait()` in the
  parent to block until they've all finished — this is the standard Go
  pattern for "fan out N tasks, wait for all of them."
- **Maps**: `idMap map[string]uint32`. Go's built-in hash map type.
  `slot, existing := e.idMap[id]` is the two-value map read: `existing` is
  `false` (and `slot` is the zero value) if the key isn't present — this is
  the same "comma ok" pattern you'll see with type assertions and channel
  receives elsewhere in Go.
- **Type assertion**: `e.heaps.Get().(*topK)` — `sync.Pool.Get()` returns
  `any` (Go's `interface{}`, the type that can hold anything), and `.(*topK)`
  asserts it's actually a `*topK`, panicking if it's wrong. Safe here
  because the pool's `New` function only ever produces `*topK` values.

Read top to bottom: `New` → `Insert` → `store` → `allocSlot` → `Delete` →
`Search` → `scan`/`scanRange` → the `topK` heap at the bottom. The heap
implementation (`push`/`siftUp`/`siftDown`) is a good, self-contained
exercise in translating a textbook algorithm (binary min-heap) into Go —
notice it's *not* using the standard library's `container/heap` interface;
the comments don't say why, but it's a reasonable guess that a
hand-specialized heap avoids the interface-dispatch overhead
`container/heap` incurs on a hot path called millions of times per search.

Then `pkg/core/engine_test.go`, especially `TestConcurrentHammer` — this is
where you'll see `go test -race` mentioned; the **race detector** is a
build mode (`go test -race ./...`) that instruments memory access to catch
exactly the kind of concurrency bug the RWMutex redesign exists to prevent.
Worth running yourself if your platform supports cgo (it didn't on the
Windows dev machine this was built on, which is itself a useful thing to
discover firsthand).

---

## Step 5 — `pkg/core/cascade.go`

Builds directly on Step 4's `Engine` — same receiver, same lock discipline,
same heap type. The new Go concept here is minimal; this step is really
about reading the *algorithm* now that the language mechanics are familiar.
Read the big doc comment on `searchCascade` as a proof, line by line, before
looking at the code below it.

One new small idiom: `var tau float32 = -2` written as
`tau := float32(-2)` with a comment explaining *why* `-2` (below any
achievable cosine similarity, which is bounded in `[-1, 1]`) — a good
example of Go's general house style favoring a value plus a comment over a
named constant when the constant would only ever be used once and the
"why" matters more than the "what."

---

## Step 6 — `pkg/core/snapshot.go`

New territory: file I/O, binary encoding, and Go's `io` interfaces.

**Go concepts you'll meet:**

- **`io.Writer` / `io.Reader`**: the two most important interfaces in the
  Go standard library. Anything with a `Write([]byte) (int, error)` method
  is an `io.Writer`; anything with `Read([]byte) (int, error)` is an
  `io.Reader`. `os.File`, `bufio.Writer`, `hash.Hash32` (via `crc32`), and
  `io.MultiWriter` (which fans one write out to several writers — see
  `w := io.MultiWriter(buf, crc)`, writing to both the buffered file output
  and the running checksum with one call) all satisfy these interfaces,
  which is why `writeTo(f io.Writer)` can be handed an `*os.File` in
  production and a `*bytes.Buffer` in a test with zero code changes. This
  is Go's version of duck typing, but checked at compile time: you never
  declare "`os.File` implements `io.Writer`" anywhere — it just does,
  because the method exists.
- **`encoding/binary`**: `binary.LittleEndian.PutUint32(buf, v)` /
  `.Uint32(buf)` convert between Go integers and their fixed-width
  byte-slice representation. This is how the snapshot format's manual byte
  layout is built — no reflection, no generic serialization library, just
  explicit byte-by-byte control, which is normal for a file format you're
  defining yourself.
- **`defer` for cleanup with error-dependent behavior**:
  `Save`'s `defer func() { if err != nil { ... } }()` is a **closure** — an
  anonymous function that captures the enclosing `err` variable — deferred
  so it can inspect `err`'s *final* value when the function actually
  returns, even though `err` gets reassigned multiple times during the
  function body. This is a step up from the simple `defer f.Close()` you
  saw in Step 4.
- **Named error sentinels + `fmt.Errorf` with `%w`** again, now composed
  with more context: `fmt.Errorf("mindb: snapshot record %d: %w", i, err)`
  wraps a lower-level error with which record failed, while
  `errors.Is(returnedErr, ErrBadChecksum)` still works on the wrapped
  result — trace this through `Load`'s error paths.

`bufio.NewWriterSize` / `bufio.NewReaderSize` — buffering I/O in fixed-size
chunks rather than issuing a syscall per small write/read is a universal
performance idiom, not Go-specific, but worth noting since `1<<20` (a
1 MiB buffer) appears in both `Save` and `Load`.

---

## Step 7 — `pkg/api/grpc_server.go`

Interfaces as contracts, `unsafe` as an escape hatch, and generated code.

**Go concepts you'll meet:**

- **Implementing an interface implicitly**: `Server` never writes
  `implements VectorServiceServer` anywhere. It satisfies that interface
  (defined in the generated `pkg/mindb` package) purely by having methods
  with matching signatures: `Insert`, `Search`, `Delete`, `Snapshot`. Go
  checks this structurally, at the point something requires the interface
  (`mindb.RegisterVectorServiceServer(srv, api.New(...))` in `main.go`) —
  if a method's signature doesn't match exactly, that's a compile error
  there, not in `grpc_server.go`.
- **`unsafe.Pointer` and `unsafe.Slice`**: `float32Vector` uses
  `unsafe.Slice((*float32)(unsafe.Pointer(&tab.Bytes[start])), n)` to
  reinterpret a `[]byte` region as a `[]float32` without copying. `unsafe`
  is a real Go package, and using it means you've stepped outside what the
  compiler and garbage collector can verify for you — the comment
  immediately above explains exactly what makes this particular use safe
  (byte layout matches, bounds are checked first). Treat every `unsafe` use
  you encounter in any Go codebase as "read the surrounding comment
  carefully, this was a deliberate tradeoff."
- **Generated code**: `pkg/mindb/` is produced by the `flatc` compiler from
  `fbs/mindb.fbs` and is treated as read-only — normal Go convention for
  any generated package (protobuf, sqlc, and this repo's own
  `dotint8_avx2_amd64_stub.go` all follow the same rule): never hand-edit,
  regenerate instead. Skim one generated file just to see what "verbose but
  mechanical" generated Go looks like; you don't need to understand it
  deeply.
- **`context.Context`**: every RPC method takes a `context.Context` as its
  first argument (`_ context.Context` here, since this server doesn't use
  it) — the standard Go convention for carrying cancellation signals,
  deadlines, and request-scoped values through a call chain. Seeing it
  ignored (`_`) is common in simple handlers; seeing it threaded through
  and checked (`ctx.Err()`, `select { case <-ctx.Done(): ... }`) is common
  in code doing real long-running work.

---

## Step 8 — `cmd/mindb-server/main.go`

The entry point, and the one file that's `package main` with a `func main()`
— the only function Go calls automatically to start a program.

**Go concepts you'll meet:**

- **`flag` package**: `flag.String("addr", ":50051", "...")` returns a
  `*string`; `flag.Parse()` fills it in from `os.Args`. Standard-library
  CLI flag parsing, no external dependency needed for something this
  simple.
- **`os/signal` + channels**: `signal.Notify(shutdown, os.Interrupt,
  syscall.SIGTERM)` routes OS signals into a Go channel
  (`chan os.Signal`), and `select { case sig := <-shutdown: ... }` blocks
  until one arrives. **Channels** are Go's built-in mechanism for
  goroutines to communicate; `select` waits on multiple channel operations
  at once and proceeds with whichever is ready first — here, racing
  "the server errored" (`errc`) against "someone asked us to stop"
  (`shutdown`).
- **`log.Fatalf`**: logs and calls `os.Exit(1)` — reserved for `main`
  itself; library code (everything in `pkg/`) never calls this, because
  exiting the whole process is a decision only the top-level program should
  make. Notice `pkg/core` and `pkg/api` only ever return `error`, never
  exit.

This is the shortest step conceptually but the best place to see everything
from Steps 1–7 wired together into a running program. After reading it, try
actually running it:

```sh
go run ./cmd/mindb-server -dims 4 -addr :50051
```

and, in another terminal, hitting it with a gRPC client (e.g.
[grpcurl](https://github.com/fullstorydevil/grpcurl), which needs the
FlatBuffers codec configured — or write a tiny Go client using
`pkg/mindb` directly, which is a good exercise in its own right once you've
read Step 7).

---

## Step 9 (optional, advanced) — `pkg/math/avo/asm.go`

Skip this on a first pass; come back once Steps 1–8 feel comfortable. It's
not idiomatic day-to-day Go — it's a **code generator**: a normal Go
program (note `//go:build ignore` — this file is deliberately excluded from
every regular build) that, when run, emits x86 assembly and a matching Go
stub file. Reading it teaches you almost nothing about typical Go
programming, but it's the most unusual and specific-to-this-project file in
the repo, and understanding it end-to-end (ideally alongside
[`DECISIONS.md`](DECISIONS.md#the-avx2-kernel-stage-3) and the "DotInt8 and
the AVX2 kernel" section of [`FEATURES.md`](FEATURES.md)) is a satisfying way to
close the loop on how `DotInt8` actually gets its speed.

---

## After this

At this point you've seen: structs and methods, pointer receivers,
interfaces (implicit), goroutines, channels, `select`, mutexes,
`sync.Pool`, `defer`, closures, error wrapping, build tags, `unsafe`,
generated code, and the standard-library `io`/`encoding/binary`/`flag`/
`os/signal` packages — which covers the large majority of idiomatic Go
you'll meet in any other Go codebase. The two big pieces of the language
this repo doesn't happen to exercise much are **generics** (Go 1.18+ type
parameters — search `go.dev/tour` or the "Generics" section of the Go
spec if you want them) and **channels used as a queue/pipeline** rather
than just a shutdown signal (this repo's one channel use, in `main.go`, is
about as simple as channels get). Worth a dedicated look elsewhere once
you're past this codebase.

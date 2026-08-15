# Interview questions — MinDB

Questions an interviewer is likely to ask about this project, grouped the
way they'd probably come up: broad pitch first, then drill-down by
subsystem. Each question has a short "what to say" pointing at the real
answer in [`DECISIONS.md`](DECISIONS.md) — read that file if any answer
here feels thin, it has the full reasoning and the rejected alternatives.

---

## The pitch (have this cold, 60 seconds)

**Q: What is MinDB and why did you build it?**
A sidecar vector database that does exact k-NN search, not approximate —
the headline claim is that it returns bit-identical results to a brute-force
float32 scan while being several times faster, using a mathematically
provable pruning technique rather than the "quantize, oversample, hope"
approach most vector DBs use. Built to learn: Go, SIMD/assembly, and the
actual math behind vector search rather than just calling a library.

**Q: How is this different from Pinecone/Qdrant/Weaviate/pgvector?**
Those are ANN (approximate nearest neighbor) — they trade recall for speed
and you tune a knob to get it back. MinDB's cascade is *exact*: a vector is
only pruned when it's mathematically impossible for it to be in the top-k
(Cauchy-Schwarz bound), so recall is always 100% by construction, not
something you measure and hope holds in production. The tradeoff is scale —
this targets ~100k vectors on one box, not billions across a cluster.

**Q: What would you do differently / what's missing?**
Be ready to name stage 4 (RaBitQ-style rotated 1-bit quantization, deferred
as research-risk — plain sign-bit quantization was tried and rejected for
poor recall on clustered data, see DECISIONS.md), the fact it's single-node
with no sharding/replication story, and that "zero-copy" only applies to
the read path, not response construction (see wire protocol section below).
Naming your own project's limits unprompted reads well.

---

## System design

**Q: Walk me through what happens on a `Search` call.**
Lock (`RLock`), then either: a plain parallel float32 scan (small corpora or
no SIMD available), or the three-pass cascade — pass 1 scores every vector's
int8 code cheaply and gets a two-sided bound via the residual norm, pass 2
keeps only vectors whose bound overlaps the current top-k threshold, pass 3
exactly rescores just the survivors in float32. Merge into a bounded min-heap,
sort by score then by external ID for deterministic output, return.

**Q: Why exact rather than approximate? Isn't ANN what everyone uses in
production?**
Yes, and there's a real argument for ANN at billion-vector scale where no
exact method is fast enough at any cost. This project deliberately targets
a smaller regime (~100k vectors, a sidecar not a cluster) where "exact and
fast" is actually achievable, and treats recall as a property you can prove
rather than benchmark-and-hope. Know this is a scale tradeoff, not a claim
that exact always beats approximate.

**Q: How would this need to change to handle 10M+ vectors?**
Honest answer: it wouldn't, not without a different architecture — sharding
across nodes, a different index structure (IVF/HNSW-style) layered on top,
and probably giving up exactness for that regime. Good place to talk about
where the current design's assumptions (single RWMutex, everything resident
in RAM, preallocated capacity) stop holding.

**Q: Why no sharding / replication / clustering?**
Scope: it's a sidecar for one service's vector needs, not a standalone
distributed system. Explicitly out of scope, not an oversight — worth
saying that plainly rather than letting it sound like a gap you didn't
notice.

---

## Concurrency

**Q: How do you handle concurrent reads and writes?**
One `sync.RWMutex` over the whole engine — `RLock` for `Search` (shared,
so concurrent searches run fully in parallel), `Lock` for `Insert`/`Delete`.
See [`DECISIONS.md#concurrency`](DECISIONS.md#concurrency).

**Q: Isn't a single global lock a bottleneck?**
`RLock`/`RUnlock` costs ~20ns; a search costs ~1-6ms. The lock is a rounding
error next to the work it protects — there's no measurable win from
anything fancier at this scale. This is the "know your actual numbers before
optimizing" answer, and it's a good one because it's backed by real
measurement, not assumption.

**Q: Did you ever try anything lock-free?**
Yes — this is the best story in the whole project, tell it in full. First
version used tombstoning + RCU-style compaction. It shipped three real bugs:
(1) a plain Go map (`idMap`) hit `concurrent map read and map write` — an
unrecoverable runtime *throw*, not a catchable panic; (2) `Insert` wrote the
vector then flipped a `live` bit with no memory barrier, so a concurrent
reader could see a half-written vector and return a wrong-but-plausible
score, silently; (3) background compaction swapped a pointer while writes
still landed on the old copy mid-swap, losing them — because real RCU
promises lock-free *readers*, never lock-free *writers*, and the design had
quietly assumed otherwise. Also discovered compaction was pointless: capacity
is preallocated, so it never reclaimed memory, it just made the live set
denser — a whole background goroutine and a 2x memory spike to solve a
problem a ten-line free list solves with zero concurrency risk. This is a
great "I was wrong, here's how I found out, here's what I learned" story —
interviewers like it more than a story where nothing went wrong.

**Q: What replaced the free-list problem RCU was (accidentally) solving?**
`Delete` pushes the freed slot index onto `e.free []uint32`; `Insert` pops
from it before growing `highWater`. Consequence: a vector's *slot* index
isn't stable across delete+reinsert, but its external ID is — that
distinction matters for tie-breaking (see below) and for snapshot reload.

**Q: How did you test the concurrency story?**
`go test -race` with a test that hammers concurrent Insert/Delete/Search
(`TestConcurrentHammer`). Also worth mentioning honestly: the race detector
needs cgo, which wasn't available on the Windows dev machine, so know how
you'd actually validate this (Linux CI, WSL) rather than claiming it always
ran locally.

**Q: Why break ties on slot index during scan but external ID at the end?**
Parallel scan means candidates with equal scores can arrive in different
orders depending on `GOMAXPROCS` — the internal tiebreak exists purely to
make the heap deterministic so the differential test (which compares scores
exactly) isn't flaky. The final sort uses external ID because slot indices
aren't stable across a snapshot reload, but IDs are — different tiebreak,
different job.

---

## Memory layout

**Q: How is data actually stored — array of structs or struct of arrays?**
Struct-of-arrays: `Engine` holds parallel slices (`vectors`, `codes`,
`scales`, `residuals`, `externalID`, `payloads`), not one `[]Vector`. A scan
only touches `vectors` (or the int8 arrays) — never ID or payload bytes
until top-k is already decided — so every cache line the hot loop pulls in
is 100% useful data, not padding it doesn't need yet.

**Q: What did that cost you?**
More bookkeeping to keep in sync — `live`, `idMap`, `free`, `highWater` all
have to agree, and `Delete` touches several slices instead of one. Worth it
because the scan is the thing being optimized; deletes are rare and cheap
regardless.

**Q: Why normalize every vector to unit length at insert?**
If `‖v‖ = ‖q‖ = 1`, cosine similarity *is* the dot product — no division, no
magnitude array, no extra random-access load in the hot loop. It's also a
precondition of the cascade's error bound: Cauchy-Schwarz gives
`|q·(v−v̂)| ≤ ‖q‖·ρ`, which only collapses to `≤ ρ` because `‖q‖ = 1`. Good
one to draw on a whiteboard if asked.

**Q: Why a hand-rolled min-heap instead of `container/heap`?**
`container/heap` needs interface dispatch (`Len`, `Less`, `Swap` as method
calls through an interface) on a path called millions of times per search;
a specialized heap avoids that overhead. Pooled per-worker via `sync.Pool`
and merged after a parallel scan.

---

## The cascade (search algorithm)

**Q: Explain the bound-and-refine cascade like I've never heard of it.**
Every insert stores three things: the exact float32 vector, an int8
quantized "code" (a compressed approximation), and a residual norm
`ρ = ‖v − v̂‖` — literally "how wrong is the compressed version." A search
scores every vector's cheap int8 code first, then uses `ρ` to compute a
hard lower and upper bound on what the *true* float32 score could possibly
be. If a vector's upper bound can't beat the current top-k's worst score,
it's mathematically eliminated — not "probably not in the top-k," but
provably not. Only the survivors get exactly rescored in float32.

**Q: How do you know it's actually exact and not just "usually right"?**
`TestCascadeIsExact` in `pkg/core/cascade_test.go` — a differential test
that asserts the cascade returns exactly what brute force returns, IDs and
scores, across thousands of random queries. Plus a separate bound-validity
test asserting `lo_i ≤ true_score_i ≤ hi_i` for every vector — if that ever
fails the whole exactness claim is void, so it's tested directly, not
inferred.

**Q: Why quantize only the stored vectors and not the query too?**
Asymmetric scoring. An earlier version quantized both sides (symmetric),
which turned out unsound — accounting for error in *both* operands gives a
much looser bound, and the first attempt at deriving it was actually wrong.
Keeping the query in float32 means the only error term is the stored
vector's own residual — a measurably tighter bound (4.3x), and a bonus: the
SIMD kernel only needs int8→float32 conversion + multiply-accumulate
(AVX2+FMA3), not true int8×int8 multiply (which needs AVX-512 VNNI, far
less universally available).

**Q: Is the residual `ρ` computed or derived?**
Measured directly, one extra `O(dims)` pass at insert time. An earlier
version derived it as `‖v̂‖ = √(1 − ρ²)`, assuming the quantization
residual is orthogonal to the reconstruction — int8 rounding isn't an
orthogonal projection, so that's only approximate, and an approximate bound
isn't a proof. Cost is paid once at insert, never at query time.

**Q: What if quantization doesn't prune much on some dataset?**
Guard fallback: if pass-2 survivors exceed 25% of the corpus, fall back to
a plain parallel float32 scan — because scattered rescoring (pass 3) runs
serially out-of-order and can cost more than a full ordered scan once
survivors get large. The 25% threshold came from a benchmark that found the
actual crossover around 49%; both paths return identical results, so this
constant only risks speed, never correctness. In practice real clustered
data prunes to ~0.4% survivors — two orders of magnitude under the guard.

**Q: When does the cascade not run at all?**
When `math.HasFastInt8()` is false — pure-Go int8 dot products plateau
around 0.83 GMAC/s (Go can't emit the packed multiply-accumulate that makes
int8 arithmetic pay off), so without SIMD the cascade path would be slower
than just scanning float32 directly. It's a measured decision, not a
guess.

---

## Quantization math

**Q: What's actually being quantized, and how?**
Per-vector int8 codes with a per-vector float scale — not a single global
scale across the whole corpus, so each vector gets full int8 dynamic range
regardless of the overall data distribution.

**Q: Why not 4-bit or 1-bit quantization for more compression?**
Both tried and measured, not assumed. 4-bit: the error bound is technically
valid but wide enough it prunes almost nothing, so you pay for a full scan
*and* a full rescore — worse than just scanning float32 once. Plain 1-bit
(sign bit): fastest possible scan, but recall collapses on clustered data
(recall@10 ≈ 0.63 even with a 1000-candidate shortlist) because vectors
sharing a cluster also share most sign bits, so Hamming distance can't
discriminate where the top-k actually lives. Deferred to a future rotated
(RaBitQ-style) approach rather than shipped broken — this is stage 4 on the
roadmap, explicitly marked research-risk.

---

## The AVX2 kernel / SIMD

**Q: Why hand-generate assembly instead of hoping the Go compiler
autovectorizes?**
Measured Go's ceiling first: pure-Go int8 dot product plateaus at ~0.83
GMAC/s across every unroll factor tried — Go doesn't emit the packed
multiply-accumulate instruction that makes int8 arithmetic pay off. On the
*float32* path a plain unrolled Go loop already hits ~13 GB/s against an
~11.7 GB/s memory subsystem — already memory-bound, so assembly there would
buy nothing. Only wrote assembly where profiling proved Go was leaving real
performance on the table.

**Q: Why Avo instead of hand-written Plan9 assembly?**
Hand-written assembly is easy to get subtly wrong (register allocation,
calling convention, stack frame size) and hard to review. Avo is a Go DSL
that generates both the `.s` file and a matching Go stub, so the actual
source of truth is normal, readable Go — and the generated files are
committed artifacts, regenerated via `go run` when the generator changes.

**Q: Why AVX2 rather than AVX-512?**
Availability: AVX2 has been on every x86-64 chip since Haswell (2013) /
Zen (2017); AVX-512 is missing on most consumer Zen ≤3 parts, disabled on
Intel's hybrid consumer lineups, and downclocks on several server parts.
Also: because scoring is asymmetric (query stays float32), the kernel never
needs true int8×int8 multiply — the operation AVX-512 VNNI exists for.
AVX2's `VPMOVSXBD` (sign-extend int8→int32) + `VCVTDQ2PS` (→float32) +
`VFMADD231PS` (multiply-accumulate) covers everything needed.

**Q: How do you handle CPUs without AVX2?**
Runtime detection via `golang.org/x/sys/cpu` in an `init()` function —
checked once at startup (`HasAVX2 && HasFMA && HasAVX`), swaps a function
variable (`dotInt8Impl`) to the generated kernel if true, otherwise it stays
pointed at the pure-Go fallback. Non-amd64 platforms use build tags
(`kernel_generic.go`, `//go:build !amd64`) so there's zero runtime branching
cost and it's correct everywhere, fast where hardware allows.

**Q: Why `init()` and not a package-level `const`/`var` initializer?**
CPU feature flags are runtime values, not compile-time constants — you
can't know at compile time what CPU the binary will run on, so the check
has to happen when the program actually starts, which is exactly what
`init()` is for.

**Q: How did you validate the generated assembly is actually correct?**
A differential test comparing the AVX2 kernel against the pure-Go reference
across 17 dimension test cases specifically chosen to hit block-size
boundaries (SIMD lanes process 8 elements at a time — the interesting bugs
live at "dims not a multiple of 8," not the common case).

**Q: What throughput did you actually measure, and why does that number
matter?**
~23.5 GB/s at 768 dims — above this machine's ~11.7 GB/s memory bandwidth
ceiling for the float32 path, meaning the int8 scan is now bandwidth-bound
rather than compute-bound, which was the entire point of writing the
assembly. On `BenchmarkSearchPaths` (N=20,000, dims=768): brute force
6.47ms vs cascade+AVX2 1.75ms — a measured 3.7x, not a projection. Always
have "measured, not projected" ready as a follow-up.

**Q: Why is Avo a `tool` dependency and not a normal `require`?**
The generator file (`pkg/math/avo/asm.go`) has `//go:build ignore` so it's
excluded from every normal build (otherwise you'd get two `package main`
files fighting in one directory). Because nothing in the buildable graph
imports it, `go mod tidy` would silently drop it from `go.mod`, breaking
regeneration for the next person. Go's `tool` directive pins a dependency
that only `go generate`/`go run` needs, never `go build` — exactly this
case.

---

## Durability / snapshots

**Q: How do you make a snapshot write crash-safe?**
`tmp → fsync(file) → rename → fsync(parent dir)`. The last step is the one
most homegrown atomic-write code skips: without fsyncing the directory,
`rename()` isn't guaranteed durable — a crash right after rename can leave
the directory entry pointing at the old file, or nowhere, even though the
new file's bytes are safely on disk.

**Q: Does that hold on every OS you've tested?**
Documented honestly rather than assumed: Windows has no directory-fsync
equivalent, so `syncDir` is a no-op there. Development happened on Windows;
production doesn't have to run there. Good example of "know the limits of
your own guarantee" rather than claiming portability you haven't verified.

**Q: Are the int8 codes persisted, or recomputed?**
Recomputed on load — only the float32 vector is written to disk. Persisting
codes would grow the file ~25% to save a re-quantization pass that's noise
next to reading hundreds of MB off disk, and it decouples the on-disk
format from the quantization scheme, so changing how codes are built later
doesn't invalidate every snapshot already written.

**Q: Why does `Load` call an internal `store()` instead of the public
`Insert()`?**
`Insert` normalizes its input; a snapshot's vectors are already normalized
(from the original insert). Re-normalizing float32-rounded data computes a
norm of `1 ± 1e-7` rather than exactly 1, and dividing by that shifts the
vector's low bits — so scores after a reload would subtly differ from
scores before it. A snapshot round-trip should be invisible to a caller;
that's the bug this fixed.

**Q: How is corruption detected?**
Checksum (CRC32) written alongside each record; a flipped byte on load is
rejected rather than silently trusted. Tested directly — a test flips a
byte in a written snapshot file and asserts the load fails with
`ErrBadChecksum`.

---

## Wire protocol / API

**Q: Why FlatBuffers over gRPC instead of plain protobuf?**
Enables an actually zero-copy *read* path — see next question — which
protobuf's generated accessors don't give you as directly.

**Q: You say "zero-copy," what does that actually mean here, precisely?**
Be precise, this is a good one to over-explain rather than hand-wave. The
generated FlatBuffers accessor reads one `float32` per call through
bounds-checked offset arithmetic — correct but still an element-by-element
copy. `float32Vector` instead validates the vector region once, then
reinterprets the wire buffer directly as a `[]float32` via `unsafe.Slice` —
safe specifically because FlatBuffers' wire format is already little-endian
4-byte-aligned float32, exactly the in-memory layout of a `[]float32` on a
little-endian platform. Honesty matters here: it's zero-copy for *reading a
request*, not for building a response — gRPC still marshals whatever a
handler returns, and constructing a response `Builder` still allocates.
Claiming the whole round-trip is zero-copy would be wrong; know which half
actually is.

**Q: What if you ran this on a big-endian platform?**
There's an explicit `nativeLittleEndian` runtime check guarding a
byte-swapping fallback — currently untested in practice since Go's
amd64/arm64 targets are all little-endian, but the code doesn't silently
assume little-endian and break there; it checks.

**Q: Why is `grpc.ForceServerCodec` mandatory rather than optional
configuration?**
The handlers return `*flatbuffers.Builder`, not a generated protobuf
struct — gRPC's default codec can't marshal that. Without registering the
FlatBuffers codec, this fails at *request time*, not compile time, so it's
the kind of misconfiguration that passes every test that constructs the
server directly in-process and only breaks the first time something talks
to it over the wire. Called out explicitly at the `grpc.NewServer(...)`
call site for exactly that reason.

**Q: Are batch inserts transactional?**
No, deliberately not. No transaction log or rollback mechanism — if vector
5 of a 10-vector insert fails validation, vectors 0-4 stay stored, and the
RPC returns an error naming which vector failed and how many were stored
before it. Building real rollback would need machinery this sidecar's scale
target doesn't need; documenting the real (non-transactional) behavior was
cheaper and more honest than half-building atomicity.

---

## Testing philosophy

**Q: What's your testing strategy at a high level?**
Differential testing is the centerpiece — the cascade's whole value
proposition is "identical to brute force," so the test that matters most
directly checks that, across thousands of random queries, not just a
handful of hand-picked cases. Below that: `go test -race` for concurrency,
a snapshot round-trip test plus a deliberately corrupted file, and
benchmarks that reproduce the specific performance numbers claimed in the
docs (`BenchmarkSearchPaths`, `BenchmarkGuardCrossover`) so the numbers in
`DECISIONS.md` are reproducible, not just asserted.

**Q: How do you validate pruning-ratio claims aren't just synthetic-data
artifacts?**
`make validate` runs against real embedding datasets (`glove-100-angular`,
`gist-960-euclidean`, normalized) bracketing the 768-dim target, rather than
trusting synthetic clustered data alone — synthetic data is a reasonable
proxy for iterating quickly, but it's explicitly not treated as evidence on
its own.

---

## Go-language questions (if the interview is Go-specific)

- **Why struct-of-arrays instead of `[]Vector`?** — cache-line efficiency,
  see memory layout above.
- **Why pointer receivers everywhere on `*Engine`?** — if any method needs
  to mutate state (nearly all of them do here), keep every method on the
  same receiver type for consistency; mixing value/pointer receivers on one
  type is a common Go footgun.
- **What's `dotInt8Impl`?** — a package-level function-value variable,
  Go's version of a strategy pattern without needing an interface for a
  single-function swap; reassigned once in `init()` based on runtime CPU
  detection.
- **Why build tags (`//go:build amd64` / `!amd64`) instead of a runtime
  `if`?** — zero runtime branching cost, and it lets platform-specific code
  simply not exist in a binary that doesn't need it (the AVX2 assembly
  isn't even present in a non-amd64 build).
- **Where does `unsafe` show up and why is it safe there?** — the
  FlatBuffers zero-copy read; know the specific invariant that makes it
  safe (see wire protocol above) rather than just "it's fast."
- **`sync.Pool` vs the `free []uint32` list — what's the difference in
  guarantee?** — a `sync.Pool` can be cleared by the GC at any time, so it's
  a cache, never something correctness depends on; the free list is real
  engine state the allocator logic actually relies on.

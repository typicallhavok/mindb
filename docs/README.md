# MinDB

> A zero-allocation, SIMD-accelerated, in-memory vector engine for Go.

MinDB is a highly specialized, embedded vector database designed to act as a high-performance gRPC (with FlatBuffers) sidecar for microservices. It intentionally caps its scale to guarantee sub-millisecond, brute-force exact k-NN searches by leveraging raw hardware sympathy and CPU-level optimizations.

## Core Features

* **Algorithm:** Exact k-NN (Brute-force Flat Index).
* **Math:** Cosine Similarity with **dynamic dimensions** (configured at startup).
* **Capacity:** Hard-capped at **100,000 vectors** (enabling ultra-fast `uint32` addressing).
* **Mutability:** Supports logical deletes via **Tombstoning** with background memory compaction.
* **Metadata:** Returns arbitrary byte-slice **Payloads** on search.
* **Durability:** Purely in-memory operation with support for **Disk Snapshots** to persist and restore states.
* **Zero-Allocation:** Query path utilizes `sync.Pool` to ensure 0 heap allocations, bypassing the Go Garbage Collector entirely.

---

## 🧠 Memory Architecture (Struct-of-Arrays)

To maintain strict L1/L2 CPU cache sympathy, MinDB completely decouples vectors from their metadata. Instead of traditional objects, vectors are packed into a single continuous float slice.

type Engine struct {
    // --- 1. The Math Core ---
    // A single, massive 1D array of length (100,000 * dimensions)
    vectors []float32 
    // Pre-computed magnitudes for Cosine Sim to save CPU cycles during search
    magnitudes []float32 

    // --- 2. The Conductor ---
    // Logical deletes: if tombstones[internalID] == true, skip during search
    tombstones []bool 
    
    // --- 3. Metadata & Translation ---
    // Maps internal uint32 (0 to 99,999) to external string UUIDs and payloads
    externalIDs []string
    payloads    [][]byte
    
    // Fast lookup for deletes/updates: UUID -> internal uint32
    idMap map[string]uint32
}

---

## 📡 The gRPC + FlatBuffers Contract

MinDB operates over an HTTP/2 gRPC connection, ensuring ultra-low latency multiplexing across microservices. To avoid Protobuf serialization overhead, the payload is defined using **FlatBuffers**, allowing zero-copy networking.

```flatbuffers
namespace mindb;

table Vector {
  id: string;
  values: [float32];
  payload: [ubyte];
}

table InsertRequest {
  vectors: [Vector];
}

table InsertResponse {
  inserted_count: int32;
}

table SearchRequest {
  query_vector: [float32];
  top_k: int32;
}

table SearchResult {
  id: string;
  score: float32;
  payload: [ubyte];
}

table SearchResponse {
  results: [SearchResult];
}

table DeleteRequest {
  ids: [string];
}

table DeleteResponse {
  deleted_count: int32;
}

table SnapshotRequest {}
table SnapshotResponse {
  success: bool;
  message: string;
}

rpc_service VectorService {
  Insert(InsertRequest): InsertResponse;
  Search(SearchRequest): SearchResponse;
  Delete(DeleteRequest): DeleteResponse;
  Snapshot(SnapshotRequest): SnapshotResponse;
}
```

---

## ⚙️ Engineering Deep Dives

### 1. SIMD Cosine Similarity
Cosine similarity requires computing the dot product and the magnitudes: (A · B) / (||A|| ||B||). 
To handle dynamic dimensions at blazing speeds, MinDB pre-calculates the magnitude of vectors during the `Insert` phase. During `Search`, highly optimized AVX-512 Assembly routines (`.s` files) process float blocks in 512-bit chunks to calculate the dot product, followed by a scalar tail for remainder dimensions.

### 2. Lockless Concurrency & Tombstoning
Vectors are not shifted in memory when deleted. The ID is translated via `idMap`, and a boolean flag is flipped in the `tombstones` array. The search loop simply bypasses tombstoned indices. A background goroutine periodically checks tombstone volume and, if necessary, allocates a compacted engine state and atomically swaps the pointer via Read-Copy-Update (RCU).

### 3. Atomic Disk Snapshots
Since internal state relies heavily on primitive contiguous arrays (`[]float32`), MinDB performs nearly instantaneous snapshots by dumping the raw binary slices. To prevent corruption from power loss or crashes during this process, snapshots utilize a **Write-Rename** atomic pattern. The state is first flushed to a temporary file (`snapshot.tmp`), synced to physical disk (`fsync`), and finally atomically renamed over the existing snapshot file.

---

## 📂 Project Structure

mindb/
├── cmd/
│   └── mindb-server/
│       └── main.go           # Entry point: boots gRPC server and loads Snapshot
├── pkg/
│   ├── api/
│   │   └── grpc_server.go    # Implements the FlatBuffers gRPC interface
│   ├── core/
│   │   ├── engine.go         # sync.Pool, RCU concurrency, Tombstone logic
│   │   └── snapshot.go       # Disk serialization (encoding/gob or raw I/O)
│   └── math/
│       ├── distance.go       # Go wrappers for assembly calls
│       ├── distance_amd64.s  # AVX-512 Assembly for Cosine Sim
│       └── distance_test.go  # Strict benchmark tests
├── fbs/
│   └── mindb.fbs             # The FlatBuffers gRPC contract
├── go.mod
└── Makefile                  # Commands for flatc (grpc plugin) generation and building
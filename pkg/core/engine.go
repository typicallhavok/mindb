package core

// Engine represents the MinDB core in-memory vector store
type Engine struct {
	vectors      []float32
	magnitudes   []float32
	tombstones   []bool
	externalIDs  []string
	payloads     [][]byte
	idMap        map[string]uint32
}

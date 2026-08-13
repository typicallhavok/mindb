//go:build amd64

package math

// Placeholder until stage 3 lands the generated AVX2 kernel, at which point
// these become runtime CPU-feature checks.
const (
	hasFastInt8 = false
	kernelName  = "pure-go"
)

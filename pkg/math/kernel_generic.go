//go:build !amd64

package math

// No SIMD int8 kernel on this architecture, so DotInt8 is the pure-Go loop and
// the cascade stays off. Correct, just not fast.
const (
	hasFastInt8 = false
	kernelName  = "pure-go"
)

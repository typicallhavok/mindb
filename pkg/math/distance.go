// Package math provides the vector kernels on MinDB's hot path.
//
// Everything here assumes unit-normalized vectors, which is what lets cosine
// similarity collapse into a plain dot product. See docs/ARCHITECTURE.md.
package math

import stdmath "math"

// Dot returns the dot product of a and b, which must be the same length.
//
// The loop is unrolled by eight with independent accumulators. The unrolling is
// not about instruction count: a single accumulator forms a serial dependency
// chain, and every add must wait on the previous one's latency. Eight
// accumulators give the out-of-order engine eight independent chains to
// interleave, which is where the ~1.7x over naive scalar comes from.
//
// The (*[8]float32)(s[i:]) conversions let the compiler prove the eight indexed
// loads are in bounds once per iteration rather than eight times.
//
// Float addition is not associative, so the accumulator order below is part of
// the contract: change it and results shift in the last bits. The cascade's
// differential test compares scores exactly, so this matters.
func Dot(a, b []float32) float32 {
	if len(a) != len(b) {
		panic("mindb/math: Dot on vectors of unequal length")
	}

	var s0, s1, s2, s3, s4, s5, s6, s7 float32
	i := 0
	for ; i+8 <= len(a); i += 8 {
		x := (*[8]float32)(a[i:])
		y := (*[8]float32)(b[i:])
		s0 += x[0] * y[0]
		s1 += x[1] * y[1]
		s2 += x[2] * y[2]
		s3 += x[3] * y[3]
		s4 += x[4] * y[4]
		s5 += x[5] * y[5]
		s6 += x[6] * y[6]
		s7 += x[7] * y[7]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return ((s0 + s1) + (s2 + s3)) + ((s4 + s5) + (s6 + s7))
}

// Norm returns the Euclidean length of v.
//
// The accumulation is float64 because this feeds Normalize, and a float32 sum
// over 768 squared terms loses enough precision to visibly skew the unit length
// of the result. Normalization is a precondition of the Cauchy-Schwarz bound in
// the cascade, so drift here weakens a correctness guarantee rather than just a
// score.
func Norm(v []float32) float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return float32(stdmath.Sqrt(sum))
}

// Normalize scales v in place to unit length and returns its original norm.
//
// A zero vector is left untouched and returns 0; the caller decides whether that
// is an error. Dividing would produce NaNs that then propagate silently through
// every subsequent score.
func Normalize(v []float32) float32 {
	n := Norm(v)
	if n == 0 {
		return 0
	}
	inv := 1 / n
	for i := range v {
		v[i] *= inv
	}
	return n
}

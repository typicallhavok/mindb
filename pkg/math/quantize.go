package math

import stdmath "math"

// Quantize encodes a unit-length vector v into int8 codes and returns the scale
// and the residual norm.
//
// The reconstruction is v̂ᵢ = codeᵢ × scale, and the residual norm is
// ρ = ‖v − v̂‖. Those two values are what make the cascade's pruning provable
// rather than heuristic: with a float32 query, Cauchy-Schwarz gives
//
//	|q·v − q·v̂|  =  |q·(v − v̂)|  ≤  ‖q‖·ρ  =  ρ     (since ‖q‖ = 1)
//
// so q·v̂ ± ρ is a hard two-sided bound on the true score.
//
// ρ is measured directly rather than derived from the scale. An earlier version
// of this design computed ‖v̂‖ = √(1−ρ²), which assumes the residual is
// orthogonal to the reconstruction; int8 rounding is not an orthogonal
// projection, so that identity is merely approximate — and "approximate" voids a
// proof. Measuring costs one extra pass at insert and nothing at query time.
//
// code must have the same length as v.
func Quantize(v []float32, code []int8) (scale, residual float32) {
	if len(code) != len(v) {
		panic("mindb/math: Quantize code buffer length does not match vector")
	}

	var maxAbs float32
	for _, x := range v {
		if x < 0 {
			x = -x
		}
		if x > maxAbs {
			maxAbs = x
		}
	}
	if maxAbs == 0 {
		for i := range code {
			code[i] = 0
		}
		return 0, 0
	}

	// 127, not 128: the range stays symmetric, so negative and positive
	// components round identically and the reconstruction has no bias.
	scale = maxAbs / 127
	inv := 1 / scale

	var sq float64
	for i, x := range v {
		q := int32(stdmath.Round(float64(x * inv)))
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		code[i] = int8(q)

		d := float64(x) - float64(int32(code[i]))*float64(scale)
		sq += d * d
	}
	return scale, float32(stdmath.Sqrt(sq))
}

// DotInt8 returns the dot product of a float32 query with an int8 code vector,
// before the code's scale is applied.
//
// This is the asymmetric half of the cascade: the query is never quantized, so
// the only error term is the database vector's residual. That makes the bound
// 4.3x tighter than quantizing both sides, and it means the SIMD kernel needs
// only AVX2 rather than AVX-512 VNNI.
//
// This pure-Go version is the correctness oracle and the fallback for platforms
// without AVX2. It is deliberately slower than the float32 path — Go cannot emit
// the instruction that makes int8 fast, so the cascade does not pay off until
// stage 3 replaces this with generated assembly.
func DotInt8(q []float32, code []int8) float32 {
	if len(q) != len(code) {
		panic("mindb/math: DotInt8 on vectors of unequal length")
	}

	var s0, s1, s2, s3, s4, s5, s6, s7 float32
	i := 0
	for ; i+8 <= len(q); i += 8 {
		x := (*[8]float32)(q[i:])
		y := (*[8]int8)(code[i:])
		s0 += x[0] * float32(y[0])
		s1 += x[1] * float32(y[1])
		s2 += x[2] * float32(y[2])
		s3 += x[3] * float32(y[3])
		s4 += x[4] * float32(y[4])
		s5 += x[5] * float32(y[5])
		s6 += x[6] * float32(y[6])
		s7 += x[7] * float32(y[7])
	}
	for ; i < len(q); i++ {
		s0 += q[i] * float32(code[i])
	}
	return ((s0 + s1) + (s2 + s3)) + ((s4 + s5) + (s6 + s7))
}

package math

import (
	stdmath "math"
	"math/rand"
	"testing"
)

func unitVec(r *rand.Rand, dims int) []float32 {
	v := randVec(r, dims)
	if Normalize(v) == 0 {
		v[0] = 1
		Normalize(v)
	}
	return v
}

// reconstruct rebuilds v̂ from codes and scale.
func reconstruct(code []int8, scale float32) []float32 {
	out := make([]float32, len(code))
	for i, c := range code {
		out[i] = float32(c) * scale
	}
	return out
}

func TestQuantizeResidualIsExact(t *testing.T) {
	// The residual is the whole proof. If the stored value ever understates
	// the true reconstruction error, the bound is unsound and the cascade
	// silently drops correct answers.
	r := rand.New(rand.NewSource(1))

	for _, dims := range []int{1, 7, 8, 9, 64, 768} {
		for trial := 0; trial < 50; trial++ {
			v := unitVec(r, dims)
			code := make([]int8, dims)
			scale, rho := Quantize(v, code)

			vhat := reconstruct(code, scale)
			var sq float64
			for i := range v {
				d := float64(v[i]) - float64(vhat[i])
				sq += d * d
			}
			want := stdmath.Sqrt(sq)

			if stdmath.Abs(float64(rho)-want) > 1e-6 {
				t.Fatalf("dims=%d: residual %v, true ‖v-v̂‖ %v", dims, rho, want)
			}
		}
	}
}

// TestBoundHoldsForRandomQueries is the property the exactness guarantee rests
// on: the true score must always land inside [q·v̂ − ρ, q·v̂ + ρ].
func TestBoundHoldsForRandomQueries(t *testing.T) {
	const dims = 768
	r := rand.New(rand.NewSource(2))

	var worstSlack float64
	for trial := 0; trial < 2000; trial++ {
		v := unitVec(r, dims)
		q := unitVec(r, dims)

		code := make([]int8, dims)
		scale, rho := Quantize(v, code)

		approx := DotInt8(q, code) * scale
		trueScore := Dot(q, v)

		lo := approx - rho
		hi := approx + rho

		// A tiny epsilon absorbs float32 summation error in approx itself,
		// which is separate from the quantization error the bound covers.
		const eps = 1e-5
		if float64(trueScore) < float64(lo)-eps || float64(trueScore) > float64(hi)+eps {
			t.Fatalf("trial %d: true score %v outside bound [%v, %v]", trial, trueScore, lo, hi)
		}
		if slack := float64(hi - lo); slack > worstSlack {
			worstSlack = slack
		}
	}

	// A bound that is always valid but enormously wide would be useless: it
	// would prune nothing. Catch that regression here rather than in a
	// benchmark nobody reads.
	if worstSlack > 0.2 {
		t.Errorf("widest bound was %v; pruning will be ineffective", worstSlack)
	}
	t.Logf("widest bound width over 2000 trials: %.6f", worstSlack)
}

func TestQuantizeReconstructionIsClose(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for trial := 0; trial < 100; trial++ {
		v := unitVec(r, 768)
		code := make([]int8, 768)
		_, rho := Quantize(v, code)

		// int8 over a unit vector should land well under 1% error; if this
		// regresses, the scale calculation is wrong.
		if rho > 0.02 {
			t.Fatalf("trial %d: residual norm %v is too large for int8", trial, rho)
		}
	}
}

func TestQuantizeUsesFullCodeRange(t *testing.T) {
	// The component with the largest magnitude must map to ±127, or the code
	// range is being wasted and the residual is larger than it needs to be.
	r := rand.New(rand.NewSource(4))
	v := unitVec(r, 256)
	code := make([]int8, 256)
	Quantize(v, code)

	var maxCode int8
	for _, c := range code {
		if c > maxCode {
			maxCode = c
		}
		if -c > maxCode {
			maxCode = -c
		}
	}
	if maxCode != 127 {
		t.Errorf("largest code magnitude is %d, want 127", maxCode)
	}
}

func TestQuantizeZeroVector(t *testing.T) {
	v := make([]float32, 16)
	code := make([]int8, 16)
	scale, rho := Quantize(v, code)
	if scale != 0 || rho != 0 {
		t.Errorf("zero vector: scale %v, residual %v, want 0, 0", scale, rho)
	}
	for i, c := range code {
		if c != 0 {
			t.Errorf("code[%d] = %d, want 0", i, c)
		}
	}
}

func TestDotInt8MatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for _, dims := range []int{0, 1, 7, 8, 9, 17, 768, 769} {
		q := randVec(r, dims)
		code := make([]int8, dims)
		for i := range code {
			code[i] = int8(r.Intn(255) - 127)
		}

		var want float64
		for i := range q {
			want += float64(q[i]) * float64(code[i])
		}
		got := float64(DotInt8(q, code))

		tol := 1e-4 * float64(dims+1)
		if stdmath.Abs(got-want) > tol {
			t.Fatalf("dims=%d: DotInt8 = %v, reference = %v", dims, got, want)
		}
	}
}

func TestDotInt8PanicsOnLengthMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("DotInt8 did not panic on mismatched lengths")
		}
	}()
	DotInt8(make([]float32, 8), make([]int8, 9))
}

func TestQuantizePanicsOnLengthMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Quantize did not panic on mismatched lengths")
		}
	}()
	Quantize(make([]float32, 8), make([]int8, 9))
}

func BenchmarkDotInt8(b *testing.B) {
	r := rand.New(rand.NewSource(6))
	for _, dims := range []int{128, 768} {
		q := randVec(r, dims)
		code := make([]int8, dims)
		for i := range code {
			code[i] = int8(r.Intn(255) - 127)
		}
		b.Run(itoa(dims), func(b *testing.B) {
			b.SetBytes(int64(dims * 5)) // 4 bytes of query + 1 of code
			for i := 0; i < b.N; i++ {
				sink = DotInt8(q, code)
			}
		})
	}
}

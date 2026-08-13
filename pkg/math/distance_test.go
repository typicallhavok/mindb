package math

import (
	stdmath "math"
	"math/rand"
	"testing"
)

// naiveDot is the reference implementation. Deliberately the dumbest possible
// loop: it is the oracle, so it must be obviously correct rather than fast.
func naiveDot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func randVec(r *rand.Rand, n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = r.Float32()*2 - 1
	}
	return v
}

func TestDotMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	// Dimensions either side of the unroll width, so the 8-wide body and the
	// scalar tail are both exercised, including the cases where one of them
	// does no work at all.
	for _, dims := range []int{0, 1, 7, 8, 9, 15, 16, 17, 63, 100, 128, 768, 769} {
		for trial := 0; trial < 20; trial++ {
			a, b := randVec(r, dims), randVec(r, dims)
			got := float64(Dot(a, b))
			want := naiveDot(a, b)

			// float32 accumulation in a different order than the reference,
			// so exact equality is not expected; the tolerance scales with
			// dims because that is how rounding error accumulates.
			tol := 1e-5 * float64(dims+1)
			if stdmath.Abs(got-want) > tol {
				t.Fatalf("dims=%d: Dot = %v, reference = %v (diff %v > tol %v)",
					dims, got, want, stdmath.Abs(got-want), tol)
			}
		}
	}
}

func TestDotIsDeterministic(t *testing.T) {
	// The cascade's differential test compares scores exactly, which only
	// holds if Dot is bit-stable across calls.
	r := rand.New(rand.NewSource(2))
	a, b := randVec(r, 768), randVec(r, 768)

	first := Dot(a, b)
	for i := 0; i < 100; i++ {
		if got := Dot(a, b); got != first {
			t.Fatalf("Dot not deterministic: call %d gave %v, first gave %v", i, got, first)
		}
	}
}

func TestDotOrthogonalAndParallel(t *testing.T) {
	a := []float32{1, 0, 0, 0, 0, 0, 0, 0, 0}
	b := []float32{0, 1, 0, 0, 0, 0, 0, 0, 0}
	if got := Dot(a, b); got != 0 {
		t.Errorf("orthogonal vectors: got %v, want 0", got)
	}
	if got := Dot(a, a); got != 1 {
		t.Errorf("unit vector with itself: got %v, want 1", got)
	}
}

func TestDotPanicsOnLengthMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Dot did not panic on mismatched lengths")
		}
	}()
	Dot(make([]float32, 8), make([]float32, 9))
}

func TestNormalize(t *testing.T) {
	r := rand.New(rand.NewSource(3))

	for _, dims := range []int{1, 7, 8, 9, 128, 768} {
		v := randVec(r, dims)
		want := Norm(v)

		got := Normalize(v)
		if stdmath.Abs(float64(got-want)) > 1e-6 {
			t.Errorf("dims=%d: Normalize returned %v, want original norm %v", dims, got, want)
		}

		// The whole design leans on ||v|| == 1 after this call: it makes
		// cosine equal dot product, and it is a precondition of the
		// cascade's Cauchy-Schwarz bound.
		if n := Norm(v); stdmath.Abs(float64(n)-1) > 1e-6 {
			t.Errorf("dims=%d: norm after Normalize = %v, want 1", dims, n)
		}
	}
}

func TestNormalizeZeroVector(t *testing.T) {
	v := make([]float32, 16)
	if got := Normalize(v); got != 0 {
		t.Errorf("zero vector: Normalize returned %v, want 0", got)
	}
	for i, x := range v {
		if x != 0 {
			t.Fatalf("zero vector was modified at %d: %v (NaN would poison every later score)", i, x)
		}
	}
}

func TestNormalizedDotIsCosine(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	for trial := 0; trial < 50; trial++ {
		a, b := randVec(r, 768), randVec(r, 768)

		cosine := naiveDot(a, b) / (float64(Norm(a)) * float64(Norm(b)))
		Normalize(a)
		Normalize(b)

		if diff := stdmath.Abs(float64(Dot(a, b)) - cosine); diff > 1e-5 {
			t.Fatalf("dot of normalized vectors %v != cosine %v (diff %v)", Dot(a, b), cosine, diff)
		}
	}
}

func BenchmarkDot(b *testing.B) {
	for _, dims := range []int{128, 384, 768} {
		r := rand.New(rand.NewSource(5))
		x, y := randVec(r, dims), randVec(r, dims)
		b.Run(itoa(dims), func(b *testing.B) {
			b.SetBytes(int64(dims * 4 * 2))
			for i := 0; i < b.N; i++ {
				sink = Dot(x, y)
			}
		})
	}
}

var sink float32

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

//go:build amd64

package math

import (
	stdmath "math"
	"math/rand"
	"testing"
)

// TestDotInt8AVX2MatchesGeneric is the differential test for the Avo-generated
// kernel: it must agree with the pure-Go reference at every dimension,
// including sizes that straddle the kernel's 32-element block/tail boundary.
func TestDotInt8AVX2MatchesGeneric(t *testing.T) {
	if !hasFastInt8 {
		t.Skip("requires AVX2, AVX and FMA3")
	}

	r := rand.New(rand.NewSource(7))
	dimsCases := []int{0, 1, 7, 8, 9, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 768, 769}

	for _, dims := range dimsCases {
		q := randVec(r, dims)
		code := make([]int8, dims)
		for i := range code {
			code[i] = int8(r.Intn(255) - 127)
		}

		want := dotInt8Generic(q, code)
		got := dotInt8AVX2(q, code)

		tol := float32(1e-4) * float32(dims+1)
		if stdmath.Abs(float64(got-want)) > float64(tol) {
			t.Fatalf("dims=%d: dotInt8AVX2 = %v, dotInt8Generic = %v", dims, got, want)
		}
	}
}

func TestKernelNameReflectsHardware(t *testing.T) {
	if hasFastInt8 && kernelName != "avx2" {
		t.Errorf("hasFastInt8=true but kernelName=%q, want avx2", kernelName)
	}
	if !hasFastInt8 && kernelName != "pure-go" {
		t.Errorf("hasFastInt8=false but kernelName=%q, want pure-go", kernelName)
	}
}

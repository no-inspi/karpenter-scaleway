package pricing

import (
	"strconv"
	"strings"
	"testing"
)

func TestRoundEURHourly_DropsFloat32Artefact(t *testing.T) {
	// 0.0910 (intended price) → float32 → float64 = 0.09099999815225601
	got := RoundEURHourly(0.0910)
	if got != 0.091 {
		t.Fatalf("got %v, want 0.091", got)
	}
}

func TestRoundEURHourly_StablesUnderRoundTrip(t *testing.T) {
	a := RoundEURHourly(0.27559998)
	b := RoundEURHourly(float32(a))
	if a != b {
		t.Fatalf("not idempotent: a=%v b=%v", a, b)
	}
}

func TestRoundEURHourly_AtMost6Decimals(t *testing.T) {
	for _, v := range []float32{0.0910, 0.20669999, 0.27559998, 0.5, 1.234567} {
		got := RoundEURHourly(v)
		s := strconv.FormatFloat(got, 'f', -1, 64)
		if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i-1 > 6 {
			t.Fatalf("input %v → %q has > 6 decimals", v, s)
		}
	}
}

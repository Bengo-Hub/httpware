package money

import "testing"

func TestSplitNegativeTotalMirrorsPositive(t *testing.T) {
	pos := Split(100, 2, 3)
	neg := Split(-100, 2, 3)
	if len(neg) != 3 {
		t.Fatalf("want 3 parts, got %v", neg)
	}
	var sum float64
	for i, p := range neg {
		if p > 0 {
			t.Errorf("part %d must not be positive: %v", i, neg)
		}
		if p != -pos[i] {
			t.Errorf("part %d: want %v, got %v", i, -pos[i], p)
		}
		sum += p
	}
	if Round(sum, 2) != -100 {
		t.Errorf("negative parts must add back to the total, got %v", sum)
	}
	// Weights apply to a negative total too, and zero parts stay zero (never negative zero).
	w := Split(-10, 2, 2, 1, 0)
	if len(w) != 2 || w[0] != -10 || w[1] != 0 {
		t.Errorf("weighted negative split: %v", w)
	}
}

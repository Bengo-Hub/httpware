package money

import (
	"math"
	"math/rand"
	"testing"
)

func total(p []float64, dp int) float64 {
	var t float64
	for _, v := range p {
		t += v
	}
	return Round(t, dp)
}

func TestRound(t *testing.T) {
	cases := []struct {
		in   float64
		dp   int
		want float64
	}{
		{74.24000000000001, 2, 74.24},
		{148.49999999999997, 2, 148.5},
		{1330.6499999999999, 2, 1330.65},
		{1.005, 2, 1.01},
		{2.675, 2, 2.68},
		{-1.005, 2, -1.01},
		{0.125, 2, 0.13},
		{85.99998624, 2, 86},
		{1234.5, 0, 1235},
		{-1234.5, 0, -1235},
		{0.0000001, 2, 0},
		{-0.0000001, 2, 0},
		{5, 2, 5},
		{10.4999, 0, 10},
		{7.123456, 4, 7.1235},
		{3, -1, 3},
	}
	for _, c := range cases {
		if got := Round(c.in, c.dp); got != c.want {
			t.Errorf("Round(%v,%d) = %v, want %v", c.in, c.dp, got, c.want)
		}
	}
	if math.Signbit(Round(-0.0000001, 2)) {
		t.Error("negative zero returned")
	}
	if !math.IsNaN(Round(math.NaN(), 2)) || !math.IsInf(Round(math.Inf(1), 2), 1) {
		t.Error("NaN or Inf was altered")
	}
}

// A value genuinely below a half cent must not be pushed up.
func TestRoundDoesNotOverreach(t *testing.T) {
	if got := Round(1.00499, 2); got != 1 {
		t.Errorf("Round(1.00499,2) = %v, want 1", got)
	}
}

func TestRoundIdempotentAndOnGrid(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for n := 0; n < 5000; n++ {
		v := float64(r.Intn(100_000_000))/100 + (r.Float64()-0.5)*1e-9
		a := Round(v, 2)
		if Round(a, 2) != a {
			t.Fatalf("not idempotent for %v", v)
		}
		if c := a * 100; math.Abs(c-math.Round(c)) > 1e-7 {
			t.Fatalf("%v rounded to %v, off the grid", v, a)
		}
	}
}

func TestPlaces(t *testing.T) {
	for code, want := range map[string]int{"KES": 2, "kes": 2, " UGX ": 0, "RWF": 0, "JPY": 0, "USD": 2, "": 2, "XXX": 2} {
		if got := Places(code); got != want {
			t.Errorf("Places(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestRoundForAndConvert(t *testing.T) {
	if got := RoundFor("UGX", 1234.56); got != 1235 {
		t.Errorf("UGX = %v", got)
	}
	if got := Convert(100, 28.5342, "UGX"); got != 2853 {
		t.Errorf("convert to UGX = %v", got)
	}
	if got := Convert(100, 0.007712, "USD"); got != 0.77 {
		t.Errorf("convert to USD = %v", got)
	}
}

func TestFieldsSumEqual(t *testing.T) {
	a, b := 74.24000000000001, 20.480000000000004
	Fields2(&a, nil, &b)
	if a != 74.24 || b != 20.48 {
		t.Fatalf("got %v %v", a, b)
	}
	if got := Sum(2, 0.1, 0.2, 0.3); got != 0.6 {
		t.Errorf("sum = %v", got)
	}
	if !Equal(0.1+0.2, 0.3, 2) || Equal(0.1, 0.2, 2) {
		t.Error("Equal wrong")
	}
}

func TestSplit(t *testing.T) {
	even := Split(100, 2, 3)
	if total(even, 2) != 100 || even[0] != 33.34 || even[1] != 33.33 || even[2] != 33.33 {
		t.Errorf("even split = %v", even)
	}
	if got := Split(10, 2, 3, 1, 1, 1); total(got, 2) != 10 {
		t.Errorf("weighted split = %v", got)
	}
	if got := Split(0.05, 2, 3, 3, 1, 1); total(got, 2) != 0.05 {
		t.Errorf("tiny split = %v", got)
	}
	if got := Split(100, 0, 3); total(got, 0) != 100 {
		t.Errorf("whole split = %v", got)
	}
	if Split(10, 2, 0) != nil || Split(10, 2, 2, 1) != nil || Split(10, 2, 2, 0, 0) != nil || Split(10, 2, 2, 1, -1) != nil {
		t.Error("bad input should return nil")
	}
	r := rand.New(rand.NewSource(5))
	for n := 0; n < 2000; n++ {
		amt := float64(r.Intn(1_000_000)) / 100
		k := 1 + r.Intn(9)
		ws := make([]float64, k)
		for i := range ws {
			ws[i] = float64(1 + r.Intn(50))
		}
		p := Split(amt, 2, k, ws...)
		if total(p, 2) != Round(amt, 2) {
			t.Fatalf("split of %v by %v = %v, sums to %v", amt, ws, p, total(p, 2))
		}
	}
}

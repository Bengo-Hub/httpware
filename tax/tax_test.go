package tax

import (
	"encoding/json"
	"os"
	"testing"
)

type vectorLine struct {
	Quantity  float64 `json:"quantity"`
	UnitPrice float64 `json:"unit_price"`
	Discount  float64 `json:"discount"`
	Rate      float64 `json:"rate"`
	Inclusive bool    `json:"inclusive"`
	Band      string  `json:"band"`
}

type vectorLineResult struct {
	Net         float64 `json:"net"`
	Tax         float64 `json:"tax"`
	Gross       float64 `json:"gross"`
	DocDiscount float64 `json:"doc_discount"`
	Band        string  `json:"band"`
}

type vector struct {
	Name         string       `json:"name"`
	DiscountBase string       `json:"discount_base"`
	Discount     float64      `json:"discount"`
	Lines        []vectorLine `json:"lines"`
	Expect       struct {
		Lines       []vectorLineResult   `json:"lines"`
		Net         float64              `json:"net"`
		Tax         float64              `json:"tax"`
		Gross       float64              `json:"gross"`
		DocDiscount float64              `json:"doc_discount"`
		Bands       map[string]BandTotal `json:"bands"`
	} `json:"expect"`
}

// TestVectors runs the shared vectors. shared-ui-lib's tax tests run the same file, so the till,
// the storefront and every Go service reach the same figures.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			doc := Document{Discount: v.Discount}
			if v.DiscountBase == "gross" {
				doc.DiscountBase = DiscountOnGross
			}
			for _, l := range v.Lines {
				doc.Lines = append(doc.Lines, Line{Quantity: l.Quantity, UnitPrice: l.UnitPrice,
					Discount: l.Discount, Rate: l.Rate, Inclusive: l.Inclusive, Band: l.Band})
			}
			got := Compute(doc)
			for i, want := range v.Expect.Lines {
				g := got.Lines[i]
				if g.Net != want.Net || g.Tax != want.Tax || g.Gross != want.Gross ||
					g.DocDiscount != want.DocDiscount || string(g.Band) != want.Band {
					t.Errorf("line %d: got net %v tax %v gross %v share %v band %s, want %+v",
						i, g.Net, g.Tax, g.Gross, g.DocDiscount, g.Band, want)
				}
			}
			if got.Net != v.Expect.Net || got.Tax != v.Expect.Tax || got.Gross != v.Expect.Gross ||
				got.DocDiscount != v.Expect.DocDiscount {
				t.Errorf("totals: got net %v tax %v gross %v discount %v, want net %v tax %v gross %v discount %v",
					got.Net, got.Tax, got.Gross, got.DocDiscount, v.Expect.Net, v.Expect.Tax, v.Expect.Gross, v.Expect.DocDiscount)
			}
			if len(got.Bands) != len(v.Expect.Bands) {
				t.Errorf("bands: got %v, want %v", got.Bands, v.Expect.Bands)
			}
			for b, want := range v.Expect.Bands {
				if got.Bands[Band(b)] != want {
					t.Errorf("band %s: got %+v, want %+v", b, got.Bands[Band(b)], want)
				}
			}
		})
	}
}

func TestSplit(t *testing.T) {
	cases := []struct {
		amount, rate    float64
		inclusive       bool
		net, tax, gross float64
	}{
		{116, 16, true, 100, 16, 116},
		{100, 16, false, 100, 16, 116},
		{1390, 0, true, 1390, 0, 1390},
		{108, 8, true, 100, 8, 108},
		{-116, 16, true, -100, -16, -116}, // a refund line
	}
	for _, c := range cases {
		net, tax, gross := Split(c.amount, c.rate, c.inclusive)
		if net != c.net || tax != c.tax || gross != c.gross {
			t.Errorf("Split(%v, %v, %v) = %v %v %v, want %v %v %v",
				c.amount, c.rate, c.inclusive, net, tax, gross, c.net, c.tax, c.gross)
		}
	}
}

func TestBandFor(t *testing.T) {
	cases := []struct {
		stored string
		rate   float64
		want   Band
	}{
		{"B", 16, BandStandard},
		{"", 16, BandStandard},
		{"A", 16, BandStandard}, // the rate wins over a wrong letter
		{"E", 8, BandReduced},
		{"c", 0, BandZero},
		{"D", 0, BandNonVAT},
		{"A", 0, BandExempt},
		{"B", 0, BandExempt},
		{"E", 0, BandExempt},
		{"", 0, BandExempt},
		{"X", 0, BandExempt},
	}
	for _, c := range cases {
		if got := BandFor(c.stored, c.rate); got != c.want {
			t.Errorf("BandFor(%q, %v) = %s, want %s", c.stored, c.rate, got, c.want)
		}
	}
}

func TestComputeSharesAddUpExactly(t *testing.T) {
	doc := Document{Discount: 10, DiscountBase: DiscountOnGross}
	for i := 0; i < 3; i++ {
		doc.Lines = append(doc.Lines, Line{Quantity: 1, UnitPrice: 33.33, Rate: 16, Inclusive: true, Band: "B"})
	}
	got := Compute(doc)
	if got.DocDiscount != 10 {
		t.Fatalf("shares add to %v, want 10", got.DocDiscount)
	}
	if got.Gross != 89.99 {
		t.Fatalf("bill %v, want 99.99 - 10 = 89.99", got.Gross)
	}
}

func TestComputeNegativeLinesTakeNoDiscount(t *testing.T) {
	doc := Document{Discount: 10, Lines: []Line{
		{Quantity: 1, UnitPrice: 100, Rate: 16, Band: "B"},
		{Quantity: -1, UnitPrice: 20, Rate: 16, Band: "B"},
	}}
	got := Compute(doc)
	if got.Lines[1].DocDiscount != 0 || got.Lines[0].DocDiscount != 10 {
		t.Fatalf("returned line took a share: %+v", got.Lines)
	}
	if got.Lines[1].Tax != -3.2 {
		t.Fatalf("returned line tax %v, want -3.2", got.Lines[1].Tax)
	}
}

func TestPercentOf(t *testing.T) {
	if got := PercentOf(613.6, 10); got != 61.36 {
		t.Fatalf("PercentOf = %v", got)
	}
	if got := PercentOf(50, 150); got != 50 {
		t.Fatalf("PercentOf over 100 = %v", got)
	}
	if got := PercentOf(50, -5); got != 0 {
		t.Fatalf("PercentOf negative = %v", got)
	}
}

func TestRatesRateFor(t *testing.T) {
	r := Rates{Standard: 16, Reduced: 8}
	for b, want := range map[Band]float64{BandStandard: 16, BandReduced: 8, BandExempt: 0, BandZero: 0, BandNonVAT: 0} {
		if got := r.RateFor(b); got != want {
			t.Errorf("RateFor(%s) = %v, want %v", b, got, want)
		}
	}
}

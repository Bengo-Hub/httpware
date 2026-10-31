package tax

import (
	"math"

	"github.com/Bengo-Hub/httpware/money"
)

// Line is one line of a sale, invoice or cart as the seller entered it.
type Line struct {
	Quantity  float64
	UnitPrice float64 // VAT-inclusive when Inclusive, otherwise the taxable unit price
	Discount  float64 // line discount amount, on the same basis as UnitPrice
	Rate      float64 // VAT percent; 0 for exempt, zero-rated and non-VAT lines
	Inclusive bool
	Band      string // stored OSCU letter; resolved with BandFor
}

// LineResult is a line after discounts and VAT, every figure rounded to 2 decimal places.
type LineResult struct {
	Amount      float64 // quantity x unit price, on the line's own basis
	Discount    float64 // the line's own discount
	DocDiscount float64 // the line's share of the document discount, on the document's discount basis
	Net         float64 // taxable value
	Tax         float64
	Gross       float64 // net + tax, what the buyer pays for the line
	Rate        float64
	Band        Band
}

// DiscountBase says what a document discount is taken off.
type DiscountBase int

const (
	// DiscountOnNet takes the discount off the taxable subtotal, as on an invoice where the
	// global discount sits under the pre-VAT subtotal.
	DiscountOnNet DiscountBase = iota
	// DiscountOnGross takes the discount off the amount payable, as at a till where the cashier
	// knocks a figure off the bill. The bill then falls by exactly the discount.
	DiscountOnGross
)

// Document is a set of lines with an optional document (global) discount amount.
type Document struct {
	Lines        []Line
	Discount     float64 // resolve a percentage first with PercentOf
	DiscountBase DiscountBase
}

// BandTotal is the taxable value and VAT of one band, the figures of an eTIMS header.
type BandTotal struct {
	Taxable float64
	Tax     float64
}

// Result is a computed document. Totals are sums of the rounded lines, never recomputed from a
// subtotal, so the lines, the totals and the eTIMS header always agree.
type Result struct {
	Lines        []LineResult
	Amount       float64
	LineDiscount float64
	DocDiscount  float64
	Net          float64
	Tax          float64
	Gross        float64
	Bands        map[Band]BandTotal
}

// PercentOf resolves a percentage discount against a base, rounded to 2 decimal places.
func PercentOf(base, percent float64) float64 {
	if percent <= 0 || base <= 0 {
		return 0
	}
	return money.Round2(base * math.Min(percent, 100) / 100)
}

// Compute works out every line and the document totals. The document discount is shared across
// the lines in proportion to their value on the discount basis (largest remainder, so the shares
// add up to the discount exactly) and reduces each line before its VAT is worked out. A discount
// larger than the lines is capped at their value.
func Compute(doc Document) Result {
	n := len(doc.Lines)
	res := Result{Lines: make([]LineResult, n), Bands: map[Band]BandTotal{}}
	if n == 0 {
		return res
	}

	after := make([]float64, n)  // line value after its own discount, on its own basis
	weight := make([]float64, n) // line value on the discount basis
	var base float64
	for i, l := range doc.Lines {
		amount := money.Round2(l.Quantity * l.UnitPrice)
		disc := money.Round2(math.Max(0, l.Discount))
		after[i] = amount - disc
		res.Lines[i] = LineResult{Amount: amount, Discount: disc, Rate: l.Rate, Band: BandFor(l.Band, l.Rate)}
		weight[i] = math.Max(0, onBasis(after[i], l, doc.DiscountBase))
		base += weight[i]
	}

	shares := make([]float64, n)
	if d := money.Round2(math.Min(math.Max(0, doc.Discount), money.Round2(base))); d > 0 && base > 0 {
		if s := money.Split(d, 2, n, weight...); s != nil {
			shares = s
		}
	}

	for i, l := range doc.Lines {
		lr := &res.Lines[i]
		lr.DocDiscount = shares[i]
		switch {
		case doc.DiscountBase == DiscountOnGross && !l.Inclusive:
			// The share comes off the line's payable amount, so work the line out inclusively from
			// what the buyer now pays for it.
			_, _, gross := Split(after[i], l.Rate, false)
			lr.Net, lr.Tax, lr.Gross = Split(gross-shares[i], l.Rate, true)
		case doc.DiscountBase == DiscountOnNet && l.Inclusive:
			// The share is a taxable value; take its VAT-inclusive equivalent off the shelf price.
			_, _, shareGross := Split(shares[i], l.Rate, false)
			lr.Net, lr.Tax, lr.Gross = Split(after[i]-shareGross, l.Rate, true)
		default:
			lr.Net, lr.Tax, lr.Gross = Split(after[i]-shares[i], l.Rate, l.Inclusive)
		}

		res.Amount += lr.Amount
		res.LineDiscount += lr.Discount
		res.DocDiscount += lr.DocDiscount
		res.Net += lr.Net
		res.Tax += lr.Tax
		res.Gross += lr.Gross
		bt := res.Bands[lr.Band]
		bt.Taxable = money.Round2(bt.Taxable + lr.Net)
		bt.Tax = money.Round2(bt.Tax + lr.Tax)
		res.Bands[lr.Band] = bt
	}
	money.Fields2(&res.Amount, &res.LineDiscount, &res.DocDiscount, &res.Net, &res.Tax, &res.Gross)
	return res
}

// onBasis converts a line value on its own basis to the document's discount basis.
func onBasis(v float64, l Line, b DiscountBase) float64 {
	if v <= 0 || l.Rate <= 0 {
		return v
	}
	switch {
	case b == DiscountOnGross && !l.Inclusive:
		return v * (100 + l.Rate) / 100
	case b == DiscountOnNet && l.Inclusive:
		return v * 100 / (100 + l.Rate)
	}
	return v
}

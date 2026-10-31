// Package tax is the one place the platform works out VAT on a line or a document.
//
// Treasury owns the tax configuration (codes, rates, the VAT switch, tenant tax settings) and
// serves it to the other services. This package owns the arithmetic, so the till, the server, the
// storefront, the invoice and the eTIMS payload all reach the same figure to the cent:
//
//   - VAT is worked out per line and rounded to 2 decimal places on each line.
//   - A document (global) discount is shared across the lines before VAT, so it reduces the
//     taxable value as the VAT Act requires for a discount given at the time of supply.
//   - Document totals and the eTIMS header are the sums of the rounded lines, per band.
//
// The TypeScript mirror lives in @bengo-hub/shared-ui-lib/tax and is held to the same test
// vectors (testdata/vectors.json).
package tax

import "strings"

// Band is a KRA eTIMS OSCU tax band (code list class 04).
type Band string

// The OSCU bands. The legacy "standard eTIMS" letters (A=16%, B=0, C=exempt) are not used anywhere
// on the platform any more; normalise stored letters with BandFor.
const (
	BandExempt   Band = "A" // exempt supplies, no VAT
	BandStandard Band = "B" // standard rate (16% today)
	BandZero     Band = "C" // zero-rated supplies
	BandNonVAT   Band = "D" // seller not VAT-registered, or a supply outside VAT
	BandReduced  Band = "E" // reduced rate (8% today, petroleum products)
)

// Bands lists every band in header order.
var Bands = []Band{BandExempt, BandStandard, BandZero, BandNonVAT, BandReduced}

// Rates are the positive VAT rates, in percent, that treasury serves from its effective-dated
// statutory rates (VAT_STANDARD, VAT_REDUCED). Callers never hardcode them.
type Rates struct {
	Standard float64
	Reduced  float64
}

// RateFor returns the VAT rate of a band. A, C and D carry no VAT.
func (r Rates) RateFor(b Band) float64 {
	switch b {
	case BandStandard:
		return r.Standard
	case BandReduced:
		return r.Reduced
	}
	return 0
}

// ParseBand normalises a stored letter. It returns false for anything that is not an OSCU band.
func ParseBand(s string) (Band, bool) {
	b := Band(strings.ToUpper(strings.TrimSpace(s)))
	switch b {
	case BandExempt, BandStandard, BandZero, BandNonVAT, BandReduced:
		return b, true
	}
	return "", false
}

// BandFor resolves the band of a line from its stored letter and its effective VAT rate. A taxed
// rate decides between B and E; a zero rate trusts the stored zero band (A, C or D) and falls back
// to exempt, the neutral zero band, when the letter is missing or names a taxed band.
func BandFor(stored string, rate float64) Band {
	switch {
	case rate >= 15:
		return BandStandard
	case rate >= 7 && rate <= 9:
		return BandReduced
	}
	if b, ok := ParseBand(stored); ok && b != BandStandard && b != BandReduced {
		return b
	}
	return BandExempt
}

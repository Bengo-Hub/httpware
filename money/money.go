// Package money is the one place the platform rounds amounts of money.
//
// Services keep amounts as float64 in the database and on the wire, and repeated division, tax
// extraction and currency conversion leave noise such as 74.24000000000001. Left in, that noise fails
// regulator validation (KRA rejects more than 2 decimals) and makes ledger sides and balance sheets
// disagree by a fraction. Every service rounds through this package so the same amount always rounds
// the same way, at the decimal places its currency uses.
package money

import (
	"math"
	"strings"
)

// DefaultPlaces is the decimal places of an amount whose currency is unknown or unlisted.
const DefaultPlaces = 2

// noiseNudge pushes a value that sits a hair below a half toward it, so 1.005 (stored as
// 1.00499999999999989) rounds to 1.01 as a person expects. It is about 90 binary ulps, far smaller
// than any real fraction of a cent.
const noiseNudge = 1e-14

// places maps a currency code to its minor unit decimal places. It mirrors CURRENCY_META in
// shared-ui-lib so the UI and the services agree; keep the two in step.
var places = map[string]int{
	"KES": 2, "USD": 2, "EUR": 2, "GBP": 2, "UGX": 0, "TZS": 2, "ZAR": 2, "NGN": 2,
	"GHS": 2, "RWF": 0, "ETB": 2, "EGP": 2, "INR": 2, "CNY": 2, "JPY": 0, "CAD": 2,
	"AUD": 2, "CHF": 2, "AED": 2, "SAR": 2,
}

// Places returns the decimal places of a currency code, case insensitive. An unlisted or empty code
// gets DefaultPlaces.
func Places(currency string) int {
	if p, ok := places[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return p
	}
	return DefaultPlaces
}

// Round rounds v half away from zero to the given decimal places and strips float noise. NaN and
// infinities are returned unchanged. Negative places are treated as 0.
func Round(v float64, dp int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	if dp < 0 {
		dp = 0
	}
	scale := math.Pow10(dp)
	x := v * scale
	r := math.Round(x * (1 + noiseNudge)) // grows the magnitude, so negatives round away from zero too
	out := r / scale
	if out == 0 {
		return 0 // never return negative zero
	}
	return out
}

// Round2 rounds to 2 decimal places, the default for amounts in most currencies.
func Round2(v float64) float64 { return Round(v, 2) }

// RoundFor rounds v to the decimal places of the currency.
func RoundFor(currency string, v float64) float64 { return Round(v, Places(currency)) }

// Fields rounds each pointed-to amount in place to dp decimal places. Nil pointers are skipped.
func Fields(dp int, vals ...*float64) {
	for _, p := range vals {
		if p != nil {
			*p = Round(*p, dp)
		}
	}
}

// Fields2 rounds each pointed-to amount in place to 2 decimal places.
func Fields2(vals ...*float64) { Fields(2, vals...) }

// Sum adds amounts and rounds the total to dp places.
func Sum(dp int, vals ...float64) float64 {
	var t float64
	for _, v := range vals {
		t += v
	}
	return Round(t, dp)
}

// Equal reports whether two amounts are the same once rounded to dp places.
func Equal(a, b float64, dp int) bool { return Round(a, dp) == Round(b, dp) }

// Convert converts an amount between currencies at rate (units of the target per one unit of the
// source) and rounds to the target currency places. Rounding at the target keeps a UGX or JPY
// result whole while a KES or USD result keeps cents.
func Convert(amount, rate float64, toCurrency string) float64 {
	return RoundFor(toCurrency, amount*rate)
}

// Split divides total into n parts that each sit on the dp grid and add back to exactly total
// (largest remainder method). It is for splitting a discount, a fee or a payment across lines
// without losing or inventing a cent. weights, when given, must have n entries and parts are
// proportional to them; with no weights the split is even. It returns nil when n is below 1, the
// weights do not match n, any weight is negative, or the weights are all zero.
func Split(total float64, dp int, n int, weights ...float64) []float64 {
	if n < 1 || (len(weights) != 0 && len(weights) != n) {
		return nil
	}
	w := weights
	if len(w) == 0 {
		w = make([]float64, n)
		for i := range w {
			w[i] = 1
		}
	}
	var sumW float64
	for _, x := range w {
		if x < 0 {
			return nil
		}
		sumW += x
	}
	if sumW == 0 {
		return nil
	}
	scale := math.Pow10(dp)
	units := int64(math.Round(Round(total, dp) * scale))
	floor := make([]int64, n)
	frac := make([]float64, n)
	var allotted int64
	for i, x := range w {
		exact := float64(units) * x / sumW
		f := math.Floor(exact)
		floor[i] = int64(f)
		frac[i] = exact - f
		allotted += floor[i]
	}
	// Hand the leftover units, one each, to the largest remainders (the earlier index wins a tie).
	for left := units - allotted; left > 0; left-- {
		best := 0
		for i := 1; i < n; i++ {
			if frac[i] > frac[best] {
				best = i
			}
		}
		floor[best]++
		frac[best] = -1
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(floor[i]) / scale
	}
	return out
}

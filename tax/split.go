package tax

import "github.com/Bengo-Hub/httpware/money"

// Split separates an amount into its taxable value and VAT at rate percent, rounded to 2 decimal
// places. When inclusive is true the amount already contains the VAT (a shelf price); otherwise it
// is the taxable value and the VAT is added on top. net + tax always equals the gross exactly.
func Split(amount, rate float64, inclusive bool) (net, tax, gross float64) {
	amount = money.Round2(amount)
	if rate <= 0 {
		return amount, 0, amount
	}
	if inclusive {
		tax = money.Round2(amount * rate / (100 + rate))
		return money.Round2(amount - tax), tax, amount
	}
	tax = money.Round2(amount * rate / 100)
	return amount, tax, money.Round2(amount + tax)
}

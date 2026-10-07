package contact

import "testing"

func TestNormalizePhone(t *testing.T) {
	ok := map[string]string{
		"0712345678":        "+254712345678",
		"0712 345 678":      "+254712345678",
		"+254712345678":     "+254712345678",
		"254712345678":      "+254712345678",
		"+254 (0)712345678": "+254712345678",
		"0110123456":        "+254110123456",
		"+256772123456":     "+256772123456",
	}
	for in, want := range ok {
		got, err := NormalizePhone(in, "")
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "12345", "abc", "+2547", "0000000000"} {
		if _, err := NormalizePhone(bad, ""); err == nil {
			t.Errorf("NormalizePhone(%q) should fail", bad)
		}
	}
}

// Numbers from any country are valid when written with their country code; the region only
// changes how a local "0..." form is read.
func TestNormalizePhoneAnyCountry(t *testing.T) {
	intl := map[string]string{
		"+44 7911 123456":   "+447911123456",
		"447911123456":      "+447911123456",
		"+1 415 555 2671":   "+14155552671",
		"+234 803 123 4567": "+2348031234567",
		"+255 754 123 456":  "+255754123456",
		"+971 50 123 4567":  "+971501234567",
	}
	for in, want := range intl {
		got, err := NormalizePhone(in, "KE")
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q, KE) = %q, %v; want %q", in, got, err, want)
		}
	}
	local := []struct{ raw, region, want string }{
		{"0772123456", "UG", "+256772123456"},
		{"07911 123456", "GB", "+447911123456"},
		{"0754123456", "TZ", "+255754123456"},
	}
	for _, c := range local {
		got, err := NormalizePhone(c.raw, c.region)
		if err != nil || got != c.want {
			t.Errorf("NormalizePhone(%q, %s) = %q, %v; want %q", c.raw, c.region, got, err, c.want)
		}
	}
}

func TestSubscriberDigits(t *testing.T) {
	if SubscriberDigits("0712345678") != SubscriberDigits("+254 712 345 678") {
		t.Error("local and international forms should share subscriber digits")
	}
}

func TestNormalizeEmail(t *testing.T) {
	ok := map[string]string{
		" Titus@Example.COM ":       "titus@example.com",
		"Titus <titus@example.com>": "titus@example.com",
		"a.b+tag@sub.example.co.ke": "a.b+tag@sub.example.co.ke",
	}
	for in, want := range ok {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "titus", "titus@", "titus@localhost", "a@b.com,c@d.com", "x@.com"} {
		if ValidEmailSyntax(bad) {
			t.Errorf("ValidEmailSyntax(%q) should be false", bad)
		}
	}
}

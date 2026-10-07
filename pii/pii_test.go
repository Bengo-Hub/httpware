package pii

import "testing"

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"titus@example.com": "t***@example.com",
		"a@b.co":            "a***@b.co",
		"not-an-email":      "no***",
		"":                  "",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"+254712345678": "+254******678",
		"0712345678":    "071****678",
		"12345":         "*****",
	}
	for in, want := range cases {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskPicksKind(t *testing.T) {
	if got := Mask("x@y.com"); got != "x***@y.com" {
		t.Errorf("email: %q", got)
	}
	if got := Mask("+254 712 345 678"); got != "+254******678" {
		t.Errorf("phone: %q", got)
	}
	if got := Mask(`{"endpoint":"https://fcm"}`); got != `{"***` {
		t.Errorf("token: %q", got)
	}
}

func TestHashAddressStable(t *testing.T) {
	if HashAddress("Titus@Example.com ") != HashAddress("titus@example.com") {
		t.Error("email hash should ignore case and spaces")
	}
	if HashAddress("+254 712-345-678") != HashAddress("+254712345678") {
		t.Error("phone hash should ignore formatting")
	}
	if HashAddress("+254712345678") == HashAddress("titus@example.com") {
		t.Error("different addresses must not collide")
	}
}

// Package pii masks and hashes personal contact details so they can be logged, shown in lists or
// used as lookup keys without exposing the address itself.
package pii

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"go.uber.org/zap"
)

// MaskEmail keeps the first character of the local part and the whole domain:
// "titus@example.com" becomes "t***@example.com". Anything without an "@" is masked as a whole.
func MaskEmail(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return MaskString(email)
	}
	local, domain := email[:at], email[at+1:]
	return string([]rune(local)[0]) + "***@" + domain
}

// MaskPhone keeps a leading "+" and country code style prefix and the last three digits:
// "+254712345678" becomes "+254*****678". Short values are fully masked.
func MaskPhone(phone string) string {
	digits := onlyDigits(phone)
	if len(digits) < 7 {
		return strings.Repeat("*", len(digits))
	}
	prefix := ""
	if strings.HasPrefix(strings.TrimSpace(phone), "+") {
		prefix = "+"
	}
	head := digits[:3]
	tail := digits[len(digits)-3:]
	return prefix + head + strings.Repeat("*", len(digits)-6) + tail
}

// MaskString hides all but the first two characters of an opaque value such as a device token.
func MaskString(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 2 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:2]) + "***"
}

// Mask picks the right masking for a contact address: email, phone, or anything else.
func Mask(address string) string {
	a := strings.TrimSpace(address)
	switch {
	case strings.Contains(a, "@"):
		return MaskEmail(a)
	case looksLikePhone(a):
		return MaskPhone(a)
	default:
		return MaskString(a)
	}
}

// HashAddress is the stable lookup key for an address: SHA-256 hex of the lowercased address,
// with phones reduced to their digits so "+254 712 345 678" and "+254712345678" match. Callers
// should normalise phones to E.164 first (see the contact package) so local and international
// forms hash the same.
func HashAddress(address string) string {
	a := strings.ToLower(strings.TrimSpace(address))
	if looksLikePhone(a) {
		a = "+" + onlyDigits(a)
	}
	sum := sha256.Sum256([]byte(a))
	return hex.EncodeToString(sum[:])
}

// Email is a zap field carrying a masked email.
func Email(key, email string) zap.Field { return zap.String(key, MaskEmail(email)) }

// Phone is a zap field carrying a masked phone number.
func Phone(key, phone string) zap.Field { return zap.String(key, MaskPhone(phone)) }

// Address is a zap field carrying any masked contact address.
func Address(key, address string) zap.Field { return zap.String(key, Mask(address)) }

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func looksLikePhone(s string) bool {
	if s == "" {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digits++
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return digits >= 7
}

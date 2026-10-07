// Package contact normalises and validates phone numbers and email addresses so every service
// stores, compares and sends to them the same way. Phones are E.164 ("+254712345678").
package contact

import (
	"errors"
	"net/mail"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// DefaultRegion is used for numbers written without a country code ("0712 345 678").
const DefaultRegion = "KE"

// ErrInvalidPhone is returned when a value is not a valid, dialable phone number.
var ErrInvalidPhone = errors.New("invalid phone number")

// ErrInvalidEmail is returned when a value is not a single, well formed email address.
var ErrInvalidEmail = errors.New("invalid email address")

// NormalizePhone parses raw in the given region (DefaultRegion when empty) and returns it in E.164.
// It accepts local forms ("0712345678"), international forms ("+254712345678", "254712345678")
// and common separators. Numbers that parse but are not valid for their region are rejected.
func NormalizePhone(raw, region string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInvalidPhone
	}
	if region == "" {
		region = DefaultRegion
	}
	// "2547..." without a plus is a full international number, not a local one.
	if !strings.HasPrefix(raw, "+") && !strings.HasPrefix(raw, "0") && len(digits(raw)) > 10 {
		raw = "+" + digits(raw)
	}
	num, err := phonenumbers.Parse(raw, strings.ToUpper(region))
	if err != nil || !phonenumbers.IsValidNumber(num) {
		return "", ErrInvalidPhone
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}

// ValidPhone reports whether raw normalises to a valid E.164 number.
func ValidPhone(raw, region string) bool {
	_, err := NormalizePhone(raw, region)
	return err == nil
}

// SubscriberDigits is the last nine digits of a phone, the dedup key used across the platform so
// "0712..." and "+254712..." count as one number.
func SubscriberDigits(phone string) string {
	d := digits(phone)
	if len(d) > 9 {
		return d[len(d)-9:]
	}
	return d
}

// NormalizeEmail trims and lowercases an address and checks it is one well formed address with a
// dotted domain. Display names ("Titus <t@x.com>") are reduced to the bare address.
func NormalizeEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, ",;") {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return "", ErrInvalidEmail
	}
	e := strings.ToLower(addr.Address)
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return "", ErrInvalidEmail
	}
	domain := e[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", ErrInvalidEmail
	}
	return e, nil
}

// ValidEmailSyntax reports whether raw is a well formed address. It does no DNS lookup.
func ValidEmailSyntax(raw string) bool {
	_, err := NormalizeEmail(raw)
	return err == nil
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

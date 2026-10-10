// Package totp implements the time-based one-time passwords of RFC 6238, which
// is what every authenticator app speaks.
//
// It is written out rather than imported because the whole of it is HMAC-SHA1
// over a counter, and the standard library already has both halves. What is
// left is the truncation rule in section 5.4 of RFC 4226, which is eight lines.
//
// The parameters are fixed — SHA-1, six digits, thirty seconds — because those
// are what the authenticator apps default to and what the otpauth:// URI below
// tells them to use. Making them configurable would only produce combinations
// that nothing else implements.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // G505: HMAC-SHA1 is the primitive RFC 6238 names
	// and the only one every authenticator app implements. It is not a choice this
	// package could make differently, and nothing here hashes a password with it.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Period is the length of one time step.
	Period = 30 * time.Second
	// Digits is how many digits a code has.
	Digits = 6
	// SecretBytes is the size of a generated shared secret. RFC 4226 requires at
	// least 128 bits and recommends 160; twenty bytes is that recommendation.
	SecretBytes = 20
	// Skew is how many steps either side of now a code is accepted in. One step
	// covers a phone whose clock is half a minute out — which is the usual reason
	// a perfectly correct code gets rejected — without widening the window
	// enough to matter to anyone guessing.
	Skew = 1
)

// Issuer is what the authenticator app shows above the account name.
const Issuer = "Weatherglass"

// base32NoPad is the encoding authenticator apps expect: RFC 4648 base32
// without the "=" padding. Go's stdlib encoder pads by default, so this is a
// copy with the padding switched off rather than a trim after the fact.
var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a fresh shared secret, base32-encoded without padding.
func GenerateSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate totp secret: %w", err)
	}
	return base32NoPad.EncodeToString(buf), nil
}

// Normalise cleans up a secret the way a person would paste it: the apps show
// them in groups of four and in lower case, and a trailing "=" or two is easy to
// copy along with them.
func Normalise(secret string) string {
	s := strings.ToUpper(strings.TrimSpace(secret))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	return strings.TrimRight(s, "=")
}

// Step returns the time step a moment falls in.
func Step(t time.Time) int64 { return t.UTC().Unix() / int64(Period.Seconds()) }

// Code returns the code for a secret at a moment in time.
func Code(secret string, t time.Time) (string, error) {
	key, err := decode(secret)
	if err != nil {
		return "", err
	}
	return code(key, Step(t), Digits), nil
}

func decode(secret string) ([]byte, error) {
	key, err := base32NoPad.DecodeString(Normalise(secret))
	if err != nil {
		return nil, fmt.Errorf("totp secret is not valid base32: %w", err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("totp secret is empty")
	}
	return key, nil
}

// code is the whole algorithm. digits is a parameter rather than the constant
// so that the tests can check it against the eight-digit vectors printed in
// RFC 6238 — the published numbers are the only way to be sure this is RFC 6238
// and not merely something that looks like it. Nothing outside this package
// ever asks for eight digits.
func code(key []byte, counter int64, digits int) string {
	var msg [8]byte
	// The counter of RFC 4226 starts at zero and only ever climbs. One below zero
	// cannot come from a clock any authenticator agrees with, and wrapping it round
	// to the far end of the 64-bit range would turn a date in 1969 into a
	// plausible-looking code from the year 584 billion — so it is folded to the
	// first step, which is a code the apps compute too.
	if counter < 0 {
		counter = 0
	}
	binary.BigEndian.PutUint64(msg[:], uint64(counter))

	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 section 5.4: the low nibble of the last byte
	// picks which four bytes to read. It is what makes the code depend on the
	// whole digest rather than on a prefix of it, so two nearby counters do not
	// produce related codes.
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, v%mod)
}

// Verify checks a code against a secret and, when it matches, reports which
// time step it belonged to.
//
// It returns the step rather than a plain bool so that the caller can refuse a
// code it has already accepted and recorded. Without that, a code read over
// somebody's shoulder stays usable for the rest of its thirty seconds — and,
// counting the skew above, for the ninety the window spans.
func Verify(secret, input string, t time.Time) (int64, bool) {
	input = strings.TrimSpace(input)
	if len(input) != Digits {
		return 0, false
	}
	key, err := decode(secret)
	if err != nil {
		return 0, false
	}
	now := Step(t)
	for delta := int64(-Skew); delta <= Skew; delta++ {
		at := now + delta
		if hmac.Equal([]byte(code(key, at, Digits)), []byte(input)) {
			return at, true
		}
	}
	return 0, false
}

// URI builds the otpauth:// link an authenticator app reads, whether from a QR
// code or from a string somebody pasted.
func URI(account, secret string) string {
	q := url.Values{}
	q.Set("secret", Normalise(secret))
	q.Set("issuer", Issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(Digits))
	q.Set("period", strconv.Itoa(int(Period.Seconds())))
	// The label is "Issuer:account", and the colon has to survive: it is what
	// tells the app where the issuer name ends and the account begins.
	return "otpauth://totp/" + url.PathEscape(Issuer+":"+account) + "?" + q.Encode()
}

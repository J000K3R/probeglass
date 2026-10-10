package totp

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// The shared secret from the appendices of both RFCs: the twenty ASCII
// characters "12345678901234567890". Every published vector below is computed
// from exactly this, so the tests use it rather than a random secret.
const rfcSecret = "12345678901234567890"

func rfcKey(t *testing.T) []byte {
	t.Helper()
	return []byte(rfcSecret)
}

// TestHOTPVectors checks the raw truncation against the ten counters printed in
// RFC 4226 appendix D. These are the numbers the whole construction is defined
// by; if the truncation rule is subtly wrong, no other test here would notice,
// because a wrong-but-self-consistent implementation still round-trips.
func TestHOTPVectors(t *testing.T) {
	want := []string{
		"755224", "287082", "359152", "969429", "338314",
		"254676", "287922", "162583", "399871", "520489",
	}
	key := rfcKey(t)
	for counter, expected := range want {
		if got := code(key, int64(counter), 6); got != expected {
			t.Errorf("HOTP counter %d = %s, want %s", counter, got, expected)
		}
	}
}

// TestTOTPVectors checks the time-based form against the eight-digit table in
// RFC 6238 appendix B. Eight digits because that is what the RFC prints; the
// six-digit form the product actually uses is the same number modulo 10^6.
func TestTOTPVectors(t *testing.T) {
	vectors := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	key := rfcKey(t)
	for _, v := range vectors {
		at := time.Unix(v.unix, 0)
		if got := code(key, Step(at), 8); got != v.want {
			t.Errorf("TOTP at %d = %s, want %s", v.unix, got, v.want)
		}
		// The truncation the product ships is the same value, shortened.
		if got := code(key, Step(at), 6); got != v.want[2:] {
			t.Errorf("TOTP at %d (6 digits) = %s, want %s", v.unix, got, v.want[2:])
		}
	}
}

func TestCodeRoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	now := time.Now()
	got, err := Code(secret, now)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if len(got) != Digits {
		t.Errorf("code %q has %d digits, want %d", got, len(got), Digits)
	}
	step, ok := Verify(secret, got, now)
	if !ok {
		t.Fatalf("Verify rejected a code it had just produced")
	}
	if step != Step(now) {
		t.Errorf("Verify reported step %d, want %d", step, Step(now))
	}
}

// TestVerifyDriftWindow pins the skew: a code from the step before or after is
// accepted, one from two steps away is not.
func TestVerifyDriftWindow(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	for _, delta := range []time.Duration{-Period, 0, Period} {
		want, err := Code(secret, now.Add(delta))
		if err != nil {
			t.Fatalf("Code: %v", err)
		}
		if _, ok := Verify(secret, want, now); !ok {
			t.Errorf("a code from %v away was rejected, want accepted", delta)
		}
	}
	for _, delta := range []time.Duration{-2 * Period, 2 * Period, time.Hour} {
		code, err := Code(secret, now.Add(delta))
		if err != nil {
			t.Fatalf("Code: %v", err)
		}
		if _, ok := Verify(secret, code, now); ok {
			t.Errorf("a code from %v away was accepted, want rejected", delta)
		}
	}
}

func TestVerifyRejectsJunk(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	now := time.Now()
	good, err := Code(secret, now)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}

	for _, input := range []string{
		"",
		"12345",    // too short
		"1234567",  // too long
		"abcdef",   // not digits
		good + "0", // right code, wrong length
		"000000",   // almost never right, and wrong when it is
	} {
		if _, ok := Verify(secret, input, now); ok && input != good {
			t.Errorf("Verify accepted %q", input)
		}
	}
	if _, ok := Verify("not base32!!!", good, now); ok {
		t.Errorf("Verify accepted a code against an unreadable secret")
	}
}

func TestGenerateSecretShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		secret, err := GenerateSecret()
		if err != nil {
			t.Fatalf("GenerateSecret: %v", err)
		}
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
		if err != nil {
			t.Fatalf("secret %q is not unpadded base32: %v", secret, err)
		}
		if len(raw) != SecretBytes {
			t.Errorf("secret decodes to %d bytes, want %d", len(raw), SecretBytes)
		}
		if strings.Contains(secret, "=") {
			t.Errorf("secret %q carries padding", secret)
		}
		if seen[secret] {
			t.Errorf("GenerateSecret returned %q twice", secret)
		}
		seen[secret] = true
	}
}

// TestNormalise covers what people actually paste: the grouped, lower-case form
// the authenticator apps display.
func TestNormalise(t *testing.T) {
	const canonical = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, input := range []string{
		canonical,
		"gezdgnbvgy3tqojqgezdgnbvgy3tqojq",
		"GEZD GNBV GY3T QOJQ GEZD GNBV GY3T QOJQ",
		" " + canonical + " ",
		canonical + "==",
	} {
		if got := Normalise(input); got != canonical {
			t.Errorf("Normalise(%q) = %q, want %q", input, got, canonical)
		}
	}
}

// TestCodeAcceptsNormalisedSecret is the point of Normalise: a secret typed in
// by hand from what the app shows must produce the same code.
func TestCodeAcceptsNormalisedSecret(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	now := time.Now()
	want, err := Code(secret, now)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}

	grouped := strings.ToLower(secret)
	if got, err := Code(grouped, now); err != nil || got != want {
		t.Errorf("Code with a lower-case secret = %q (%v), want %q", got, err, want)
	}
}

func TestURIEscaping(t *testing.T) {
	uri := URI("ann a/b", "JBSWY3DPEHPK3PXP")
	for _, want := range []string{
		"otpauth://totp/",
		"secret=JBSWY3DPEHPK3PXP",
		"issuer=Weatherglass",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q is missing %q", uri, want)
		}
	}
	if strings.Contains(uri, "ann a/b") {
		t.Errorf("URI %q left the account unescaped", uri)
	}
	// The colon between issuer and account is structural: an app splits the
	// label on it to tell the two apart.
	if !strings.Contains(uri, "Weatherglass:") {
		t.Errorf("URI %q lost the issuer/account separator", uri)
	}
}

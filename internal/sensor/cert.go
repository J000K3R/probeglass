package sensor

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// The three probers that can meet a TLS certificate — http, tcp and tls — share
// the arithmetic and the wording here. They are the same question asked from
// three directions, and three hand-rolled answers would be three chances to
// disagree about how many days are left.

// certInfo is the leaf certificate of a finished handshake, reduced to what a
// probe message needs.
type certInfo struct {
	// NotAfter is when the certificate stops being valid.
	NotAfter time.Time
	// Days is whole days until NotAfter, rounded down towards minus infinity —
	// not towards zero, so that a certificate which ran out an hour ago reports
	// -1 rather than 0 and cannot be mistaken for one expiring today.
	Days int
}

// leafInfo returns the leaf certificate of a handshake, or false when the peer
// presented none. A server that offers no certificate at all is legal in TLS —
// PSK and anonymous cipher suites exist — so this is a fact to handle rather
// than an error to assume away.
func leafInfo(state tls.ConnectionState) (certInfo, bool) {
	if len(state.PeerCertificates) == 0 {
		return certInfo{}, false
	}
	leaf := state.PeerCertificates[0]
	return certInfo{
		NotAfter: leaf.NotAfter,
		Days:     daysUntil(leaf.NotAfter),
	}, true
}

// daysUntil is whole days from now until t, rounded down towards minus infinity.
func daysUntil(t time.Time) int {
	return int(math.Floor(time.Until(t).Hours() / 24))
}

// note is the short suffix a success line carries: " · cert 82d".
func (ci certInfo) note() string {
	return fmt.Sprintf(" · cert %dd", ci.Days)
}

// warning reports whether the certificate is worth failing the probe over:
// inside the warning window, or already expired.
//
// An expired certificate is a failure whatever the threshold says. With
// verification on, the handshake would have refused it before reaching here;
// with verification skipped it is the one thing this check exists to notice, and
// a threshold of zero must not be able to silence it.
func (ci certInfo) warning(warnDays int) bool {
	if ci.Days < 0 {
		return true
	}
	return warnDays > 0 && ci.Days <= warnDays
}

// describe is the sentence a failing probe reports. It is the same everywhere a
// certificate can fail, so that an operator learns one wording rather than
// three — and the expiry wording is identical to the one used when the handshake
// refused the certificate itself, because to the reader it is the same event.
func (ci certInfo) describe() string {
	if ci.Days < 0 {
		return expiredSentence(ci.NotAfter)
	}
	return fmt.Sprintf("TLS certificate expires in %d day(s)", ci.Days)
}

// expiredSentence names the day the certificate ran out.
func expiredSentence(notAfter time.Time) string {
	return fmt.Sprintf("TLS certificate expired on %s", notAfter.UTC().Format(time.DateOnly))
}

// describeValid is the sentence a successful probe reports about the
// certificate. The last day is called out rather than counted: "valid for 0
// day(s)" reads like a fault, and on the one day it is true it is worth saying
// plainly.
func (ci certInfo) describeValid() string {
	if ci.Days <= 0 {
		return "certificate expires today"
	}
	return fmt.Sprintf("certificate valid for %d day(s)", ci.Days)
}

// describeCertError names which of the three things went wrong when a handshake
// refused a certificate.
//
// They are told apart because the remedies have nothing in common: a name
// mismatch is a configuration mistake on one side or the other, expiry is a
// renewal somebody forgot, and an unknown authority is a private CA whose root
// is not installed — the everyday case in a homelab. "Not trusted" lumps all
// three together and helps with none.
//
// The second return value is false when the error is not about a certificate,
// so the caller can fall through to its own wording.
func describeCertError(err error) (string, bool) {
	// crypto/tls wraps these in *tls.CertificateVerificationError, which
	// implements Unwrap, so matching on the x509 types finds them either way.
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		// x509's own sentence names the names that are on the certificate,
		// which is the whole of what it takes to fix it.
		return strings.TrimPrefix(hostErr.Error(), "x509: "), true
	}

	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &invalidErr) {
		// The same reason code covers a certificate that has run out and one
		// that has not started yet, and the two need opposite reactions, so
		// which it is comes from the dates rather than from the reason.
		if invalidErr.Reason == x509.Expired && invalidErr.Cert != nil {
			now := time.Now()
			switch {
			case now.After(invalidErr.Cert.NotAfter):
				return expiredSentence(invalidErr.Cert.NotAfter), true
			case now.Before(invalidErr.Cert.NotBefore):
				return fmt.Sprintf("TLS certificate is not valid until %s",
					invalidErr.Cert.NotBefore.UTC().Format(time.DateOnly)), true
			}
		}
		// The remaining reasons are rare enough that x509's own sentence,
		// without its prefix, says more than a summary would.
		return strings.TrimPrefix(invalidErr.Error(), "x509: "), true
	}

	var unknownErr x509.UnknownAuthorityError
	if errors.As(err, &unknownErr) {
		return "TLS certificate is signed by an authority this host does not trust " +
			"(self-signed, or a private CA)", true
	}
	return "", false
}

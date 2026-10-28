package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// SaveCertificate records the certificate a sensor just saw, replacing whatever
// it saw the time before.
//
// Replacing rather than appending is the whole design: the inventory is a list
// of certificates that are in use now, and a table that kept every renewal would
// turn the question "what will run out next?" into a history lesson. The renewal
// itself is visible afterwards in the fingerprint, which changes.
func (s *Store) SaveCertificate(ctx context.Context, cert model.Certificate) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO certificates (sensor_id, subject, issuer, serial, fingerprint, dns_names,
		                          not_before, not_after, tls_version, warn_days, observed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(sensor_id) DO UPDATE SET
			subject     = excluded.subject,
			issuer      = excluded.issuer,
			serial      = excluded.serial,
			fingerprint = excluded.fingerprint,
			dns_names   = excluded.dns_names,
			not_before  = excluded.not_before,
			not_after   = excluded.not_after,
			tls_version = excluded.tls_version,
			warn_days   = excluded.warn_days,
			observed_at = excluded.observed_at`,
		cert.SensorID, cert.Subject, cert.Issuer, cert.Serial, cert.Fingerprint,
		strings.Join(cert.DNSNames, "\n"), fmtTime(cert.NotBefore), fmtTime(cert.NotAfter),
		cert.TLSVersion, cert.WarnDays, fmtTime(cert.ObservedAt))
	return err
}

// DeleteCertificate forgets what a sensor last saw.
//
// It is called when a probe completed without a certificate — a target that
// switched from https to http, a TCP sensor whose TLS was switched off — so that
// the inventory never lists a certificate nothing has seen since. A probe that
// failed before the handshake is not this: the certificate may still be in use,
// and a page that emptied itself on every timeout would be useless.
func (s *Store) DeleteCertificate(ctx context.Context, sensorID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM certificates WHERE sensor_id = ?`, sensorID)
	return err
}

// Certificates returns every certificate the installation currently knows
// about, soonest to expire first.
//
// The join is what makes this the inventory rather than a table dump: a row
// whose sensor has gone cannot appear, and each certificate carries the name and
// place of the sensor that saw it so the page can say where it is.
func (s *Store) Certificates(ctx context.Context) ([]model.Certificate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.sensor_id, s.name, s.type, s.icon, s.target,
		       s.grp, COALESCE(s.site, ''),
		       c.subject, c.issuer, c.serial, c.fingerprint, c.dns_names,
		       c.not_before, c.not_after, c.tls_version, c.warn_days, c.observed_at
		FROM certificates c
		JOIN sensors s ON s.id = c.sensor_id
		ORDER BY c.not_after ASC, s.name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Certificate{}
	for rows.Next() {
		var (
			c         model.Certificate
			dnsNames  string
			notBefore string
			notAfter  string
			observed  string
		)
		if err := rows.Scan(&c.SensorID, &c.SensorName, &c.SensorType, &c.SensorIcon,
			&c.Target, &c.Group, &c.Site, &c.Subject, &c.Issuer, &c.Serial,
			&c.Fingerprint, &dnsNames, &notBefore, &notAfter, &c.TLSVersion,
			&c.WarnDays, &observed); err != nil {
			return nil, err
		}
		c.NotBefore = parseTime(notBefore)
		c.NotAfter = parseTime(notAfter)
		c.ObservedAt = parseTime(observed)
		if dnsNames != "" {
			c.DNSNames = strings.Split(dnsNames, "\n")
		}
		// Whole days down towards minus infinity, so a certificate that ran out
		// an hour ago says -1 and not "today". Worked out here rather than stored
		// because a stored number is wrong the next morning.
		c.DaysLeft = int(math.Floor(time.Until(c.NotAfter).Hours() / 24))
		out = append(out, c)
	}
	return out, rows.Err()
}

// Certificate returns what one sensor last saw, or sql.ErrNoRows.
func (s *Store) Certificate(ctx context.Context, sensorID int64) (*model.Certificate, error) {
	var (
		c         model.Certificate
		dnsNames  string
		notBefore string
		notAfter  string
		observed  string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT sensor_id, subject, issuer, serial, fingerprint, dns_names,
		       not_before, not_after, tls_version, warn_days, observed_at
		FROM certificates WHERE sensor_id = ?`, sensorID).Scan(
		&c.SensorID, &c.Subject, &c.Issuer, &c.Serial, &c.Fingerprint, &dnsNames,
		&notBefore, &notAfter, &c.TLSVersion, &c.WarnDays, &observed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	c.NotBefore = parseTime(notBefore)
	c.NotAfter = parseTime(notAfter)
	c.ObservedAt = parseTime(observed)
	if dnsNames != "" {
		c.DNSNames = strings.Split(dnsNames, "\n")
	}
	c.DaysLeft = int(math.Floor(time.Until(c.NotAfter).Hours() / 24))
	return &c, nil
}

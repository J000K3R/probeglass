package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// TestCertificatesAreOneRowPerSensorSortedByExpiry checks the two properties the
// inventory page depends on: a renewal replaces what the sensor saw rather than
// adding a second row, and the list comes back soonest-to-expire first.
func TestCertificatesAreOneRowPerSensorSortedByExpiry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	nas := mustSensor(t, st, model.Sensor{
		Name: "NAS", Type: model.SensorTLS, Target: "nas.local:443", Group: "Homelab",
	})
	web := mustSensor(t, st, model.Sensor{
		Name: "Website", Type: model.SensorHTTP, Target: "https://example.org",
		Icon: "builtin:cloud",
	})

	soon := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second)
	later := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)

	if err := st.SaveCertificate(ctx, model.Certificate{
		SensorID: nas.ID, Subject: "CN=nas.local", Issuer: "CN=Homelab CA",
		NotBefore: later.Add(-90 * 24 * time.Hour), NotAfter: soon,
		DNSNames: []string{"nas.local", "*.nas.local"}, WarnDays: 14,
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save NAS certificate: %v", err)
	}
	if err := st.SaveCertificate(ctx, model.Certificate{
		SensorID: web.ID, Subject: "CN=example.org", Issuer: "CN=R3",
		NotAfter: later, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save website certificate: %v", err)
	}

	list, err := st.Certificates(ctx)
	if err != nil {
		t.Fatalf("list certificates: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("the inventory has %d rows, want 2: %+v", len(list), list)
	}
	if list[0].SensorName != "NAS" || !list[0].NotAfter.Equal(soon) {
		t.Errorf("the first row is %+v, want the certificate expiring in ten days", list[0])
	}
	if list[0].Group != "Homelab" || list[0].Target != "nas.local:443" {
		t.Errorf("the row lost its sensor: %+v", list[0])
	}
	if len(list[0].DNSNames) != 2 {
		t.Errorf("the SANs were not stored: %+v", list[0].DNSNames)
	}
	if list[0].DaysLeft < 8 || list[0].DaysLeft > 10 {
		t.Errorf("DaysLeft = %d, want about 10", list[0].DaysLeft)
	}

	// A renewal — the same sensor, a later expiry, a new fingerprint — replaces
	// the row rather than adding one.
	if err := st.SaveCertificate(ctx, model.Certificate{
		SensorID: nas.ID, Subject: "CN=nas.local", Issuer: "CN=Homelab CA",
		Fingerprint: "abc123", NotAfter: later.Add(30 * 24 * time.Hour),
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("renew NAS certificate: %v", err)
	}
	list, err = st.Certificates(ctx)
	if err != nil {
		t.Fatalf("list certificates after renewal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("the renewal added a row: %d rows", len(list))
	}
	if list[1].SensorName != "NAS" || list[1].Fingerprint != "abc123" {
		t.Errorf("the renewed certificate is %+v, want the NAS last", list[1])
	}
}

// TestDeletingASensorForgetsItsCertificate covers both ways a row goes away: the
// explicit one used when a probe stops seeing a certificate, and the one that
// follows a sensor being deleted.
func TestDeletingASensorForgetsItsCertificate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	c := mustSensor(t, st, model.Sensor{
		Name: "gateway", Type: model.SensorTLS, Target: "gateway.local",
	})
	save := func() {
		t.Helper()
		if err := st.SaveCertificate(ctx, model.Certificate{
			SensorID: c.ID, Subject: "CN=gateway.local",
			NotAfter: time.Now().Add(48 * time.Hour), ObservedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("save certificate: %v", err)
		}
	}

	save()
	if _, err := st.Certificate(ctx, c.ID); err != nil {
		t.Fatalf("read certificate: %v", err)
	}
	if err := st.DeleteCertificate(ctx, c.ID); err != nil {
		t.Fatalf("delete certificate: %v", err)
	}
	if _, err := st.Certificate(ctx, c.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the certificate survived the delete: %v", err)
	}

	save()
	if err := st.DeleteSensor(ctx, c.ID); err != nil {
		t.Fatalf("delete sensor: %v", err)
	}
	list, err := st.Certificates(ctx)
	if err != nil {
		t.Fatalf("list certificates: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("a deleted sensor still has a certificate in the inventory: %+v", list)
	}
}

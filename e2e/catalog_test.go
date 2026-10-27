// Package e2e — applying a sensor template.
//
// The catalogue's second half is not a form of its own: choosing a template asks
// for the device and its credentials, shows the checks it would add, and creates
// the ticked ones as ordinary sensors. What only a browser can show is that the
// whole path works — the template reaches the dialog, the checkboxes are the
// decision, and the sensors that appear afterwards are the ones that were ticked.
package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestASensorTemplateCreatesItsTickedChecks(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal[open] [data-type-open]`),
			chromedp.Click(`#modal [data-type-open]`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#modal-catalog [data-catalog-tab="templates"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
			chromedp.Click(`#modal-catalog [data-catalog-tab="templates"]`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#modal-catalog [data-template-id="qnap"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
			chromedp.Click(`#modal-catalog [data-template-id="qnap"]`),
			chromedp.WaitVisible(`#catalog-name`),
		)
	}); err != nil {
		capture(t, ctx, dir, "template-dialog")
		t.Fatalf("could not open the QNAP template: %v", err)
	}

	// Seven checks are proposed. All but CPU temperature and uptime are unticked,
	// so exactly those two must be made.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.SetValue(`#catalog-name`, "Test NAS"),
			chromedp.SetValue(`#catalog-target`, "192.0.2.10"),
			chromedp.SetValue(`#catalog-group`, "Test NAS"),
			chromedp.Click(`#catalog-body [data-catalog-check="system_temp"]`),
			chromedp.Click(`#catalog-body [data-catalog-check="disk1_temp"]`),
			chromedp.Click(`#catalog-body [data-catalog-check="disk2_temp"]`),
			chromedp.Click(`#catalog-body [data-catalog-check="disk3_temp"]`),
			chromedp.Click(`#catalog-body [data-catalog-check="disk4_temp"]`),
			chromedp.Click(`#catalog-add`),
			chromedp.WaitNotPresent(`#modal-catalog[open]`),
			chromedp.WaitNotPresent(`#modal[open]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "template-not-applied")
		t.Fatalf("could not apply the template: %v", err)
	}

	eventually(t, ctx, `fetch('/api/sensors').then(r => r.json())
		.then(list => list.filter((s) => s.name.startsWith('Test NAS · ')).length === 2)`,
		"the template did not create its two ticked sensors", dir, "template-sensors", 15*time.Second)

	type created struct {
		Name    string         `json:"name"`
		Type    string         `json:"type"`
		Group   string         `json:"group"`
		Target  string         `json:"target"`
		Options map[string]any `json:"options"`
	}
	list := readAsync[[]created](t, ctx,
		`fetch('/api/sensors').then(r => r.json())
			.then(list => list.filter((s) => s.name.startsWith('Test NAS · ')))`,
		"the sensors the template created")
	if len(list) != 2 {
		t.Fatalf("the template created %d sensor(s), want 2: %+v", len(list), list)
	}
	names := make([]string, 0, len(list))
	for _, sensor := range list {
		names = append(names, sensor.Name)
		if sensor.Type != "snmp" {
			t.Errorf("%s has type %q, want snmp", sensor.Name, sensor.Type)
		}
		if sensor.Group != "Test NAS" || sensor.Target != "192.0.2.10" {
			t.Errorf("%s is in group %q at %q, want Test NAS at 192.0.2.10",
				sensor.Name, sensor.Group, sensor.Target)
		}
		if oid, ok := sensor.Options["oid"].(string); !ok || !strings.HasPrefix(oid, "1.3.6.1.") {
			t.Errorf("%s has no SNMP oid: %v", sensor.Name, sensor.Options)
		}
	}
	joined := strings.Join(names, "\n")
	if strings.Contains(joined, "Disk 1 temperature") || strings.Contains(joined, "System temperature") {
		t.Errorf("an unticked check was created anyway:\n%s", joined)
	}
	if !strings.Contains(joined, "CPU temperature") || !strings.Contains(joined, "Uptime") {
		t.Errorf("the created sensors are not the template's ticked checks:\n%s", joined)
	}
}

func TestTheTemplateCatalogueCanBeFiltered(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal[open] [data-type-open]`),
			chromedp.Click(`#modal [data-type-open]`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#modal-catalog [data-catalog-tab="templates"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
			chromedp.Click(`#modal-catalog [data-catalog-tab="templates"]`),
			chromedp.Poll[chromedp.Void](
				`document.querySelectorAll('#template-grid .template-card').length >= 10`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "template-grid")
		t.Fatalf("could not open the template grid: %v", err)
	}

	setQuery := func(term string) {
		t.Helper()
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`(() => {
			const input = document.querySelector('#modal-catalog [data-template-query]');
			input.value = '`+term+`';
			input.dispatchEvent(new Event('input', { bubbles: true }));
			return true;
		})()`)); err != nil {
			t.Fatalf("could not search for %q: %v", term, err)
		}
	}
	cardNames := func() string {
		t.Helper()
		return read[string](t, ctx, `[...document.querySelectorAll('#template-grid .template-card strong')]
			.map((n) => n.textContent).join('|')`, "the template cards")
	}

	setQuery("qnap")
	eventually(t, ctx, `document.querySelectorAll('#template-grid .template-card').length === 1`,
		"searching for qnap did not narrow the grid", dir, "template-search", 10*time.Second)
	if names := cardNames(); !strings.Contains(names, "QNAP") {
		t.Errorf("searching for qnap shows %q", names)
	}

	// Clear the search, then narrow by vendor and by system. The two chips combine.
	setQuery("")
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`(() => {
		const chip = document.querySelector('#modal-catalog [data-filter-vendor="MikroTik"]');
		if (!chip) return false;
		chip.click();
		return true;
	})()`)); err != nil {
		t.Fatalf("could not choose the MikroTik vendor: %v", err)
	}
	eventually(t, ctx, `document.querySelectorAll('#template-grid .template-card').length === 1`,
		"the vendor filter did not narrow the grid", dir, "template-vendor", 10*time.Second)
	if names := cardNames(); !strings.Contains(names, "MikroTik") {
		t.Errorf("the MikroTik vendor filter shows %q", names)
	}

	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`(() => {
		const chip = document.querySelector('#modal-catalog [data-filter-system="nas"]');
		if (!chip) return false;
		chip.click();
		return true;
	})()`)); err != nil {
		t.Fatalf("could not choose the NAS system: %v", err)
	}
	eventually(t, ctx, `document.querySelector('#template-grid .template-card') === null`,
		"a vendor and a system with no template in common did not empty the grid",
		dir, "template-empty", 10*time.Second)
}

// TestAnApplicationTemplateCreatesHTTPChecks applies the MOS template: it has an
// API token instead of SNMP credentials, target patterns that add the /api/v1
// path, and one optional UPS check that must start unticked.
func TestAnApplicationTemplateCreatesHTTPChecks(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal[open] [data-type-open]`),
			chromedp.Click(`#modal [data-type-open]`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#modal-catalog [data-catalog-tab="templates"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
			chromedp.Click(`#modal-catalog [data-catalog-tab="templates"]`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#modal-catalog [data-template-id="mos"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
			chromedp.Click(`#modal-catalog [data-template-id="mos"]`),
			chromedp.WaitVisible(`#catalog-credential`),
		)
	}); err != nil {
		capture(t, ctx, dir, "mos-template")
		t.Fatalf("could not open the MOS template: %v", err)
	}

	state := read[map[string]any](t, ctx, `(() => ({
		snmpCredentialFields: document.querySelectorAll('#catalog-options').length,
		tokenField: document.querySelector('#catalog-credential')?.type || '',
		upsChecked: document.querySelector('[data-catalog-check="ups"]')?.checked ?? null,
		loadChecked: document.querySelector('[data-catalog-check="system_load"]')?.checked ?? null,
		checks: document.querySelectorAll('#catalog-body [data-catalog-check]').length,
	}))()`, "the MOS template form")
	if state["snmpCredentialFields"].(float64) != 0 {
		t.Errorf("the MOS template still asks for SNMP credentials: %v", state)
	}
	if state["tokenField"] != "password" {
		t.Errorf("the API token field is %v, want a password field", state["tokenField"])
	}
	if state["upsChecked"] != false || state["loadChecked"] != true {
		t.Errorf("the optional UPS check is checked=%v and system load is checked=%v; want false and true",
			state["upsChecked"], state["loadChecked"])
	}
	if state["checks"].(float64) != 5 {
		t.Errorf("the MOS template offers %v checks, want 5", state["checks"])
	}

	target := strings.TrimPrefix(base, "http://")
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.SetValue(`#catalog-name`, "MOS Test"),
			chromedp.SetValue(`#catalog-target`, target),
			chromedp.SetValue(`#catalog-group`, "MOS Test"),
			chromedp.SetValue(`#catalog-credential`, "test-token"),
			chromedp.Click(`#catalog-add`),
			chromedp.WaitNotPresent(`#modal-catalog[open]`),
			chromedp.WaitNotPresent(`#modal[open]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "mos-not-applied")
		t.Fatalf("could not apply the MOS template: %v", err)
	}

	eventually(t, ctx, `fetch('/api/sensors').then(r => r.json())
		.then(list => list.filter((s) => s.name.startsWith('MOS Test · ')).length === 4)`,
		"the MOS template did not create four sensors", dir, "mos-sensors", 15*time.Second)

	type created struct {
		Name    string         `json:"name"`
		Type    string         `json:"type"`
		Target  string         `json:"target"`
		Options map[string]any `json:"options"`
	}
	list := readAsync[[]created](t, ctx,
		`fetch('/api/sensors').then(r => r.json())
			.then(list => list.filter((s) => s.name.startsWith('MOS Test · ')))`,
		"the sensors the MOS template created")
	if len(list) != 4 {
		t.Fatalf("the MOS template created %d sensor(s), want 4: %+v", len(list), list)
	}
	for _, sensor := range list {
		switch sensor.Type {
		case "http":
			if !strings.HasPrefix(sensor.Target, "http://"+target+"/api/v1/") {
				t.Errorf("%s has target %q, want the /api/v1 path on the entered address",
					sensor.Name, sensor.Target)
			}
			if sensor.Options["bearer_token"] != "__stored__" {
				t.Errorf("%s does not carry the API token (masked): %v", sensor.Name, sensor.Options)
			}
		case "ping":
			if sensor.Target != strings.Split(target, ":")[0] {
				t.Errorf("%s has target %q, want the bare host", sensor.Name, sensor.Target)
			}
		default:
			t.Errorf("%s has unexpected type %q", sensor.Name, sensor.Type)
		}
	}
}

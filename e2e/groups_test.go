package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestAGroupCanBeDeletedWithItsSensors is the dialog path: a group with two
// sensors, "delete all", and both the row and the sensors are gone.
func TestAGroupCanBeDeletedWithItsSensors(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`(async () => {
		const res = await fetch('/api/sensors/bulk', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ sensors: [
				{ name: 'Delete one', type: 'ping', target: '192.0.2.10', interval: 60, group: 'DeleteMe' },
				{ name: 'Delete two', type: 'ping', target: '192.0.2.11', interval: 60, group: 'DeleteMe' }
			] }),
		});
		return res.ok;
	})()`, chromedp.EvalAwaitPromise)); err != nil {
		t.Fatalf("could not create the group's sensors: %v", err)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#groups-panel tr[data-name="DeleteMe"]`),
			chromedp.Click(`#groups-panel tr[data-name="DeleteMe"] [data-action="group-forget"]`),
			chromedp.WaitVisible(`#modal-group-delete[open]`),
			chromedp.Evaluate[chromedp.Void](`(() => {
				const radio = document.querySelector('#modal-group-delete input[name="group-delete-mode"][value="all"]');
				radio.closest('label').click();
			})()`),
			chromedp.Click(`#group-delete-confirm`),
			chromedp.WaitNotPresent(`#modal-group-delete[open]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "group-delete")
		t.Fatalf("could not delete the group: %v", err)
	}

	eventually(t, ctx, `fetch('/api/sensors').then(r => r.json())
		.then(list => !list.some((s) => s.group === 'DeleteMe'))`,
		"the group deletion left its sensors behind", dir, "group-delete-sensors", 15*time.Second)
	eventually(t, ctx, `document.querySelector('#groups-panel tr[data-name="DeleteMe"]') === null`,
		"the deleted group is still in the settings table", dir, "group-delete-row", 15*time.Second)
}

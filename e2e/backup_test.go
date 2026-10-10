// Package e2e — the Download button on the backup panel.
//
// The API test next door proves the route answers with the right headers and the
// right bytes. What it cannot say is whether the button on the settings page asks
// it for the right thing: the scope and the period are two menus on that page, and
// a download that quietly ignores them hands somebody a configuration file while
// they believe they have taken their history with them. That is the failure worth a
// browser, because it is silent — the file arrives, it is called a backup, and the
// gap is only discovered on the day it is needed.
//
// The download itself is intercepted rather than allowed to happen. A real one
// would be written into a directory this test does not own and could not clean up,
// and what is being checked is what the page asked for and what it was handed, not
// what the browser then did with the bytes.
package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// backupDownload is everything observable about the one request the panel makes.
type backupDownload struct {
	URL   string `json:"url"`   // what the page asked for
	Dispo string `json:"dispo"` // the server's Content-Disposition
	Type  string `json:"type"`  // the server's Content-Type
	// Filename is the name the page put on the file it handed the browser. It is read
	// off the <a download> rather than assumed, because the whole point of taking the
	// name from the header is that the page does not decide it.
	Filename string `json:"filename"`
	Body     string `json:"body"`
}

// installBackupRecorder patches the page so the download is observed instead of
// performed: the fetch to /api/backup is watched, and the anchor click that starts
// the download is swallowed.
//
// Swallowing every anchor that carries `download` is safe here. This panel is the
// only place in the dashboard that builds one, so nothing else on the page navigates
// through an anchor of that shape.
const installBackupRecorder = `(() => {
  window.__backup = null;
  const realFetch = window.fetch.bind(window);
  window.fetch = async (...args) => {
    const url = String(args[0]);
    const response = await realFetch(...args);
    if (!url.startsWith('/api/backup')) return response;
    const seen = {
      url,
      dispo: response.headers.get('Content-Disposition') || '',
      type: response.headers.get('Content-Type') || '',
      filename: '',
      body: '',
    };
    window.__backup = seen;
    response.clone().text().then(t => { seen.body = t; }).catch(() => {});
    return response;
  };
  const realClick = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function () {
    if (!this.hasAttribute('download')) return realClick.call(this);
    if (window.__backup) window.__backup.filename = this.download;
  };
})()`

// TestTheBackupButtonAsksForWhatTheMenusSay presses Download with the two menus set
// to something other than their defaults, and reads back what left the page.
//
// It runs against the demo data set, so that "with history" is a backup that has some
// and the file is a real document rather than an empty envelope.
func TestTheBackupButtonAsksForWhatTheMenusSay(t *testing.T) {
	browser := findBrowser(t)
	bin := buildBinary(t)
	data := t.TempDir()
	seedDemo(t, bin, data)
	base := startServerIn(t, bin, data)

	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("backup"),
			chromedp.WaitVisible(`[data-panel="backup"] #backup-scope`),
		)
	}); err != nil {
		capture(t, ctx, dir, "backup-panel")
		t.Fatalf("the settings page never offered the backup panel: %v", err)
	}

	// History rather than the default configuration, and seven days rather than the
	// default thirty, so that a button which read neither menu cannot pass by
	// accident — it would ask for the defaults and be caught.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](installBackupRecorder),
			chromedp.Evaluate[chromedp.Void](`
				(() => {
				  const scope = document.querySelector('#backup-scope');
				  scope.value = 'history';
				  scope.dispatchEvent(new Event('change', { bubbles: true }));
				  const days = document.querySelector('#backup-days');
				  days.value = '7';
				  days.dispatchEvent(new Event('change', { bubbles: true }));
				})()`),
			chromedp.Click(`[data-action="backup-download"]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "backup-click")
		t.Fatalf("could not press Download: %v", err)
	}

	eventually(t, ctx, `window.__backup !== null && window.__backup.body !== ''`,
		"pressing Download never produced a backup", dir, "backup-no-file", 20*time.Second)
	got := read[backupDownload](t, ctx, `window.__backup`, "the download")

	// --- what was asked for -------------------------------------------------

	const wantURL = "/api/backup?scope=history&days=7"
	if got.URL != wantURL {
		capture(t, ctx, dir, "backup-wrong-request")
		t.Errorf("the button requested %q; the two menus on the panel say %q. A backup "+
			"that ignores them is a file that is not the one its name promises",
			got.URL, wantURL)
	}

	// --- what was handed back -----------------------------------------------

	if !strings.HasPrefix(got.Type, "application/json") {
		t.Errorf("the backup arrived as %q", got.Type)
	}
	// The name is checked here rather than in the API test because this is the end of
	// the journey: the header is what the browser is told, and this is the page reading
	// it back out. `attachment` is what keeps a browser from offering to display the
	// file instead of saving it, and everything in it is a credential.
	if !strings.HasPrefix(got.Dispo, "attachment;") {
		t.Errorf("the backup was served with Content-Disposition %q; it has to be an "+
			"attachment, or a browser is free to open it in the window instead of "+
			"saving it", got.Dispo)
	}
	serverName := strings.TrimSuffix(strings.TrimPrefix(got.Dispo, `attachment; filename="`), `"`)
	if !strings.HasPrefix(serverName, "weatherglass-backup-") || !strings.HasSuffix(serverName, ".json") {
		t.Errorf("the server named the file %q, which is not a backup name", got.Dispo)
	}
	if got.Filename != serverName {
		t.Errorf("the file was saved as %q while the server called it %q; the page is "+
			"meant to take the name from the header rather than invent one", got.Filename, serverName)
	}

	// --- and what was inside it ---------------------------------------------

	var file struct {
		Format  string `json:"format"`
		Scope   string `json:"scope"`
		Days    int    `json:"days"`
		Sensors []any  `json:"sensors"`
		Hourly  []any  `json:"results_hourly"`
		Daily   []any  `json:"results_daily"`
	}
	if err := json.Unmarshal([]byte(got.Body), &file); err != nil {
		capture(t, ctx, dir, "backup-not-json")
		t.Fatalf("the downloaded file is not the JSON it is named as: %v", err)
	}
	if file.Format != "weatherglass-backup" {
		t.Errorf("the file's format marker is %q; the restore path refuses anything else, "+
			"so a backup that says something different cannot be put back", file.Format)
	}
	if file.Scope != "history" || file.Days != 7 {
		t.Errorf("the file says scope=%q days=%d, but the menus said history and 7 — "+
			"the request and the content disagree", file.Scope, file.Days)
	}
	// History means the rollups come with it. That is the whole difference between the
	// two scopes, and it is the reason somebody picks this one.
	if len(file.Hourly) == 0 {
		t.Errorf("a backup taken with history carries no hourly figures, so nothing "+
			"that survives the restore was actually saved (sensors: %d)", len(file.Sensors))
	}

	if name := read[string](t, ctx, `document.querySelector('#backup-error').textContent`, "error"); name != "" {
		t.Errorf("the panel reported %q after a download it also completed", name)
	}
}

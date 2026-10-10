// Package e2e — the recipe shown under a channel type's fields.
//
// The recipes are written in Go, beside the field descriptions they talk about,
// and reach the dialog over /api/channel-types. Nothing in the API can be wrong
// about this and still be caught here: the route answers with the list whether or
// not anybody draws it, and the failure this guards is that the person setting up
// their first Telegram bot is shown a form of four boxes and no way to fill them
// in.
//
// So this test reads the dialog and holds what it shows against what the API
// answered. That is deliberately a comparison and not an assertion about wording:
// the words belong to the Go tables, and pinning them here would mean a test that
// fails when somebody improves a sentence.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// channelDialog is the part of the channel editor this test is about.
type channelDialog struct {
	Shown   bool     `json:"shown"`  // the recipe block is on screen
	Behind  string   `json:"behind"` // "", "closed" or "missing": where the block sits
	Heading string   `json:"heading"`
	Steps   []string `json:"steps"`
}

// readChannelDialog reads the recipe block, and says whether it is actually
// visible. A block inside a folded <details> is in the document and cannot be
// read, which is the difference between a recipe and a recipe nobody sees.
const readChannelDialog = `(() => {
  const box = document.querySelector('#channel-fields .channel-setup');
  if (!box) return { shown: false, behind: 'missing', heading: '', steps: [] };
  const fold = box.closest('details');
  const behind = fold && !fold.open ? 'closed' : '';
  const h = box.querySelector('h4');
  return {
    shown: box.getBoundingClientRect().height > 0,
    behind,
    heading: h ? h.textContent.trim() : '',
    steps: [...box.querySelectorAll('li')].map(li => li.textContent.trim()),
  };
})()`

// channelTypes is the API's own answer, which is what the dialog was built from.
const channelTypesScript = `fetch('/api/channel-types')
  .then(r => r.json())
  .then(list => Object.fromEntries(list.map(t => [t.type, t.setup || []])))`

func TestChannelDialogShowsTheRecipe(t *testing.T) {
	browser := findBrowser(t)
	bin := buildBinary(t)
	base := startServer(t, bin)

	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	var recipes map[string][]string
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/channels'`),
			chromedp.WaitVisible(`#new-channel`),
			chromedp.Click(`#new-channel`),
			chromedp.WaitVisible(`#channel-fields .channel-setup ol li`),
		)
	}); err != nil {
		capture(t, ctx, dir, "channels-dialog")
		t.Fatalf("could not open the channel editor: %v", err)
	}
	if err := withTimeout(ctx, func(tctx context.Context) error {
		var err error
		recipes, err = chromedp.Run(tctx,
			chromedp.Evaluate[map[string][]string](channelTypesScript, chromedp.EvalAwaitPromise))
		return err
	}); err != nil {
		capture(t, ctx, dir, "channels-api")
		t.Fatalf("could not read the channel types the dialog was built from: %v", err)
	}

	// Each type is checked by switching the dialog to it and holding what appears
	// against what the API says that type's recipe is. The switch is what makes
	// this more than a test of the first render: the fields and the recipe are
	// redrawn together, and a stale recipe left over from the previous type would
	// be advice about boxes that are no longer on screen.
	for _, typ := range []string{"telegram", "ntfy", "webhook"} {
		want, ok := recipes[typ]
		if !ok || len(want) == 0 {
			t.Fatalf("the API describes no setup for %q, so this test has nothing to hold the dialog against", typ)
		}

		if typ != "telegram" {
			if err := withTimeout(ctx, func(tctx context.Context) error {
				return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`
					(() => {
					  const select = document.querySelector('#channel-type');
					  select.value = `+jsQuote(typ)+`;
					  select.dispatchEvent(new Event('change'));
					})()`))
			}); err != nil {
				capture(t, ctx, dir, "channels-"+typ)
				t.Fatalf("could not switch the channel type to %q: %v", typ, err)
			}
			// Waited for by content rather than by a sleep: the redraw is synchronous
			// with the change event, but the first step's text is what proves it.
			eventually(t, ctx,
				`(document.querySelector('#channel-fields .channel-setup li') || {}).textContent === `+jsQuote(want[0]),
				"switching the channel type to "+typ+" left the previous type's recipe in place",
				dir, "channels-"+typ+"-not-redrawn", 10*time.Second)
		}

		got := read[channelDialog](t, ctx, readChannelDialog, "the channel recipe")
		if !got.Shown {
			capture(t, ctx, dir, "channels-"+typ+"-hidden")
			t.Errorf("%s: the recipe is not on screen (%s)", typ, got.Behind)
			continue
		}
		if got.Behind != "" {
			capture(t, ctx, dir, "channels-"+typ+"-folded")
			t.Errorf("%s: the recipe is behind a closed fold; whoever needs it is exactly whoever "+
				"does not know to open it", typ)
		}
		if len(got.Steps) != len(want) {
			t.Errorf("%s: the dialog shows %d step(s), the API sends %d", typ, len(got.Steps), len(want))
			continue
		}
		for i, step := range got.Steps {
			if step != want[i] {
				t.Errorf("%s: step %d on screen is %q, the API sent %q", typ, i+1, step, want[i])
			}
		}
	}
}

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The example data is the one part of the dashboard nobody asked for, so two
// things about it have to hold: it says so on every card, and removing it is not
// a one-way door.
//
// This test walks the whole loop a person can walk — the sample sensors a fresh
// installation writes by itself, the removal in Settings, and the button that
// puts them back — and it is the last step that is new. Before it existed, the
// only way out of the removal was to start over.

// exampleCards reads the dashboard, not the API: the badge is what a person
// reads and data-origin is what the stylesheet keys on, and the marking only
// means something while the two agree.
const exampleCards = `(() => {
  const cards = [...document.querySelectorAll('#groups .card')];
  const examples = cards.filter(c => c.dataset.origin === 'example');
  return {
    total: cards.length,
    examples: examples.length,
    marked: examples.filter(c => c.querySelector('.badge-example') !== null).length,
  };
})()`

// markedExamplesReady is the polling predicate for "the cards are on screen and
// every example among them carries its badge".
const markedExamplesReady = `(() => {
  const cards = [...document.querySelectorAll('#groups .card')];
  const examples = cards.filter(c => c.dataset.origin === 'example');
  return examples.length > 0 && examples.every(c => c.querySelector('.badge-example') !== null);
})()`

// noCardsAtAll is the state after the removal, and it is worth being strict
// about: a card left behind would mean the removal took the wrong set.
const noCardsAtAll = `document.querySelectorAll('#groups .card').length === 0`

// exampleCounts asks the API rather than the screen, because the part of the
// dataset that matters most — a week of invented history — has no place on a
// card.
const exampleCounts = `fetch('/api/examples').then(r => r.json())`

// exampleCardView is what exampleCards returns.
type exampleCardView struct {
	Total    int `json:"total"`
	Examples int `json:"examples"`
	Marked   int `json:"marked"`
}

// exampleCountView is what /api/examples returns.
type exampleCountView struct {
	Sensors   int `json:"sensors"`
	Results   int `json:"results"`
	Incidents int `json:"incidents"`
}

func TestExampleDataCanBePutBack(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	defer cancelAlloc()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("could not start the browser: %v", err)
	}

	//nolint:misspell // "screenshotDir" is the spelling GitHub uses for the uploaded files
	screenshotDir := os.Getenv("PROBEGLASS_E2E_ARTIFACTS")
	if screenshotDir == "" {
		screenshotDir = t.TempDir()
	}

	// --- phase 1: a fresh installation and the examples it starts with --------

	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(base+"/"),
			chromedp.WaitVisible(`#auth-password`),
			chromedp.SendKeys(`#auth-username`, testUser),
			chromedp.SendKeys(`#auth-password`, testPassword),
			chromedp.SendKeys(`#auth-confirm`, testPassword),
			chromedp.Click(`#auth-submit`),
			chromedp.WaitVisible(`#auth-mode-step`),
			chromedp.Click(`input[name="ui-mode"][value="advanced"]`),
			chromedp.Click(`#auth-submit`),
			// The tour covers the dashboard on a fresh install. This test is about
			// the examples underneath it, so it is dismissed.
			chromedp.WaitVisible(`.tour-card`),
			chromedp.Click(`.tour-card [data-tour="skip"]`),
			chromedp.WaitNotPresent(`.tour-card`),
			// The examples the setup writes by itself, which is the half of the
			// user's decision that says "the lean ones at first start".
			chromedp.Poll[chromedp.Void](markedExamplesReady, chromedp.WithPollingTimeout(20*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "examples-not-marked")
		t.Fatalf("the example sensors a fresh install creates did not appear marked "+
			"on the dashboard: %v", err)
	}

	fresh, err := chromedp.Run(ctx, chromedp.Evaluate[exampleCardView](exampleCards))
	if err != nil {
		t.Fatalf("could not read the example cards: %v", err)
	}
	if fresh.Examples != 4 {
		t.Errorf("a fresh installation shows %d example sensor(s), want the four the "+
			"setup writes", fresh.Examples)
	}
	if fresh.Marked != fresh.Examples {
		t.Errorf("%d of %d example cards carry the badge; every one of them has to, "+
			"or the mark cannot be relied on", fresh.Marked, fresh.Examples)
	}

	// --- phase 2: remove them ------------------------------------------------

	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			// The removal asks first, with the browser's own confirm box, which
			// cannot be answered from outside without a dialog handler. A person who
			// clicks the button and then OK does exactly this.
			chromedp.Evaluate[chromedp.Void](`window.confirm = () => true`),
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#examples-panel [data-action="drop-examples"]`),
			chromedp.Click(`#examples-panel [data-action="drop-examples"]`),
			// The panel used to disappear here, which is why this test exists: with
			// it went the only way back. It stays now, offering the data instead.
			chromedp.WaitVisible(`#examples-panel [data-action="restore-examples"]`),
			chromedp.WaitNotPresent(`#examples-panel [data-action="drop-examples"]`),
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/dashboard'`),
			chromedp.Poll[chromedp.Void](noCardsAtAll, chromedp.WithPollingTimeout(15*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "examples-not-removed")
		t.Fatalf("removing the example data did not leave an empty dashboard with the "+
			"way back offered in Settings: %v", err)
	}

	// --- phase 3: put them back ----------------------------------------------

	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#examples-panel [data-action="restore-examples"]`),
			chromedp.Click(`#examples-panel [data-action="restore-examples"]`),
			// The panel flips back to the loaded state when the request returns.
			chromedp.WaitVisible(`#examples-panel [data-action="drop-examples"]`),
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/dashboard'`),
			chromedp.Poll[chromedp.Void](markedExamplesReady, chromedp.WithPollingTimeout(20*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "examples-not-restored")
		t.Fatalf("the button that loads the example data back did not put them on the "+
			"dashboard: %v", err)
	}

	restored, err := chromedp.Run(ctx, chromedp.Evaluate[exampleCardView](exampleCards))
	if err != nil {
		t.Fatalf("could not read the restored cards: %v", err)
	}
	// More than the lean set the setup writes: the button loads the rich dataset,
	// and that difference is deliberate — it is what the user asked for — so the
	// relationship is what is pinned here, not the number. The number is the thing
	// that grows: it was five when this test was written and is thirteen now, and a
	// constant that has to be edited every time somebody adds a sensor to the
	// dataset teaches people to edit it without reading it. The exact count is
	// pinned where it can be said exactly, against len(demo.Sensors()) in
	// internal/api — this module cannot import that package without dragging the
	// store, and sqlite with it, into a go.sum that exists to stay small.
	if restored.Examples <= fresh.Examples {
		t.Errorf("the dashboard shows %d restored example sensor(s), no more than the "+
			"%d the setup writes; the button is meant to load the larger dataset",
			restored.Examples, fresh.Examples)
	}
	if restored.Marked != restored.Examples {
		t.Errorf("%d of %d restored cards carry the badge, want all of them",
			restored.Marked, restored.Examples)
	}
	if restored.Total != restored.Examples {
		t.Errorf("the dashboard has %d card(s), %d of them examples; nothing else should "+
			"be there", restored.Total, restored.Examples)
	}

	// The history is the point of the rich dataset, and it is invisible on a card,
	// so it is asked for directly. EvalAwaitPromise is what makes the fetch
	// resolve; without it the action returns the pending promise and every count
	// decodes as zero.
	counts, err := chromedp.Run(ctx,
		chromedp.Evaluate[exampleCountView](exampleCounts, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("could not read the example data counts: %v", err)
	}
	if counts.Results == 0 {
		t.Error("the restored example sensors have no probe history; the dataset is " +
			"supposed to arrive with a week of it")
	}
	if counts.Incidents == 0 {
		t.Error("the restored example sensors have no incidents; the outages in the " +
			"dataset are what the incident log and the uptime bars are meant to show")
	}
}

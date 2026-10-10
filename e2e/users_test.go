// Accounts created from the settings screen have to appear on it.
//
// This test exists because the screenshot program found the opposite. Creating a
// user answered 201, the toast said "Account created.", and the table went on
// showing the one row it had been drawn with when the page was opened — because
// the panel was re-rendered from `state.users`, and `loadUsers` returns early once
// it has loaded something. On a fresh page load the new account was there, so the
// server was right and only the screen was wrong, which is the shape of bug no
// API test can see.
//
// The interesting half of the assertion is not "the row appears". It is "the row
// appears without the page being reloaded", because a reload would also make the
// row appear and would also be a lie: the panel is supposed to be one screen, and
// a person who has to press F5 to see the thing they just created has been told
// something false about how the dashboard works. So the check sets a marker on
// `window` before making the change and refuses to pass unless the marker
// survived it — a reload wipes it, and then the row arriving proves nothing.
//
// It lives in the e2e module with the other browser tests. Run it with:
//
//	cd e2e && go test ./... -run UserList -v
package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// createdUser is the account this test adds through the settings screen. It never
// signs in; it exists to be a row. Its password satisfies the form's minlength.
const createdUser = "e2e-viewer"

// noReload is the marker that makes "without a page reload" a fact rather than a
// claim. It is set on the live page before the change and read back after it.
const noReload = `window.__e2eNoReload === 'kept'`

// liveTable wraps a condition on the user table in the marker check, so that a
// condition which only became true after a reload counts as false. Written out as
// a builder rather than inline because Go has no interpolation: a `${name}` in a
// raw string is five literal characters, and the browser answers with a syntax
// error rather than the assertion anyone meant to write.
func liveTable(condition string) string {
	return `(() => {
		if (!(` + noReload + `)) return false;
		const rows = Array.from(document.querySelectorAll('#users-panel .user-row'));
		return ` + condition + `;
	})()`
}

// readUsers returns the rows of the user table as they are on screen right now.
// It reads the DOM rather than any state the page holds, because the defect this
// test guards against was in the DOM while the state was correct.
const readUsers = `Array.from(document.querySelectorAll('#users-panel .user-row')).map((row) => {
  const name = row.querySelector('.user-name');
  const role = row.children[1];
  return {
    id: Number(row.dataset.id),
    name: name ? name.textContent.trim() : '',
    role: role ? role.textContent.trim() : '',
  };
})`

type userRow struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// TestUserListRefreshesWithoutAReload creates an account and then changes its
// role, and checks after each that the table says so while the page it is drawn
// on is demonstrably still the one that was already open.
func TestUserListRefreshesWithoutAReload(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	defer cancelAlloc()

	// Without a deadline, for the same reason as in the other browser tests: a
	// deadline here kills the browser rather than the action, and the phase that
	// timed out would then have nothing left to photograph.
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("could not start the browser: %v", err)
	}

	//nolint:misspell // "screenshotDir" is the spelling GitHub uses for the uploaded files
	screenshotDir := os.Getenv("WEATHERGLASS_E2E_ARTIFACTS")
	if screenshotDir == "" {
		screenshotDir = t.TempDir()
	}

	// --- phase 1: an installation with one account, on the settings view -----

	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(base+"/"),

			// Setup: a username this time as well as a password, and then the
			// question of how much interface to show. The user list is part of
			// the advanced mode, so the choice made here is what decides whether
			// there is a table to assert on at all.
			chromedp.WaitVisible(`#auth-password`),
			chromedp.SendKeys(`#auth-username`, testUser),
			chromedp.SendKeys(`#auth-password`, testPassword),
			chromedp.SendKeys(`#auth-confirm`, testPassword),
			chromedp.Click(`#auth-submit`),
			chromedp.WaitVisible(`#auth-mode-step`),
			chromedp.Click(`input[name="ui-mode"][value="advanced"]`),
			chromedp.Click(`#auth-submit`),
			chromedp.WaitVisible(`#app`),

			// The tour offers itself and covers the whole page while it is up,
			// so it is skipped before anything is clicked through it. Skipping
			// also records it as seen, which keeps it from coming back here.
			chromedp.WaitVisible(`.tour-card`),
			chromedp.Click(`[data-tour="skip"]`),

			// The tabs are underneath the tour layer, which is why the route is
			// changed rather than the tab clicked. The hash is what the router
			// listens to, so this is the same journey by another road.
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("access"),
			chromedp.WaitVisible(`#users-panel`),

			// Exactly one account, and it is the one setup created. If this is
			// wrong the two phases below are asserting about the wrong page.
			chromedp.Poll[chromedp.Void](`document.querySelectorAll('#users-panel .user-row').length === 1`,
				chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "users-not-reachable")
		t.Fatalf("could not reach the settings screen with one account on it: %v\n"+
			"see the screenshot in %s", err, screenshotDir)
	}

	// The marker. From here on, anything that reloads the page wipes it, and the
	// assertions below turn that into a failure rather than into a pass.
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[string](`window.__e2eNoReload = 'kept'`)); err != nil {
		t.Fatalf("could not set the reload marker: %v", err)
	}

	// --- phase 2: a new account is a new row -------------------------------

	newRow := fmt.Sprintf(
		`rows.length === 2 && rows.some((r) => r.querySelector('.user-name').textContent.trim() === %q)`,
		createdUser)

	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`[data-action="user-new"]`),
			chromedp.WaitVisible(`#modal input[name="username"]`),
			chromedp.SendKeys(`#modal input[name="username"]`, createdUser),
			chromedp.SendKeys(`#modal input[name="password"]`, testPassword),
			// The role picker is a fieldset of radios; viewer is the default, but
			// the point of the row below is what it says, so it is chosen rather
			// than left to a default that could change.
			chromedp.Click(`#modal input[name="role"][value="viewer"]`),
			chromedp.Click(`#modal-save`),
			chromedp.WaitNotVisible(`#modal`),

			// The two facts this test is for, in one predicate so that neither
			// can pass on its own: the row is there, and the page was never
			// reloaded to get it there.
			chromedp.Poll[chromedp.Void](liveTable(newRow), chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "user-not-listed")
		t.Fatalf("the account was created but the user table did not show it: %v\n"+
			"The table says %s, and the page %s.\n"+
			"A list that only tells the truth after a reload is the defect this test "+
			"exists for: the panel is re-rendered from `state.users`, so it has to be "+
			"re-read (`loadUsers(true)`) and not merely redrawn.\n"+
			"see the screenshot in %s",
			err, describeUsers(t, ctx), reloadState(t, ctx), screenshotDir)
	}

	// --- phase 3: an edited account is an updated row -----------------------

	// The other half of "the list is live": the panel reads its rows from state
	// that the edit dialog changes too, and a role that only takes effect on a
	// fresh load is the same defect wearing a different hat. It is worth its own
	// assertion rather than a comment, because the two go through different
	// requests — POST and PUT — and could be re-read in one place and not the
	// other.

	id, err := createdUserID(t, ctx)
	if err != nil {
		t.Fatalf("could not read the id of the account just created: %v", err)
	}

	// The role cell, read by position: it is the second column of this row.
	changedRole := fmt.Sprintf(
		`(() => {
			const row = document.querySelector('#users-panel .user-row[data-id="%d"]');
			return !!row && row.children[1].textContent.trim() === 'Operator';
		})()`, id)

	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(fmt.Sprintf(`[data-action="user-edit"][data-id="%d"]`, id)),
			chromedp.WaitVisible(`#modal input[name="role"][value="operator"]`),
			chromedp.Click(`#modal input[name="role"][value="operator"]`),
			chromedp.Click(`#modal-save`),
			chromedp.WaitNotVisible(`#modal`),

			chromedp.Poll[chromedp.Void](liveTable(changedRole), chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "role-not-updated")
		t.Fatalf("the role was changed but the table kept showing the old one: %v\n"+
			"The table says %s, and the page %s.\n"+
			"see the screenshot in %s",
			err, describeUsers(t, ctx), reloadState(t, ctx), screenshotDir)
	}
}

// createdUserID returns the id the settings screen gave the account this test
// created. It is read out of the row rather than remembered, so that the edit
// below addresses whatever the table is actually showing.
func createdUserID(t *testing.T, ctx context.Context) (int, error) {
	t.Helper()
	rows, err := readUserRows(t, ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if r.Name == createdUser {
			return r.ID, nil
		}
	}
	return 0, fmt.Errorf("no row for %q in %s", createdUser, describe(rows))
}

func readUserRows(t *testing.T, ctx context.Context) ([]userRow, error) {
	t.Helper()
	return chromedp.Run(ctx, chromedp.Evaluate[[]userRow](readUsers))
}

// describeUsers is what a failing assertion prints instead of "timeout": the
// table as it stands. The interesting part of a wrong table is what is in it.
func describeUsers(t *testing.T, ctx context.Context) string {
	t.Helper()
	rows, err := readUserRows(t, ctx)
	if err != nil {
		return "(could not be read: " + err.Error() + ")"
	}
	return describe(rows)
}

func describe(rows []userRow) string {
	if len(rows) == 0 {
		return "no rows at all"
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s (%s, id %d)", r.Name, r.Role, r.ID))
	}
	return strings.Join(parts, "; ")
}

// reloadState says whether the page the table was read from is still the page the
// marker was set on.
func reloadState(t *testing.T, ctx context.Context) string {
	t.Helper()
	kept, err := chromedp.Run(ctx, chromedp.Evaluate[bool](noReload))
	if err != nil {
		return "could not be checked (" + err.Error() + ")"
	}
	if !kept {
		return "was reloaded in between, which does not count as the list being live"
	}
	return "was not reloaded"
}

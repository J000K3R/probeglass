package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
	"github.com/j000k3r/probeglass/internal/totp"
)

// ---------------------------------------------------------------------------
// who may do what
// ---------------------------------------------------------------------------

// signIn authenticates and returns the session cookie without touching the one
// the harness is currently carrying, so a test can hold several accounts' sessions
// at once and switch between them.
func (h *harness) signIn(username, password string) *http.Cookie {
	h.t.Helper()
	resp := h.login(username, password)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("sign in as %s: status = %d", username, resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	h.t.Fatalf("signing in as %s set no session cookie", username)
	return nil
}

// addUser creates an account through the API, the way the settings screen does, and
// returns the row it came back with.
func (h *harness) addUser(t *testing.T, username, password string, role model.Role) model.User {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/users",
		map[string]any{"username": username, "password": password, "role": role}, origin(h))
	var u model.User
	decode(t, resp, &u)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s: status = %d", username, resp.StatusCode)
	}
	if u.Role != role {
		t.Fatalf("created %s with role %q, want %q", username, u.Role, role)
	}
	return u
}

// as runs the request with somebody else's session and puts the harness back the way
// it was.
func (h *harness) as(cookie *http.Cookie, fn func() *http.Response) *http.Response {
	h.t.Helper()
	prev := h.cookie
	h.cookie = cookie
	defer func() { h.cookie = prev }()
	return fn()
}

// TestRolesDecideWhatMayBeWritten walks the same handful of requests past all
// three roles. It is deliberately one table rather than three tests: the interesting
// property is the ordering between the roles, and that is only visible when the
// answers sit next to each other.
func TestRolesDecideWhatMayBeWritten(t *testing.T) {
	h := newHarness(t)
	h.setup() // signs in as testUser, an administrator
	admin := h.cookie

	h.addUser(t, "operator", "operator1234", model.RoleOperator)
	h.addUser(t, "viewer", "viewer1234", model.RoleViewer)
	operatorCookie := h.signIn("operator", "operator1234")
	viewerCookie := h.signIn("viewer", "viewer1234")

	// An example sensor to hang the write attempts on.
	sensors := h.listSensors()
	if len(sensors) != 4 {
		t.Fatalf("got %d example sensors, want 4", len(sensors))
	}
	sensor := sensors[0]

	newSensor := map[string]any{
		"name": "web", "type": "http", "target": "https://example.org",
		"interval": 60, "timeout": 10, "retries": 1,
	}

	// A window covering the next hour for one of the example sensors, so that the
	// write attempts below have something real to aim at.
	now := time.Now().UTC()
	newWindow := map[string]any{
		"sensor_id": sensor.ID, "kind": "once",
		"starts_at": now.Format(time.RFC3339), "ends_at": now.Add(time.Hour).Format(time.RFC3339),
		"timezone": "UTC", "enabled": true, "note": "role test",
	}

	cases := []struct {
		what   string
		cookie *http.Cookie
		method string
		path   string
		body   any
		want   int
	}{
		{"a viewer may read the sensors", viewerCookie, http.MethodGet, "/api/sensors", nil, http.StatusOK},
		{"a viewer may read the summary", viewerCookie, http.MethodGet, "/api/summary", nil, http.StatusOK},
		{"a viewer may read the settings", viewerCookie, http.MethodGet, "/api/settings", nil, http.StatusOK},
		{"a viewer may read the maintenance windows", viewerCookie, http.MethodGet, "/api/maintenance", nil, http.StatusOK},
		{"a viewer may not read the accounts", viewerCookie, http.MethodGet, "/api/users", nil, http.StatusForbidden},
		{"a viewer may not create a sensor", viewerCookie, http.MethodPost, "/api/sensors", newSensor, http.StatusForbidden},
		{"a viewer may not delete one either", viewerCookie, http.MethodDelete,
			"/api/sensors/" + itoa(sensor.ID), nil, http.StatusForbidden},
		{"a viewer may not switch one off", viewerCookie, http.MethodPatch,
			"/api/sensors/" + itoa(sensor.ID), map[string]any{"enabled": false}, http.StatusForbidden},
		{"a viewer may not declare a maintenance window", viewerCookie, http.MethodPost,
			"/api/maintenance", newWindow, http.StatusForbidden},

		{"an operator may pause a sensor", operatorCookie, http.MethodPatch,
			"/api/sensors/" + itoa(sensor.ID), map[string]any{"enabled": false}, http.StatusOK},
		{"and start it again", operatorCookie, http.MethodPatch,
			"/api/sensors/" + itoa(sensor.ID), map[string]any{"enabled": true}, http.StatusOK},
		{"an operator may run the diagnostic", operatorCookie, http.MethodPost, "/api/doctor", nil, http.StatusOK},
		{"an operator may not create a sensor", operatorCookie, http.MethodPost, "/api/sensors", newSensor, http.StatusForbidden},
		{"an operator may not rename one", operatorCookie, http.MethodPut,
			"/api/sensors/" + itoa(sensor.ID), map[string]any{"name": "renamed"}, http.StatusForbidden},
		{"an operator may not read the accounts", operatorCookie, http.MethodGet, "/api/users", nil, http.StatusForbidden},
		// A window is configuration, like the sensor list, and not an action taken
		// during an outage. An operator acknowledges an incident; an administrator
		// decides in advance that a box is allowed to be away.
		{"an operator may not declare a maintenance window either", operatorCookie, http.MethodPost,
			"/api/maintenance", newWindow, http.StatusForbidden},

		{"an administrator may create a sensor", admin, http.MethodPost, "/api/sensors", newSensor, http.StatusCreated},
		{"an administrator may read the accounts", admin, http.MethodGet, "/api/users", nil, http.StatusOK},
		{"an administrator may declare a maintenance window", admin, http.MethodPost,
			"/api/maintenance", newWindow, http.StatusCreated},
	}

	for _, c := range cases {
		resp := h.as(c.cookie, func() *http.Response {
			return h.do(c.method, c.path, c.body, origin(h))
		})
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.what, c.method, c.path, resp.StatusCode, c.want)
		}
	}
}

// The one write an operator has on a sensor is a partial update, and the body it may
// carry is exactly one field wide. Anything else has to be refused, or "operators may
// pause a sensor" quietly becomes "operators may edit a sensor".
func TestAnOperatorMayOnlyChangeTheOneFieldTheyOwn(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.addUser(t, "operator", "operator1234", model.RoleOperator)
	op := h.signIn("operator", "operator1234")

	sensor := h.listSensors()[0]

	h.as(op, func() *http.Response {
		resp := h.do(http.MethodPatch, "/api/sensors/"+itoa(sensor.ID),
			map[string]any{"enabled": false, "name": "renamed"}, origin(h))
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("a PATCH carrying a second field: status = %d, want 403", resp.StatusCode)
		}
		return resp
	})

	if got := h.getSensor(t, sensor.ID); got.Name != sensor.Name {
		t.Errorf("the name changed to %q although the request was refused", got.Name)
	}
}

// A refusal must say which role was wanted, not merely "no". The operator staring at
// a greyed-out button needs to know whether to ask for a promotion or to give up.
func TestARefusalNamesTheRoleItWanted(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.addUser(t, "operator", "operator1234", model.RoleOperator)
	op := h.signIn("operator", "operator1234")

	resp := h.as(op, func() *http.Response {
		return h.do(http.MethodPost, "/api/sensors", map[string]any{"name": "web"}, origin(h))
	})
	msg := errorMessage(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(msg, "admin") {
		t.Errorf("the refusal says %q, want it to name the admin role", msg)
	}
}

// The API key is a full administrator credential. Reading the settings screen must
// not be a way to obtain one.
func TestAViewerIsNotToldTheAPISecret(t *testing.T) {
	h := newHarness(t)
	h.setup()

	rotate := h.do(http.MethodPost, "/api/api-key", nil, origin(h))
	var key struct {
		APIKey string `json:"api_key"`
	}
	decode(t, rotate, &key)
	if key.APIKey == "" {
		t.Fatal("rotating the API key returned nothing")
	}

	if got := h.getSettings()["api_key"]; got != key.APIKey {
		t.Errorf("an administrator sees api_key = %q, want the key", got)
	}

	h.addUser(t, "viewer", "viewer1234", model.RoleViewer)
	viewer := h.signIn("viewer", "viewer1234")

	h.as(viewer, func() *http.Response {
		resp := h.do(http.MethodGet, "/api/settings", nil, nil)
		var out struct {
			UI map[string]string `json:"ui"`
			// The key arrives as a sibling of "ui", so it is read separately.
			APIKey *string `json:"api_key"`
		}
		decode(t, resp, &out)
		if out.APIKey != nil {
			t.Errorf("a viewer was handed the API key %q", *out.APIKey)
		}
		return resp
	})

	// And the admin route that mints one is out of reach in the first place.
	resp := h.as(viewer, func() *http.Response {
		return h.do(http.MethodPost, "/api/api-key", nil, origin(h))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer rotating the API key: status = %d, want 403", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// a session is only as good as the account behind it
// ---------------------------------------------------------------------------

// The account row is read on every request precisely so that this works. Without
// that read a role is decided once at sign-in and holds for a month, and "demote this
// person" becomes a promise that comes true in the new year.
func TestARoleChangeAppliesToASessionAlreadyOpen(t *testing.T) {
	h := newHarness(t)
	h.setup()

	u := h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	watcher := h.signIn("watcher", "watcher1234")

	resp := h.as(watcher, func() *http.Response {
		return h.do(http.MethodPost, "/api/doctor", nil, origin(h))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("as a viewer: status = %d, want 403", resp.StatusCode)
	}

	// Promote, without the watcher signing in again.
	h.do(http.MethodPut, "/api/users/"+itoa(u.ID),
		map[string]any{"role": "operator"}, origin(h)).Body.Close()

	resp = h.as(watcher, func() *http.Response {
		return h.do(http.MethodPost, "/api/doctor", nil, origin(h))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("after being promoted, the same session got %d on an operator route, "+
			"want 200: a new role has to take effect on the next click, not on the next "+
			"sign-in", resp.StatusCode)
	}

	// And back down again: the demotion is just as immediate, which is the half that
	// matters when somebody leaves.
	h.do(http.MethodPut, "/api/users/"+itoa(u.ID),
		map[string]any{"role": "viewer"}, origin(h)).Body.Close()

	resp = h.as(watcher, func() *http.Response {
		return h.do(http.MethodPost, "/api/doctor", nil, origin(h))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("after being demoted, the same session got %d, want 403", resp.StatusCode)
	}
}

// Disabling an account has to end its sessions at once. A disabled user who keeps
// working until their cookie expires has not been disabled.
func TestDisablingAnAccountEndsItsSession(t *testing.T) {
	h := newHarness(t)
	h.setup()

	u := h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	watcher := h.signIn("watcher", "watcher1234")

	resp := h.as(watcher, func() *http.Response { return h.do(http.MethodGet, "/api/sensors", nil, nil) })
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("before being disabled: status = %d, want 200", resp.StatusCode)
	}

	h.do(http.MethodPut, "/api/users/"+itoa(u.ID),
		map[string]any{"disabled": true}, origin(h)).Body.Close()

	resp = h.as(watcher, func() *http.Response { return h.do(http.MethodGet, "/api/sensors", nil, nil) })
	resp.Body.Close()
	// 401, not 403: the account is not forbidden, it is gone as far as this cookie is
	// concerned, and the dashboard has to send the browser back to the login.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after being disabled: status = %d, want 401", resp.StatusCode)
	}

	// Signing in again is refused too, and says why. A 403 rather than the 401 the
	// wrong-password path returns, because the password was accepted first: whoever
	// is reading this sentence already knew the credentials, so "this account is
	// disabled" tells them nothing they did not have and saves them a support ticket.
	again := h.login("watcher", "watcher1234")
	msg := errorMessage(t, again)
	if again.StatusCode != http.StatusForbidden {
		t.Errorf("a disabled account could still sign in: status = %d, want 403", again.StatusCode)
	}
	if !strings.Contains(msg, "disabled") {
		t.Errorf("the refusal says %q, want it to say the account is disabled", msg)
	}
}

// Deleting an account is the same thing, one step further.
func TestDeletingAnAccountEndsItsSession(t *testing.T) {
	h := newHarness(t)
	h.setup()

	u := h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	watcher := h.signIn("watcher", "watcher1234")

	h.do(http.MethodDelete, "/api/users/"+itoa(u.ID), nil, origin(h)).Body.Close()

	resp := h.as(watcher, func() *http.Response { return h.do(http.MethodGet, "/api/sensors", nil, nil) })
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after being deleted: status = %d, want 401", resp.StatusCode)
	}
}

// A password change ends that account's other sessions and nobody else's. The old
// behaviour rotated one global secret and threw out every account at once, which was
// both too broad and pointed at the wrong thing.
func TestChangingAPasswordEndsOnlyThatAccountsOtherSessions(t *testing.T) {
	h := newHarness(t)
	h.setup()
	adminOther := h.signIn(testUser, testPassword)

	u := h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	watcher := h.signIn("watcher", "watcher1234")

	h.as(watcher, func() *http.Response {
		resp := h.do(http.MethodPut, "/api/password",
			map[string]string{"current_password": "watcher1234", "new_password": "watcher5678"},
			origin(h))
		resp.Body.Close()
		return resp
	})

	// Everybody else carries on.
	resp := h.do(http.MethodGet, "/api/users", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the administrator was signed out by another account's password change: "+
			"status = %d, want 200", resp.StatusCode)
	}
	resp = h.as(adminOther, func() *http.Response { return h.do(http.MethodGet, "/api/users", nil, nil) })
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a second session of the same administrator was ended as well: status = %d, "+
			"want 200", resp.StatusCode)
	}

	// And the account whose password changed is signed out of the session it was
	// using, because that cookie was issued under the old epoch.
	resp = h.as(watcher, func() *http.Response { return h.do(http.MethodGet, "/api/sensors", nil, nil) })
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the session that changed the password is still valid: status = %d, want 401",
			resp.StatusCode)
	}

	// The new password is the one that works.
	if got := h.login("watcher", "watcher5678"); got.StatusCode != http.StatusOK {
		t.Errorf("the new password does not work: status = %d", got.StatusCode)
	} else {
		got.Body.Close()
	}
	_ = u // the account is created for the sign-in above; the row itself is not needed
}

// ---------------------------------------------------------------------------
// the guards that keep an installation reachable
// ---------------------------------------------------------------------------

func TestNobodyCanLockThemselvesOut(t *testing.T) {
	h := newHarness(t)
	h.setup()

	me, err := h.store.GetUserByName(context.Background(), testUser)
	if err != nil {
		t.Fatalf("read my own account: %v", err)
	}

	cases := []struct {
		what   string
		method string
		path   string
		body   any
	}{
		{"deleting my own account", http.MethodDelete, "/api/users/" + itoa(me.ID), nil},
		{"disabling my own account", http.MethodPut, "/api/users/" + itoa(me.ID),
			map[string]any{"disabled": true}},
		{"demoting myself while I am the only administrator", http.MethodPut,
			"/api/users/" + itoa(me.ID), map[string]any{"role": "viewer"}},
	}
	for _, c := range cases {
		resp := h.do(c.method, c.path, c.body, origin(h))
		body := errorMessage(t, resp)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409 (%s)", c.what, resp.StatusCode, body)
		}
	}

	// Refused means refused: I am still signed in and still an administrator.
	after, err := h.store.GetUserByName(context.Background(), testUser)
	if err != nil || after.Role != model.RoleAdmin || after.Disabled {
		t.Fatalf("my own account changed anyway: %+v (%v)", after, err)
	}
	if after.TokenEpoch != me.TokenEpoch {
		t.Errorf("my token epoch moved from %d to %d, so a refused request signed me "+
			"out of everything", me.TokenEpoch, after.TokenEpoch)
	}
}

// The last administrator is not a special case of "me": a second administrator
// removing the only remaining one is the same outage from a different chair.
func TestTheLastAdministratorSurvivesTheOthersAttempts(t *testing.T) {
	h := newHarness(t)
	h.setup() // testUser, administrator

	first, err := h.store.GetUserByName(context.Background(), testUser)
	if err != nil {
		t.Fatalf("read the first administrator: %v", err)
	}
	h.addUser(t, "second", "second1234", model.RoleAdmin)
	second := h.signIn("second", "second1234")

	// Two administrators exist, so demoting one is allowed.
	resp := h.as(second, func() *http.Response {
		return h.do(http.MethodPut, "/api/users/"+itoa(first.ID),
			map[string]any{"role": "viewer"}, origin(h))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("demoting one of two administrators: status = %d, want 200", resp.StatusCode)
	}

	// Now "second" is the last one. Promoting the demoted account back is the way out
	// and must stay open; everything that removes the last administrator must not.
	me := mustUser(t, h, "second")
	for _, c := range []struct {
		what string
		body map[string]any
	}{
		{"demoting", map[string]any{"role": "viewer"}},
		{"disabling", map[string]any{"disabled": true}},
	} {
		resp := h.as(second, func() *http.Response {
			return h.do(http.MethodPut, "/api/users/"+itoa(me.ID), c.body, origin(h))
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s myself as the last administrator: status = %d, want 409",
				c.what, resp.StatusCode)
		}
	}
	resp = h.as(second, func() *http.Response {
		return h.do(http.MethodDelete, "/api/users/"+itoa(first.ID), nil, origin(h))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("removing a viewer: status = %d, want 200", resp.StatusCode)
	}

	resp = h.as(second, func() *http.Response {
		return h.do(http.MethodPut, "/api/users/"+itoa(first.ID),
			map[string]any{"role": "admin"}, origin(h))
	})
	resp.Body.Close()
	// The account was deleted a moment ago, so this is a 404 — which is itself the
	// point: the way out of a single-administrator installation is to create another
	// one, and creating accounts keeps working.
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("changing a deleted account: status = %d, want 404", resp.StatusCode)
	}
	resp = h.as(second, func() *http.Response {
		return h.do(http.MethodPost, "/api/users",
			map[string]any{"username": "third", "password": "third1234", "role": "admin"}, origin(h))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("creating a second administrator from a single-administrator "+
			"installation: status = %d, want 201", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// the second factor
// ---------------------------------------------------------------------------

// The interim credential handed out between the password and the code is the single
// most dangerous value this package creates. If it were accepted as a session cookie,
// anybody who read it off the wire would be signed in without ever producing a code —
// which is the whole thing the second factor exists to prevent.
func TestTheChallengeIsNotAcceptedAsASessionCookie(t *testing.T) {
	h := newHarness(t)
	h.setup()

	u := mustUser(t, h, testUser)
	challenge := h.server.signChallenge(u.ID)

	// It is signed by the server's own secret, and it looks like one of ours.
	cookie := &http.Cookie{Name: sessionCookie, Value: challenge}
	resp := h.as(cookie, func() *http.Response { return h.do(http.MethodGet, "/api/sensors", nil, nil) })
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a challenge was accepted as a session: status = %d, want 401.\n"+
			"The two are signed with the same secret and separated only by their prefix "+
			"and their key; if either check goes, the second factor stops being one.",
			resp.StatusCode)
	}

	// And the other way round: a real session is not a challenge, so a signed-in
	// browser cannot trade its cookie for a second-factor bypass.
	session := h.cookie.Value
	resp = h.do(http.MethodPost, "/api/login/totp",
		map[string]string{"challenge": session, "code": "123456"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a session cookie was accepted as a challenge: status = %d, want 401",
			resp.StatusCode)
	}
}

// A code read over somebody's shoulder is good for thirty seconds. Without the
// recorded step it is good for that whole window, which is long enough to use.
func TestASecondFactorCodeCannotBeUsedTwice(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	account := h.enrolSecondFactor(t, "watcher")

	// Run the pair inside one 30-second window, so that a step boundary cannot fall
	// between them and turn a replay refusal into a merely stale code. Both are a 401,
	// but this test is about the replay rule.
	waitForRoomInTheWindow(t)

	code, err := totp.Code(account.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("compute a code: %v", err)
	}

	first := h.totpLogin(t, "watcher", "watcher1234", code)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first use of the code: status = %d, want 200", first.StatusCode)
	}

	second := h.totpLogin(t, "watcher", "watcher1234", code)
	msg := errorMessage(t, second)
	if second.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second use of the same code: status = %d, want 401", second.StatusCode)
	}
	if !strings.Contains(msg, "already been used") {
		t.Errorf("the second attempt said %q, want it to say the code has been used.\n"+
			"A stale code means the clock is wrong; a replayed one means somebody is "+
			"watching, and the two want different answers.", msg)
	}
}

// A recovery code is a way past a lost phone, so it is a password in all but name: it
// works once, and one that has been used stays used.
func TestARecoveryCodeWorksExactlyOnce(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	h.enrolSecondFactor(t, "watcher")

	ctx := context.Background()
	u := mustUser(t, h, "watcher")
	codes, err := store.NewRecoveryCodes(4)
	if err != nil {
		t.Fatalf("make recovery codes: %v", err)
	}
	if err := h.store.ReplaceRecoveryCodes(ctx, u.ID, codes); err != nil {
		t.Fatalf("store recovery codes: %v", err)
	}

	first := h.totpLogin(t, "watcher", "watcher1234", codes[0])
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("signing in with a recovery code: status = %d, want 200", first.StatusCode)
	}

	second := h.totpLogin(t, "watcher", "watcher1234", codes[0])
	second.Body.Close()
	if second.StatusCode != http.StatusUnauthorized {
		t.Errorf("the same recovery code worked twice: status = %d, want 401", second.StatusCode)
	}

	// A different one still works — the refusal is about that code, not about
	// recovery codes in general.
	third := h.totpLogin(t, "watcher", "watcher1234", codes[1])
	third.Body.Close()
	if third.StatusCode != http.StatusOK {
		t.Errorf("a second recovery code was refused: status = %d, want 200", third.StatusCode)
	}

	// And the count the settings screen shows went down by two.
	left, err := h.store.CountRecoveryCodes(ctx, u.ID)
	if err != nil {
		t.Fatalf("count the remaining codes: %v", err)
	}
	if left != 2 {
		t.Errorf("recovery codes left = %d, want 2", left)
	}
}

// Signing in with a second factor takes two steps, and the second step must not be
// skippable: presenting the right password alone has to leave the browser
// unauthenticated.
func TestAPasswordAloneDoesNotSignInWhenASecondFactorIsSet(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	h.enrolSecondFactor(t, "watcher")

	resp := h.login("watcher", "watcher1234")
	var out struct {
		TOTPRequired bool   `json:"totp_required"`
		Challenge    string `json:"challenge"`
	}
	decode(t, resp, &out)
	if resp.StatusCode != http.StatusOK || !out.TOTPRequired || out.Challenge == "" {
		t.Fatalf("the password step: status = %d, body = %+v; want a challenge", resp.StatusCode, out)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("the password step set %d cookie(s) before the code was given: "+
			"a second factor that hands out a session anyway is not a second factor",
			len(resp.Cookies()))
	}

	// A wrong code is refused, and the challenge survives the attempt.
	bad := h.do(http.MethodPost, "/api/login/totp",
		map[string]string{"challenge": out.Challenge, "code": "000000"}, nil)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong code: status = %d, want 401", bad.StatusCode)
	}

	waitForRoomInTheWindow(t)
	code, err := totp.Code(mustUser(t, h, "watcher").TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("compute a code: %v", err)
	}
	good := h.do(http.MethodPost, "/api/login/totp",
		map[string]string{"challenge": out.Challenge, "code": code}, nil)
	good.Body.Close()
	if good.StatusCode != http.StatusOK {
		t.Fatalf("the right code after a wrong one: status = %d, want 200.\n"+
			"A mistyped digit must not cost the whole sign-in.", good.StatusCode)
	}
}

// A challenge is good for two minutes and then stops being good. It is not a session
// with a long expiry.
func TestAnExpiredChallengeIsRefused(t *testing.T) {
	h := newHarness(t)
	h.setup()

	expired := mintChallenge(t, h, mustUser(t, h, testUser).ID, time.Now().Add(-time.Second))
	resp := h.do(http.MethodPost, "/api/login/totp",
		map[string]string{"challenge": expired, "code": "123456"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an expired challenge: status = %d, want 401", resp.StatusCode)
	}
}

// The second factor is the account's own business, and these routes act only on the
// signed-in account. There is deliberately no way to switch on a code for somebody
// else, so there is no id in the path to get wrong.
func TestSecondFactorRoutesAreConfinedToTheSignedInAccount(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// Nothing but an API key is presented, so a wrong one is simply not authenticated.
	bare := h.as(nil, func() *http.Response {
		return h.do(http.MethodPost, "/api/me/totp/start", nil,
			map[string]string{"X-API-Key": "not-a-real-key"})
	})
	bare.Body.Close()
	if bare.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unknown key: status = %d, want 401", bare.StatusCode)
	}

	// A valid key, on the other hand, authenticates — and then has to be told that
	// this particular panel is not for it.
	key := h.rotateKey(t)
	resp := h.do(http.MethodPost, "/api/me/totp/start", nil, map[string]string{"X-API-Key": key})
	msg := errorMessage(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("starting an enrolment with the API key: status = %d, want 400 (%s)",
			resp.StatusCode, msg)
	}
	if !strings.Contains(msg, "account") {
		t.Errorf("the refusal says %q, want it to explain that the key has no account", msg)
	}

	// A viewer can enrol one for themselves: their own credentials are theirs.
	h.addUser(t, "watcher", "watcher1234", model.RoleViewer)
	watcher := h.signIn("watcher", "watcher1234")
	resp = h.as(watcher, func() *http.Response {
		return h.do(http.MethodPost, "/api/me/totp/start", nil, origin(h))
	})
	var start struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	decode(t, resp, &start)
	if resp.StatusCode != http.StatusOK || start.Secret == "" {
		t.Fatalf("a viewer enrolling their own second factor: status = %d, body = %+v",
			resp.StatusCode, start)
	}
	if !strings.HasPrefix(start.URI, "otpauth://totp/") {
		t.Errorf("uri = %q, want an otpauth:// link an authenticator app understands", start.URI)
	}
}

// ---------------------------------------------------------------------------
// small helpers for the second-factor tests
// ---------------------------------------------------------------------------

// enrolSecondFactor switches the second factor on for an account directly, the way
// the settings screen leaves it after a successful enrolment, and returns the row.
func (h *harness) enrolSecondFactor(t *testing.T, username string) model.User {
	t.Helper()
	u := mustUser(t, h, username)
	secret, err := totp.GenerateSecret()
	if err != nil {
		t.Fatalf("generate a secret: %v", err)
	}
	if err := h.store.SetUserTOTP(context.Background(), u.ID, secret, true, 0); err != nil {
		t.Fatalf("store the secret: %v", err)
	}
	return mustUser(t, h, username)
}

func mustUser(t *testing.T, h *harness, username string) model.User {
	t.Helper()
	u, err := h.store.GetUserByName(context.Background(), username)
	if err != nil {
		t.Fatalf("read %s: %v", username, err)
	}
	return u
}

// totpLogin walks the whole two-step sign-in and returns the response to the second
// step.
func (h *harness) totpLogin(t *testing.T, username, password, code string) *http.Response {
	t.Helper()
	first := h.login(username, password)
	var step struct {
		Challenge string `json:"challenge"`
	}
	decode(t, first, &step)
	if first.StatusCode != http.StatusOK || step.Challenge == "" {
		t.Fatalf("the password step for %s: status = %d", username, first.StatusCode)
	}
	return h.do(http.MethodPost, "/api/login/totp",
		map[string]string{"challenge": step.Challenge, "code": code}, nil)
}

// rotateKey mints a fresh API key through the API and returns it.
func (h *harness) rotateKey(t *testing.T) string {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/api-key", nil, origin(h))
	var out struct {
		APIKey string `json:"api_key"`
	}
	decode(t, resp, &out)
	if out.APIKey == "" {
		t.Fatalf("rotating the API key: status = %d", resp.StatusCode)
	}
	return out.APIKey
}

// mintChallenge builds a challenge that expired at the given moment. signChallenge
// always issues one two minutes into the future, and a test that waits two minutes is
// not a test anybody runs.
func mintChallenge(t *testing.T, h *harness, userID int64, expiry time.Time) string {
	t.Helper()
	_, secret := h.server.secrets()
	payload := fmt.Sprintf("%s%d.%s", challengePrefix, userID,
		strconv.FormatInt(expiry.Unix(), 10))
	return payload + "." + sign(signingKey(secret, "challenge"), payload)
}

// getSettings reads the settings screen as the current session.
func (h *harness) getSettings() map[string]any {
	h.t.Helper()
	resp := h.do(http.MethodGet, "/api/settings", nil, nil)
	var out map[string]any
	decode(h.t, resp, &out)
	return out
}

// getSensor reads one sensor as the current session.
func (h *harness) getSensor(t *testing.T, id int64) model.Sensor {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/sensors/"+itoa(id), nil, nil)
	var s model.Sensor
	decode(t, resp, &s)
	return s
}

// waitForRoomInTheWindow blocks until the current 30-second TOTP window has at least
// five seconds left, so that a pair of requests made back to back lands inside one
// step.
func waitForRoomInTheWindow(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if totp.Step(time.Now()) == totp.Step(time.Now().Add(5*time.Second)) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no 30-second window with room in it appeared within a minute")
}

// decodeJSON tells an unknown field apart from any other decoding failure by matching
// the wording encoding/json uses, because the standard library reports it as an
// untyped error. That makes the wording part of the contract, and a Go release that
// rephrases it would silently turn the operator's 403 back into a 400 — a change
// nobody would notice, in the answer that explains a refusal.
func TestTheUnknownFieldWordingIsWhatWeMatchOn(t *testing.T) {
	var dst struct {
		Enabled *bool `json:"enabled"`
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/sensors/1",
		strings.NewReader(`{"enabled":false,"name":"renamed"}`))
	err := decodeJSON(req, &dst)
	if err == nil {
		t.Fatal("a body with an extra field was accepted")
	}
	if !errors.Is(err, errUnknownField) {
		t.Errorf("decodeJSON returned %q, which is not recognised as an unknown field.\n"+
			"encoding/json has reworded its message; %q is the prefix to update.",
			err, unknownFieldPrefix)
	}
	if !strings.HasPrefix(err.Error(), unknownFieldPrefix) {
		t.Errorf("the message %q no longer starts with %q", err, unknownFieldPrefix)
	}
	// The message the client sees is the one from the standard library, so that
	// whoever reads it learns which field was the problem.
	if !strings.Contains(err.Error(), `"name"`) {
		t.Errorf("the message %q does not name the offending field", err)
	}
}

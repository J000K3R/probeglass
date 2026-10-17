package store

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
)

// testPNG is a real image of the given size, encoded the way a browser would send
// one. The bytes have to decode: an upload is read before it is stored, so a
// hand-written header that nothing can read would be refused by the very check the
// tests around it are about.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode a %dx%d PNG: %v", w, h, err)
	}
	return buf.Bytes()
}

func mustIcon(t *testing.T, st *Store, name string) model.Icon {
	t.Helper()
	ic := model.Icon{Name: name}
	if err := st.CreateIcon(context.Background(), &ic, testPNG(t, 24, 24)); err != nil {
		t.Fatalf("create icon %q: %v", name, err)
	}
	return ic
}

// TestIconRefForms pins the three shapes a reference can have, which is the one
// thing every other part of this feature agrees on: the API, the configuration file,
// the backup and the dashboard all write the same string.
func TestIconRefForms(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		kind icon.Kind
		want bool // whether Check accepts it
	}{
		{"", icon.KindNone, true},
		{"  ", icon.KindNone, true},
		{"builtin:router", icon.KindBuiltin, true},
		{"upload:7", icon.KindUpload, true},
		// A bare name is not a reference, however much it looks like one: it would
		// be ambiguous the moment somebody uploads an image and calls it "router".
		{"router", icon.KindUnknown, false},
		{"builtin:nosuchthing", icon.KindUnknown, false},
		{"upload:seven", icon.KindUnknown, false},
		{"upload:", icon.KindUnknown, false},
		{"javascript:alert(1)", icon.KindUnknown, false},
	} {
		if kind, _ := icon.Parse(tc.ref); kind != tc.kind {
			t.Errorf("Parse(%q) = %v, want %v", tc.ref, kind, tc.kind)
		}
		err := icon.Check(tc.ref)
		if (err == nil) != tc.want {
			t.Errorf("Check(%q) = %v, want accepted=%v", tc.ref, err, tc.want)
		}
	}
}

// TestUploadedIconRoundTrip goes the whole way: an image in, the bytes back out,
// and the count of what wears it.
func TestUploadedIconRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	data := testPNG(t, 32, 16)
	ic := model.Icon{Name: "fritzbox"}
	if err := st.CreateIcon(ctx, &ic, data); err != nil {
		t.Fatalf("create icon: %v", err)
	}
	if ic.ID == 0 || ic.MediaType != "image/png" || ic.Width != 32 || ic.Height != 16 {
		t.Errorf("stored icon = %+v, want a 32x16 PNG with an id", ic)
	}

	stored, got, err := st.GetIcon(ctx, ic.ID)
	if err != nil {
		t.Fatalf("get icon: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("the bytes that came back are not the bytes that went in")
	}
	if stored.Size != len(data) {
		t.Errorf("stored size = %d, want %d", stored.Size, len(data))
	}

	// A sensor wearing it, and the count that the picker shows beside it.
	mustSensor(t, st, model.Sensor{
		Name: "fritzbox", Type: model.SensorPing, Target: "10.0.0.1",
		Icon: icon.UploadRef(ic.ID),
	})
	list, err := st.ListIcons(ctx)
	if err != nil {
		t.Fatalf("list icons: %v", err)
	}
	if len(list) != 1 || list[0].Uses != 1 {
		t.Errorf("listing = %+v, want one icon used once", list)
	}

	// Deleting it while something wears it is refused, and the refusal counts what.
	var inUse *IconInUseError
	err = st.DeleteIcon(ctx, ic.ID, false)
	if !errors.As(err, &inUse) {
		t.Fatalf("deleting an icon in use = %v, want a refusal", err)
	}
	if inUse.Sensors != 1 || inUse.Sites != 0 || inUse.Groups != 0 {
		t.Errorf("refusal counts %+v, want one sensor", inUse)
	}

	// And with clear, the rows are emptied in the same breath.
	if err := st.DeleteIcon(ctx, ic.ID, true); err != nil {
		t.Fatalf("delete with clear: %v", err)
	}
	c, err := st.GetSensorByName(ctx, "fritzbox")
	if err != nil {
		t.Fatalf("get sensor: %v", err)
	}
	if c.Icon != "" {
		t.Errorf("the sensor still wears %q after the icon was cleared", c.Icon)
	}
	if _, _, err := st.GetIcon(ctx, ic.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the icon is still there: %v", err)
	}
}

// TestIconRefusesWhatIsNotAnImage is the allowlist, and it matters most for the one
// format that is deliberately missing from it.
//
// An SVG is not an image but a document: it may carry script, and it would be served
// from this dashboard's own origin, where that script would run with the session
// cookie of whoever looked at it. Nothing else an upload can be carries that risk,
// which is why "no SVG" is a rule and not a preference.
func TestIconRefusesWhatIsNotAnImage(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"an SVG", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "has to be"},
		{"a text file", []byte("this is not an image at all"), "has to be"},
		{"nothing", []byte{}, "empty"},
		{"a truncated PNG", testPNG(t, 8, 8)[:40], "could not be read"},
		{"too many bytes", append(testPNG(t, 8, 8), make([]byte, icon.MaxBytes)...), "may be at most"},
		{"too many pixels", testPNG(t, icon.MaxSide+1, 1), "at most 512×512"},
	} {
		ic := model.Icon{Name: tc.name}
		err := st.CreateIcon(ctx, &ic, tc.data)
		if err == nil {
			t.Errorf("%s was accepted as an icon", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refusal = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
	list, err := st.ListIcons(ctx)
	if err != nil {
		t.Fatalf("list icons: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("a refused upload was stored anyway: %+v", list)
	}
}

// TestValidateIconRefRefusesAnUploadThatIsNotHere is the difference between the
// shape of a reference and whether it points at anything.
//
// It is the mistake a backup made on another machine produces: the file says
// `upload:3`, this installation has no row 3, and the sensor would draw nothing
// without a word anywhere to say why.
func TestValidateIconRefRefusesAnUploadThatIsNotHere(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.ValidateIconRef(ctx, ""); err != nil {
		t.Errorf("no icon was refused: %v", err)
	}
	if err := st.ValidateIconRef(ctx, icon.BuiltinRef("router")); err != nil {
		t.Errorf("a built-in was refused: %v", err)
	}
	err := st.ValidateIconRef(ctx, icon.UploadRef(3))
	if err == nil {
		t.Fatal("an upload id that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "upload:3") {
		t.Errorf("the refusal does not name the reference: %v", err)
	}

	ic := mustIcon(t, st, "router")
	if err := st.ValidateIconRef(ctx, icon.UploadRef(ic.ID)); err != nil {
		t.Errorf("an upload that is here was refused: %v", err)
	}
}

// TestGroupsAreSettingsNotRows is the design decision this whole table exists to
// express: a group is still a word on a sensor.
//
// A sensor may name a group this table has never heard of, and it is listed all the
// same — that is the ordinary state of a fresh installation, and it has to stay
// cheap. What the table adds is only what a word cannot say.
func TestGroupsAreSettingsNotRows(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mustSensor(t, st, model.Sensor{Name: "router", Type: model.SensorPing, Target: "10.0.0.1", Group: "Netz"})
	mustSensor(t, st, model.Sensor{Name: "nas", Type: model.SensorTCP, Target: "10.0.0.2:443", Group: "Netz"})
	mustSensor(t, st, model.Sensor{Name: "printer", Type: model.SensorTCP, Target: "10.0.0.9:9100", Group: "Drucker"})

	groups, err := st.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("listed %d groups, want the two the sensors name: %+v", len(groups), groups)
	}
	byName := map[string]model.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	if g := byName["Netz"]; g.SensorCount != 2 || g.Icon != "" || g.Colour != "" {
		t.Errorf("Netz = %+v, want two sensors and no settings", g)
	}

	// Now somebody says something about one of them.
	netz := byName["Netz"]
	netz.Icon = icon.BuiltinRef("router")
	netz.Colour = "amber"
	if err := st.SetGroup(ctx, &netz); err != nil {
		t.Fatalf("set group: %v", err)
	}
	if netz.CreatedAt.IsZero() || netz.SensorCount != 2 {
		t.Errorf("saved group = %+v, want the stored timestamps and the count", netz)
	}

	groups, err = st.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("the settings row invented a second group: %+v", groups)
	}
	for _, g := range groups {
		if g.Name == "Netz" && (g.Icon != icon.BuiltinRef("router") || g.Colour != "amber") {
			t.Errorf("Netz came back as %+v, want the settings that were saved", g)
		}
	}

	// Forgetting it leaves every sensor where it was: the group itself was never a
	// row, so there is nothing about the monitoring to remove.
	count, err := st.ForgetGroup(ctx, "Netz")
	if err != nil {
		t.Fatalf("forget group: %v", err)
	}
	if count != 2 {
		t.Errorf("forgetting Netz reported %d sensors, want 2", count)
	}
	groups, err = st.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("forgetting a group removed the group: %+v", groups)
	}
	for _, g := range groups {
		if g.Name == "Netz" && (g.Icon != "" || g.Colour != "") {
			t.Errorf("Netz still has settings after being forgotten: %+v", g)
		}
	}
}

// TestGroupRefusesWhatTheFormCannotSend covers the refusals a group write can
// produce: a name that cannot be a URL segment, a colour that is not in the palette
// and an icon that points at nothing.
func TestGroupRefusesWhatTheFormCannotSend(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		g    model.Group
		want string
	}{
		{"no name", model.Group{}, "needs a name"},
		{"a slash", model.Group{Name: "Netz/WAN"}, "may not contain"},
		{"a long name", model.Group{Name: strings.Repeat("x", 65)}, "at most 64"},
		{"a colour that is not one", model.Group{Name: "Netz", Colour: "#f59e0b"}, "not a colour"},
		{"an icon that does not exist", model.Group{Name: "Netz", Icon: icon.BuiltinRef("nope")}, "no built-in icon"},
		{"an upload that does not exist", model.Group{Name: "Netz", Icon: icon.UploadRef(9)}, "no uploaded icon"},
	} {
		err := st.SetGroup(ctx, &tc.g)
		if err == nil {
			t.Errorf("%s was accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refusal = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// TestSensorsAndSitesCarryTheirIcon is the column doing the ordinary thing, through
// the two write paths that exist: an edit, and the configuration file's apply.
func TestSensorsAndSitesCarryTheirIcon(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	c := mustSensor(t, st, model.Sensor{
		Name: "router", Type: model.SensorPing, Target: "10.0.0.1",
		Icon: icon.BuiltinRef("router"),
	})
	if c.Icon != icon.BuiltinRef("router") {
		t.Fatalf("a sensor stored with an icon came back as %q", c.Icon)
	}
	// An edit that says nothing about the icon keeps it, and one that says ""
	// clears it — the same rule the site and the parent follow.
	c.Target = "10.0.0.2"
	if err := st.UpdateSensor(ctx, &c); err != nil {
		t.Fatalf("update sensor: %v", err)
	}
	again, err := st.GetSensor(ctx, c.ID)
	if err != nil {
		t.Fatalf("get sensor: %v", err)
	}
	if again.Icon != icon.BuiltinRef("router") {
		t.Errorf("an edit lost the icon: %q", again.Icon)
	}

	site := mustSite(t, st, model.Site{
		Name: "keller", Kind: model.SiteLocal, Icon: icon.BuiltinRef("server"),
	})
	if site.Icon != icon.BuiltinRef("server") {
		t.Fatalf("a site stored with an icon came back as %q", site.Icon)
	}
	renamed := model.Site{Name: "HWR", Kind: model.SiteLocal, Icon: site.Icon}
	if err := st.UpdateSite(ctx, "keller", &renamed); err != nil {
		t.Fatalf("update site: %v", err)
	}
	if renamed.Icon != icon.BuiltinRef("server") {
		t.Errorf("renaming a site lost the icon: %q", renamed.Icon)
	}

	// And the apply path, which writes the whole row from the file on every start.
	managed := model.Sensor{
		Name: "managed", Type: model.SensorPing, Target: "10.0.0.3",
		Icon: icon.BuiltinRef("wifi"),
	}
	if err := st.UpsertManagedSensor(ctx, &managed); err != nil {
		t.Fatalf("apply a managed sensor: %v", err)
	}
	stored, err := st.GetSensorByName(ctx, "managed")
	if err != nil {
		t.Fatalf("get managed: %v", err)
	}
	if stored.Icon != icon.BuiltinRef("wifi") {
		t.Errorf("the apply path stored icon %q, want wifi", stored.Icon)
	}
}

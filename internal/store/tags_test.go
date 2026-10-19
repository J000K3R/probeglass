package store

import (
	"context"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
)

func TestTagsAreSettingsNotRows(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mustSensor(t, st, model.Sensor{
		Name: "router", Type: model.SensorPing, Target: "10.0.0.1",
		Tags: []string{"lan", "core"},
	})
	mustSensor(t, st, model.Sensor{
		Name: "nas", Type: model.SensorTCP, Target: "10.0.0.2:443",
		Tags: []string{"lan", "storage"},
	})

	tags, err := st.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	byName := map[string]model.Tag{}
	for _, tag := range tags {
		byName[tag.Name] = tag
	}
	if got := byName["lan"]; got.SensorCount != 2 {
		t.Errorf("lan = %+v, want two sensors", got)
	}
	if got := byName["core"]; got.SensorCount != 1 {
		t.Errorf("core = %+v, want one sensor", got)
	}

	// Now somebody says something about one of them.
	lan := byName["lan"]
	lan.Icon = icon.BuiltinRef("wifi")
	lan.Colour = "amber"
	if err := st.SetTag(ctx, &lan); err != nil {
		t.Fatalf("set tag: %v", err)
	}
	if lan.CreatedAt.IsZero() || lan.SensorCount != 2 {
		t.Errorf("saved tag = %+v, want the stored timestamps and the count", lan)
	}

	tags, err = st.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("the settings row invented a second tag: %+v", tags)
	}
	for _, tag := range tags {
		if tag.Name == "lan" && (tag.Icon != icon.BuiltinRef("wifi") || tag.Colour != "amber") {
			t.Errorf("lan came back as %+v, want the settings that were saved", tag)
		}
	}

	// A tag nobody carries can have settings too; the editor creates it that way.
	printer := model.Tag{Name: "printer", Colour: "rose"}
	if err := st.SetTag(ctx, &printer); err != nil {
		t.Fatalf("set settings for a tag no sensor carries: %v", err)
	}
	tags, err = st.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 4 {
		t.Fatalf("a settings-only tag is not listed: %+v", tags)
	}

	// Forgetting it leaves every sensor where it was: the tag itself was never a row.
	count, err := st.ForgetTag(ctx, "lan")
	if err != nil {
		t.Fatalf("forget tag: %v", err)
	}
	if count != 2 {
		t.Errorf("forgetting lan reported %d sensors, want 2", count)
	}
	tags, err = st.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	for _, tag := range tags {
		if tag.Name == "lan" && (tag.Icon != "" || tag.Colour != "") {
			t.Errorf("lan still has settings after being forgotten: %+v", tag)
		}
	}
}

func TestTagRefusesWhatTheFormCannotSend(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		tag  model.Tag
		want string
	}{
		{"no name", model.Tag{}, "needs a name"},
		{"a comma", model.Tag{Name: "lan,core"}, "may not contain a comma"},
		{"a slash", model.Tag{Name: "lan/wan"}, "may not contain"},
		{"a long name", model.Tag{Name: strings.Repeat("x", 65)}, "at most 64"},
		{"a colour that is not one", model.Tag{Name: "lan", Colour: "#f59e0b"}, "not a colour"},
		{"an icon that does not exist", model.Tag{Name: "lan", Icon: icon.BuiltinRef("nope")}, "no built-in icon"},
		{"an upload that does not exist", model.Tag{Name: "lan", Icon: icon.UploadRef(9)}, "no uploaded icon"},
	} {
		err := st.SetTag(ctx, &tc.tag)
		if err == nil {
			t.Errorf("%s was accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refusal = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

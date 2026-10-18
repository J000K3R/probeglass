package api

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
)

// Icons and groups over HTTP. These tests are about the three things the layer adds
// to what the store already does: an upload that arrives as multipart and is served
// back as an image, an icon reference that is refused where it is written rather
// than where it is drawn, and a group whose settings can be written before any
// sensor carries its name.

// pngBytes is a real PNG of the given size. It has to decode: an upload is read
// before it is stored, so a hand-written header would be refused by the very check
// these tests are about.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode a %dx%d PNG: %v", w, h, err)
	}
	return buf.Bytes()
}

// uploadIcon posts one file the way the dashboard's file input does.
func (h *harness) uploadIcon(filename, name string, data []byte) *http.Response {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		h.t.Fatalf("build the form: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		h.t.Fatalf("write the file part: %v", err)
	}
	if name != "" {
		if err := mw.WriteField("name", name); err != nil {
			h.t.Fatalf("write the name field: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		h.t.Fatalf("close the form: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, h.http.URL+"/api/icons", &buf)
	if err != nil {
		h.t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", h.http.URL)
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		h.t.Fatalf("upload: %v", err)
	}
	return resp
}

// mustUpload uploads an image and returns the row the server made of it.
func mustUpload(t *testing.T, h *harness, name string) model.Icon {
	t.Helper()
	resp := h.uploadIcon(name+".png", name, pngBytes(t, 24, 24))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload %q: status = %d, body = %s", name, resp.StatusCode, readBody(t, resp))
	}
	var ic model.Icon
	decode(t, resp, &ic)
	return ic
}

// TestIconUploadListAndServe covers the whole life of an uploaded image over HTTP:
// in as multipart, listed as metadata, out again as bytes, and refused a delete
// while a sensor wears it.
func TestIconUploadListAndServe(t *testing.T) {
	h := newHarness(t)
	h.setup()

	data := pngBytes(t, 40, 20)
	resp := h.uploadIcon("fritzbox.png", "", data)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var ic model.Icon
	decode(t, resp, &ic)
	if ic.MediaType != "image/png" || ic.Width != 40 || ic.Height != 20 {
		t.Errorf("stored icon = %+v, want a 40x20 PNG", ic)
	}
	// The filename is the fallback name, without its extension: nobody typed one.
	if ic.Name != "fritzbox" {
		t.Errorf("icon name = %q, want the filename without its extension", ic.Name)
	}

	// The list carries the uploaded images, the built-in names and both limits: that
	// is what the picker is built from, and guessing any of the four would produce a
	// field that accepts a file and then refuses it.
	resp = h.do(http.MethodGet, "/api/icons", nil, nil)
	var list struct {
		Icons      []model.Icon `json:"icons"`
		Builtin    []string     `json:"builtin"`
		MediaTypes []string     `json:"media_types"`
		MaxBytes   int          `json:"max_bytes"`
		MaxSide    int          `json:"max_side"`
	}
	decode(t, resp, &list)
	if len(list.Icons) != 1 || list.Icons[0].ID != ic.ID {
		t.Errorf("the listing does not hold the uploaded icon: %+v", list.Icons)
	}
	if len(list.Builtin) != len(icon.Names) || list.MaxBytes != icon.MaxBytes {
		t.Errorf("the listing offers %d built-ins and a %d byte cap", len(list.Builtin), list.MaxBytes)
	}

	// The bytes, with the type they were stored as and a validator that answers the
	// second request with 304.
	resp = h.do(http.MethodGet, "/api/icons/"+itoa(ic.ID), nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serve icon: status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("served as %q, want image/png", ct)
	}
	etag := resp.Header.Get("ETag")
	raw := readBody(t, resp)
	if !bytes.Equal([]byte(raw), data) {
		t.Error("the served bytes are not the bytes that were uploaded")
	}
	if etag == "" {
		t.Fatal("no ETag on the image, so nothing can cache it")
	}
	resp = h.do(http.MethodGet, "/api/icons/"+itoa(ic.ID), nil, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("a request holding the validator got %d, want 304", resp.StatusCode)
	}
	resp.Body.Close()

	// A sensor wearing it, and the delete that is then refused.
	cresp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "fritzbox", "type": "ping", "target": "10.0.0.1",
		"icon": icon.UploadRef(ic.ID),
	}, origin(h))
	if cresp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor: status = %d, body = %s", cresp.StatusCode, readBody(t, cresp))
	}
	var sensor model.Sensor
	decode(t, cresp, &sensor)
	if sensor.Icon != icon.UploadRef(ic.ID) {
		t.Errorf("the sensor came back wearing %q", sensor.Icon)
	}

	resp = h.do(http.MethodDelete, "/api/icons/"+itoa(ic.ID), nil, origin(h))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleting an icon in use: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, "1 sensor") {
		t.Errorf("the refusal does not say what wears it: %s", body)
	}

	// And with clear, it goes — taking the reference off the sensor with it.
	resp = h.do(http.MethodDelete, "/api/icons/"+itoa(ic.ID)+"?clear=1", nil, origin(h))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete with clear: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	// Looked up by name: the harness's setup seeds the example sensors, so "the
	// first one in the list" is not this sensor.
	worn := ""
	for _, c := range h.listSensors() {
		if c.Name == "fritzbox" {
			worn = c.Icon
		}
	}
	if worn != "" {
		t.Errorf("after clearing, the sensor still wears %q", worn)
	}
}

// TestIconUploadRefusesWhatIsNotAnImage is the allowlist over HTTP.
//
// The SVG case is the one that matters: it is a document that may carry script,
// served from this dashboard's own origin, where that script would run with the
// session cookie of whoever looked at it.
func TestIconUploadRefusesWhatIsNotAnImage(t *testing.T) {
	h := newHarness(t)
	h.setup()

	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "has to be"},
		{"notebook.png", []byte("just some text with the wrong extension"), "has to be"},
		{"huge.png", append(pngBytes(t, 8, 8), make([]byte, icon.MaxBytes)...), "may be at most"},
		{"wide.png", pngBytes(t, icon.MaxSide+1, 2), "at most 512×512"},
	} {
		resp := h.uploadIcon(tc.name, "", tc.data)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", tc.name, resp.StatusCode, readBody(t, resp))
			continue
		}
		if body := readBody(t, resp); !strings.Contains(body, tc.want) {
			t.Errorf("%s: refusal = %q, want it to mention %q", tc.name, body, tc.want)
		}
	}

	// Nothing was stored: a refused upload must not leave a row the picker then
	// offers as a broken image.
	resp := h.do(http.MethodGet, "/api/icons", nil, nil)
	var list struct {
		Icons []model.Icon `json:"icons"`
	}
	decode(t, resp, &list)
	if len(list.Icons) != 0 {
		t.Errorf("a refused upload was stored: %+v", list.Icons)
	}
}

// TestSensorRefusesAnIconThatIsNotThere is the check happening where the value is
// written. An icon that cannot be resolved draws nothing and says nothing, so a typo
// in a name would go unnoticed until somebody looked at the one sensor without a
// symbol.
func TestSensorRefusesAnIconThatIsNotThere(t *testing.T) {
	h := newHarness(t)
	h.setup()

	for _, tc := range []struct{ icon, want string }{
		{"builtin:nosuchthing", "no built-in icon"},
		{"upload:42", "no uploaded icon"},
		{"router", "is not an icon"},
		{"http://example.com/x.png", "is not an icon"},
	} {
		resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
			"name": "web", "type": "http", "target": "https://example.com",
			"icon": tc.icon,
		}, origin(h))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("icon %q: status = %d, want 400 (body %s)", tc.icon, resp.StatusCode, readBody(t, resp))
			continue
		}
		if body := readBody(t, resp); !strings.Contains(body, tc.want) {
			t.Errorf("icon %q: refusal = %q, want it to mention %q", tc.icon, body, tc.want)
		}
	}

	// A built-in name and an upload that exists are both accepted, and the site's
	// own icon goes through the same door.
	ic := mustUpload(t, h, "router")
	for _, ref := range []string{icon.BuiltinRef("router"), icon.UploadRef(ic.ID)} {
		resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
			"name": "web-" + strings.TrimPrefix(ref, "upload:"), "type": "http",
			"target": "https://example.com", "icon": ref,
		}, origin(h))
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("icon %q was refused: %s", ref, readBody(t, resp))
			continue
		}
		var c model.Sensor
		decode(t, resp, &c)
		if c.Icon != ref {
			t.Errorf("stored icon = %q, want %q", c.Icon, ref)
		}
	}

	resp := h.do(http.MethodPost, "/api/sites", map[string]any{
		"name": "keller", "kind": "local", "icon": icon.BuiltinRef("server"),
	}, origin(h))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("site with an icon: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	site := h.listSites()[0]
	if site.Icon != icon.BuiltinRef("server") {
		t.Errorf("the site came back with icon %q", site.Icon)
	}
}

// TestGroupsOverHTTP is the group editor's whole surface: a group that exists
// because a sensor names it, settings written onto it, a refusal for a colour that
// is not in the palette, and a forget that leaves the sensors where they are.
func TestGroupsOverHTTP(t *testing.T) {
	h := newHarness(t)
	h.setup()

	placeSensor(t, h, "router", "")
	placeSensor(t, h, "nas", "")
	for _, name := range []string{"router", "nas"} {
		// The group is typed on the sensor, which is the only way a group comes into
		// being at all.
		resp := h.do(http.MethodPut, "/api/sensors/"+itoa(sensorID(t, h, name)),
			map[string]any{"group": "Netz"}, origin(h))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("give %s a group: status = %d, body = %s", name, resp.StatusCode, readBody(t, resp))
		}
		resp.Body.Close()
	}

	resp := h.do(http.MethodGet, "/api/groups", nil, nil)
	var listed struct {
		Groups  []model.Group `json:"groups"`
		Accents []string      `json:"accents"`
	}
	decode(t, resp, &listed)
	// Netz is looked up rather than assumed to be the only one: the harness's setup
	// seeds the example sensors, and those carry a group of their own.
	netz, ok := groupNamed(listed.Groups, "Netz")
	if !ok || netz.SensorCount != 2 {
		t.Fatalf("groups = %+v, want Netz with two sensors", listed.Groups)
	}
	if len(listed.Accents) != len(model.Accents) {
		t.Errorf("the listing offers %d colours, want the palette's %d", len(listed.Accents), len(model.Accents))
	}

	// The settings, written by the editor.
	resp = h.do(http.MethodPut, "/api/groups/Netz", map[string]any{
		"icon": icon.BuiltinRef("router"), "colour": "amber", "sort_order": 2,
	}, origin(h))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set group: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var saved model.Group
	decode(t, resp, &saved)
	if saved.Icon != icon.BuiltinRef("router") || saved.Colour != "amber" || saved.SortOrder != 2 {
		t.Errorf("saved group = %+v", saved)
	}
	if saved.SensorCount != 2 {
		t.Errorf("the answer says %d sensors, want 2", saved.SensorCount)
	}

	// A colour that is not one of the six, and an icon that does not exist.
	for _, body := range []map[string]any{
		{"colour": "#f59e0b"},
		{"icon": icon.BuiltinRef("nope")},
	} {
		resp = h.do(http.MethodPut, "/api/groups/Netz", body, origin(h))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status = %d, want 400 (body %s)", body, resp.StatusCode, readBody(t, resp))
		}
	}

	// A group nobody has arranged yet: its settings row is made by this very call,
	// which is why there is no separate create.
	resp = h.do(http.MethodPut, "/api/groups/Drucker", map[string]any{"colour": "rose"}, origin(h))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings for a group no sensor names: status = %d, body = %s",
			resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// Forgetting one leaves the sensors exactly where they were.
	resp = h.do(http.MethodDelete, "/api/groups/Netz", nil, origin(h))
	var forgotten struct {
		Forgotten string `json:"forgotten"`
		Sensors   int    `json:"sensors"`
	}
	decode(t, resp, &forgotten)
	if forgotten.Sensors != 2 {
		t.Errorf("forgetting Netz reports %d sensors, want 2", forgotten.Sensors)
	}
	resp = h.do(http.MethodGet, "/api/groups", nil, nil)
	decode(t, resp, &listed)
	for _, g := range listed.Groups {
		if g.Name == "Netz" && (g.Icon != "" || g.Colour != "") {
			t.Errorf("Netz still has settings after being forgotten: %+v", g)
		}
	}
	if len(h.listSensors()) != 6 {
		t.Error("forgetting a group changed the sensors")
	}
}

// groupNamed picks one group out of the list, since the harness's example data
// brings a group of its own.
func groupNamed(groups []model.Group, name string) (model.Group, bool) {
	for _, g := range groups {
		if g.Name == name {
			return g, true
		}
	}
	return model.Group{}, false
}

// sensorID finds a sensor's id by name, for the routes that take one.
func sensorID(t *testing.T, h *harness, name string) int64 {
	t.Helper()
	for _, c := range h.listSensors() {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("no sensor called %q", name)
	return 0
}

// ---------------------------------------------------------------------------
// the online catalogue
// ---------------------------------------------------------------------------

// onlineCatalogue stands in for dashboardicons.com: an index of two icons and a
// real PNG for one of them. The picture has to decode, because the import runs it
// through the same check an upload does.
func onlineCatalogue(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	picture := pngBytes(t, 32, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("/metadata.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
		  "pi-hole": {"base": "svg", "aliases": ["pihole", "dns"], "categories": ["Network"]},
		  "plex":    {"base": "png", "aliases": [], "categories": ["Media"]}
		}`))
	})
	mux.HandleFunc("/png/pi-hole.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(picture)
	})
	// A wide logo, which is the shape the catalogue really has and which this
	// program's own limit would refuse as it stands.
	mux.HandleFunc("/png/wide-logo.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 623, 512))
	})
	// And a picture that is over the byte limit rather than the pixel one. It has to
	// arrive whole: cut to the icon limit it could not be decoded, let alone reduced.
	mux.HandleFunc("/png/heavy.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(noisyPNG(t, 800, 800))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, picture
}

// useCatalogue points the harness's server at a stand-in for the online catalogue.
// It has to happen before the first request to the search routes: a handler reads
// the field per request.
func (h *harness) useCatalogue(url string) {
	h.t.Helper()
	cat := icon.NewCatalogue()
	cat.IndexURL = url + "/metadata.json"
	cat.ImageURL = url + "/png/"
	h.server.catalogue = cat
}

// searchAnswer is what GET /api/icons/search answers with.
type searchAnswer struct {
	Source  string               `json:"source"`
	Offline bool                 `json:"offline"`
	Results []icon.CatalogueIcon `json:"results"`
}

func TestTheOnlineSearchFindsAnIconByItsAlias(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv, _ := onlineCatalogue(t)
	h.useCatalogue(srv.URL)

	resp := h.do(http.MethodGet, "/api/icons/search?q=pihole", nil, nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("search returned %d", resp.StatusCode)
	}
	var got searchAnswer
	decode(t, resp, &got)
	if got.Offline {
		t.Fatal("the search said the catalogue was unreachable, and it was not")
	}
	if got.Source != "dashboardicons.com" {
		t.Errorf("the answer credits %q, want dashboardicons.com", got.Source)
	}
	if len(got.Results) != 1 || got.Results[0].Name != "pi-hole" {
		t.Fatalf("searching for pihole answered %+v, want pi-hole", got.Results)
	}
}

func TestTheOnlineSearchSaysSoWhenTheCatalogueIsUnreachable(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv, _ := onlineCatalogue(t)
	srv.Close()
	h.useCatalogue(srv.URL)

	resp := h.do(http.MethodGet, "/api/icons/search?q=plex", nil, nil)
	defer resp.Body.Close()
	// Not an error status: a network the container cannot reach is a state, not a
	// fault in the request, and the dashboard draws it as a sentence.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a search with no network returned %d, want 200 with offline set", resp.StatusCode)
	}
	var got searchAnswer
	decode(t, resp, &got)
	if !got.Offline {
		t.Fatal("the answer did not say that the catalogue was unreachable")
	}
	if len(got.Results) != 0 {
		t.Errorf("an unreachable catalogue answered with %d results", len(got.Results))
	}
}

func TestImportingAnOnlineIconStoresItLikeAnUpload(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv, picture := onlineCatalogue(t)
	h.useCatalogue(srv.URL)

	resp := h.do(http.MethodPost, "/api/icons/import", map[string]string{"name": "pi-hole"}, nil)
	if resp.StatusCode != http.StatusCreated {
		body := new(strings.Builder)
		_, _ = io.Copy(body, resp.Body)
		resp.Body.Close()
		t.Fatalf("import returned %d: %s", resp.StatusCode, body)
	}
	var stored model.Icon
	decode(t, resp, &stored)
	if stored.Name != "pi-hole" {
		t.Errorf("the imported icon is called %q", stored.Name)
	}
	if stored.ID == 0 {
		t.Fatal("the imported icon has no id")
	}

	// And it is an ordinary uploaded icon from here on: served by the same route,
	// listed with the rest, with nothing left that points at the internet.
	bytesResp := h.do(http.MethodGet, "/api/icons/"+strconv.FormatInt(stored.ID, 10), nil, nil)
	defer bytesResp.Body.Close()
	got, err := io.ReadAll(bytesResp.Body)
	if err != nil {
		t.Fatalf("read the stored icon: %v", err)
	}
	if !bytes.Equal(got, picture) {
		t.Fatal("the stored icon is not the picture the catalogue served")
	}
	if ct := bytesResp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("the stored icon is served as %q", ct)
	}
}

func TestThePreviewShowsAPictureWithoutStoringIt(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv, picture := onlineCatalogue(t)
	h.useCatalogue(srv.URL)

	resp := h.do(http.MethodGet, "/api/icons/preview/pi-hole", nil, nil)
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read the preview: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview returned %d", resp.StatusCode)
	}
	if !bytes.Equal(got, picture) {
		t.Fatal("the preview is not the picture the catalogue served")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("the preview is served as %q", ct)
	}

	var listed struct {
		Icons []model.Icon `json:"icons"`
	}
	decode(t, h.do(http.MethodGet, "/api/icons", nil, nil), &listed)
	if len(listed.Icons) != 0 {
		t.Fatalf("a preview stored %d icon(s); it is a look, not an import", len(listed.Icons))
	}
}

// A picture from the catalogue that is bigger than this program's limit is made to
// fit instead of being refused. The catalogue holds 623×512 logos, and a search that
// offers a symbol the import then refuses is a search nobody can use.
func TestImportingAWideOnlineIconShrinksIt(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv, _ := onlineCatalogue(t)
	h.useCatalogue(srv.URL)

	// It can be looked at first: a preview of a picture too wide to store is exactly
	// the case where the browser would otherwise draw a broken image.
	preview := h.do(http.MethodGet, "/api/icons/preview/wide-logo", nil, nil)
	preview.Body.Close()
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("previewing a wide logo returned %d", preview.StatusCode)
	}

	resp := h.do(http.MethodPost, "/api/icons/import", map[string]string{"name": "wide-logo"}, nil)
	if resp.StatusCode != http.StatusCreated {
		body := new(strings.Builder)
		_, _ = io.Copy(body, resp.Body)
		resp.Body.Close()
		t.Fatalf("importing a wide logo returned %d: %s", resp.StatusCode, body)
	}
	var stored model.Icon
	decode(t, resp, &stored)
	if stored.Width != 512 {
		t.Errorf("the stored picture is %d pixels wide, want 512", stored.Width)
	}
	if stored.Height < 419 || stored.Height > 423 {
		t.Errorf("the stored picture is %d pixels high, want the 512-wide logo's proportion", stored.Height)
	}
}

// noisyPNG is a real PNG that will not compress: every pixel differs, which is how a
// picture of the right shape still ends up over the byte limit.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			at := img.PixOffset(x, y)
			img.Pix[at] = uint8(x*7 + y*13)
			img.Pix[at+1] = uint8(x*x + y*3)
			img.Pix[at+2] = uint8(x*11 ^ y*5)
			img.Pix[at+3] = uint8(200 + (x+y)%56)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode a noisy %dx%d PNG: %v", w, h, err)
	}
	return buf.Bytes()
}

// A picture over the byte limit is the other way the catalogue does not fit this
// program's rules. It used to come back cut in half — fetch stopped reading at the
// icon limit — and the refusal said "this one is 65537 bytes", which is the length of
// the cut, not of the picture.
func TestImportingAHeavyOnlineIconShrinksIt(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv, _ := onlineCatalogue(t)
	h.useCatalogue(srv.URL)

	resp := h.do(http.MethodPost, "/api/icons/import", map[string]string{"name": "heavy"}, nil)
	if resp.StatusCode != http.StatusCreated {
		body := new(strings.Builder)
		_, _ = io.Copy(body, resp.Body)
		resp.Body.Close()
		t.Fatalf("importing a heavy picture returned %d: %s", resp.StatusCode, body)
	}
	var stored model.Icon
	decode(t, resp, &stored)
	if stored.Size > icon.MaxBytes {
		t.Errorf("the stored picture is %d bytes, and the limit is %d", stored.Size, icon.MaxBytes)
	}
	if stored.Width > icon.MaxSide || stored.Height > icon.MaxSide {
		t.Errorf("the stored picture is %dx%d", stored.Width, stored.Height)
	}
}

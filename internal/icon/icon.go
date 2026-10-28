// Package icon is what a sensor, a site or a group carries as its symbol, and
// what an uploaded image is allowed to be.
//
// A symbol is one string, and it says one of three things. Empty means none: an
// installation that never touches this feature looks exactly as it did before,
// because the column's default is the empty string and nothing draws for it. A
// `builtin:` name is one of the drawings that ship inside the binary — the ones
// that follow the theme's colour, because a homelab has more routers than logos.
// An `upload:` number is an image somebody put in the database themselves.
//
// Two forms with a prefix rather than one bare word, because the two are not
// interchangeable: a built-in is a name this program knows and an upload is a row
// in this installation's database, and a bare `router` that meant both would be
// ambiguous the moment somebody uploads an icon and calls it that.
package icon

import (
	"fmt"
	"strconv"
	"strings"
)

// The two prefixes a reference may carry.
const (
	PrefixBuiltin = "builtin:"
	PrefixUpload  = "upload:"
	PrefixBrand   = "brand:"
)

// Brand icons are the vendor and application marks bundled with the templates.
// They are images with their own colours, not the theme-following line art of the
// built-ins, so they are a kind of their own.
var brandNames = map[string]bool{
	"qnap": true, "synology": true, "unifi": true, "apc": true,
	"mikrotik": true, "pfsense": true, "proxmox": true, "truenas": true,
	"mos": true, "home-assistant": true, "pi-hole": true, "adguard": true,
	"grafana": true, "portainer": true, "jellyfin": true, "uptime-kuma": true,
	"node-red": true, "mosquitto": true, "shelly": true, //nolint:misspell // the MQTT broker
	"sonarr": true, "radarr": true, "lidarr": true, "prowlarr": true,
	"bazarr": true, "overseerr": true, "jellyseerr": true, "paperless-ngx": true,
	"ubiquiti": true, "unifi-protect": true,
}

// Kind is where a reference points.
type Kind uint8

const (
	// KindNone is the empty reference: nothing is drawn, and that is the
	// ordinary state of every row that existed before this feature.
	KindNone Kind = iota
	// KindBuiltin is one of the drawings that ship with the program.
	KindBuiltin
	// KindUpload is an image in this installation's own database.
	KindUpload
	// KindBrand is one of the vendor marks bundled with the templates.
	KindBrand
	// KindUnknown is a string that is neither of the above. It is never stored —
	// both the API and the configuration file refuse it — but a database that
	// acquired one by hand resolves to it rather than to a crash, and the
	// dashboard draws nothing for it.
	KindUnknown
)

// Parse reads a reference. The value is the built-in's name or the upload's id
// as a string, and is empty for KindNone and KindUnknown.
func Parse(ref string) (Kind, string) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return KindNone, ""
	case strings.HasPrefix(ref, PrefixBuiltin):
		name := strings.TrimPrefix(ref, PrefixBuiltin)
		if IsBuiltin(name) {
			return KindBuiltin, name
		}
		return KindUnknown, ""
	case strings.HasPrefix(ref, PrefixUpload):
		id := strings.TrimPrefix(ref, PrefixUpload)
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return KindUnknown, ""
		}
		return KindUpload, id
	case strings.HasPrefix(ref, PrefixBrand):
		name := strings.TrimPrefix(ref, PrefixBrand)
		if brandNames[name] {
			return KindBrand, name
		}
		return KindUnknown, ""
	}
	return KindUnknown, ""
}

// Check refuses a reference that is not one of the three forms.
//
// It is the shape and not the existence: a `builtin:` name has to be one this
// program ships, and an `upload:` has to be a number, but whether that number is a
// row in this installation's database is a question only the database can answer —
// see store.ValidateIconRef, which calls this first.
//
// The two mistakes are refused here rather than at drawing time because neither
// says anything when it is wrong: an icon that cannot be resolved draws nothing,
// silently, and a typo in a name would go unnoticed until somebody looked at the
// one sensor that has no symbol. A message where the value is written is the only
// place anybody can act on it.
func Check(ref string) error {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	kind, _ := Parse(ref)
	if kind == KindBuiltin || kind == KindUpload || kind == KindBrand {
		return nil
	}
	if name, ok := strings.CutPrefix(strings.TrimSpace(ref), PrefixBuiltin); ok {
		return fmt.Errorf("there is no built-in icon %q", name)
	}
	if name, ok := strings.CutPrefix(strings.TrimSpace(ref), PrefixBrand); ok {
		return fmt.Errorf("there is no bundled brand icon %q", name)
	}
	return fmt.Errorf("%q is not an icon: use \"\", a built-in name like %q, a bundled brand like %q, or an uploaded one like %q",
		ref, BuiltinRef("router"), BrandRef("qnap"), UploadRef(1))
}

// BrandRef is the reference to a bundled brand icon.
func BrandRef(name string) string { return PrefixBrand + name }

// UploadID returns the id an `upload:` reference names, or 0.
func UploadID(ref string) int64 {
	kind, value := Parse(ref)
	if kind != KindUpload {
		return 0
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// BuiltinRef is the reference to a built-in drawing.
func BuiltinRef(name string) string { return PrefixBuiltin + name }

// UploadRef is the reference to an uploaded image.
func UploadRef(id int64) string { return PrefixUpload + strconv.FormatInt(id, 10) }

// IsBuiltin reports whether name is one of the drawings that ship with the
// program.
func IsBuiltin(name string) bool {
	for _, n := range Names {
		if n == name {
			return true
		}
	}
	return false
}

// Names are the built-in drawings, in the order the picker offers them.
//
// The order is the order of a rack rather than an alphabet: the things a homelab
// is made of first, then the things attached to it, then the abstractions — so
// that the one somebody wants is where they look for it.
//
// Each name is a file of the same name under `internal/web/assets/icons/`, and
// `TestBuiltinIconsMatchTheFiles` in internal/web holds the two lists together in
// both directions: a name without a drawing would be an entry that draws nothing,
// and a drawing without a name would be a file nobody can choose.
var Names = []string{
	"router",
	"switch",
	"server",
	"nas",
	"container",
	"cloud",
	"globe",
	"database",
	"disk",
	"cpu",
	"wifi",
	"camera",
	"printer",
	"battery",
	"plug",
	"thermometer",
	"bulb",
	"lock",
	"certificate",
	"bell",
	"mail",
	"phone",
	"laptop",
	"monitor",
	"clock",
	"tag",
}

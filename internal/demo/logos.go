package demo

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// The four pictures the example data uploads.
//
// They are brand marks rather than drawings of ours, and they are here on purpose:
// the symbol picker offers two kinds of picture — the drawings that ship in the
// binary and the images somebody uploaded — and the demo data is the one place a
// person sees both without doing anything. A dataset of built-ins only would show
// half the feature.
//
// They come from Dashboard Icons (homarr-labs/dashboard-icons, Apache-2.0), already
// reduced by icon.Fit to the size and weight this program keeps an icon at. The
// marks belong to the projects they name; this is example data, and it is the first
// thing anybody is invited to remove.
//
//go:embed logos/*.png
var logoFiles embed.FS

// logo is one of them: the file, the name it is given in the picker, and the
// drawing it stands for.
//
// The last one is not decoration. The website's demo is a recording of static files
// and the dashboard asks for an uploaded picture as an image at a route only a
// server can answer, so the recording uses the drawing instead — see
// RecordBuiltinIcons.
type logo struct {
	file    string
	name    string
	builtin string
}

var logos = []logo{
	{file: "cloudflare", name: "Cloudflare", builtin: "globe"},
	{file: "debian", name: "Debian", builtin: "database"},
	{file: "docker", name: "Docker", builtin: "container"},
	{file: "truenas", name: "TrueNAS", builtin: "nas"},
}

// EnsureLogos puts the example pictures in the icons table and answers the reference
// for each: upload:<id>, the form the icon columns hold.
//
// A picture that is already there under its name is reused rather than added again,
// so seeding twice leaves four icons and not eight. It also means somebody who
// uploaded one of these marks themselves keeps the copy they chose.
//
// The rows stay when the example data is removed. An icon is a picture, not example
// data: it may already be worn by a sensor somebody made, and "remove the invented
// sensors" is not a request to take their symbols away.
func EnsureLogos(ctx context.Context, st *store.Store) (map[string]string, error) {
	existing, err := st.ListIcons(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]model.Icon, len(existing))
	for _, ic := range existing {
		byName[strings.ToLower(ic.Name)] = ic
	}

	refs := make(map[string]string, len(logos))
	for _, l := range logos {
		if ic, ok := byName[strings.ToLower(l.name)]; ok {
			refs[l.file] = icon.UploadRef(ic.ID)
			continue
		}
		data, err := logoFiles.ReadFile("logos/" + l.file + ".png")
		if err != nil {
			return nil, fmt.Errorf("read the %s example icon: %w", l.name, err)
		}
		ic := model.Icon{Name: l.name}
		if err := st.CreateIcon(ctx, &ic, data); err != nil {
			return nil, fmt.Errorf("add the %s example icon: %w", l.name, err)
		}
		refs[l.file] = icon.UploadRef(ic.ID)
	}
	return refs, nil
}

// RecordBuiltinIcons turns the uploaded example pictures back into drawings.
//
// It exists for one caller: the website's demo, which is a folder of static files.
// The dashboard draws an uploaded icon with <img src="/api/icons/7">, and a static
// host has no server behind that path — so a recording that referenced an upload
// would show a broken image where a logo belongs. Recording the drawing it stands
// for keeps the demo honest about what it is: a picture of the dashboard, not a
// database.
func RecordBuiltinIcons(ctx context.Context, st *store.Store) error {
	wanted := make(map[string]string, len(logos))
	for _, d := range sensors {
		if d.logo == "" {
			continue
		}
		for _, l := range logos {
			if l.file == d.logo {
				wanted[d.name] = icon.BuiltinRef(l.builtin)
			}
		}
	}
	for name, ref := range wanted {
		c, err := st.GetSensorByName(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if c.Icon == ref {
			continue
		}
		c.Icon = ref
		if err := st.UpdateSensor(ctx, &c); err != nil {
			return fmt.Errorf("record %q with a drawing: %w", name, err)
		}
	}
	return nil
}

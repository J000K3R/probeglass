package icon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The online catalogue at dashboardicons.com.
//
// This is deliberately not a second source of symbols the dashboard draws from:
// what a search turns up is fetched once and stored as an uploaded image, so it
// lands in the one SQLite file like every other picture and the dashboard never
// asks a third party for anything in order to draw a page. The catalogue is a
// place to look something up, not a place to depend on.
//
// The site itself has no search endpoint that filters — /api/icons/search answers
// with the whole catalogue whatever it is asked — so the thing worth fetching is
// the index behind it, which is a static file on the project's GitHub repository
// and honours a conditional request.

const (
	// CatalogueURL is the project's index: every icon it knows, with the aliases
	// and the categories somebody wrote down for it. About 1.2 MB, which is why
	// it is revalidated rather than re-downloaded.
	CatalogueURL = "https://raw.githubusercontent.com/homarr-labs/dashboard-icons/refs/heads/main/metadata.json"

	// CatalogueImageURL is where the pictures are. The project keeps a PNG of every
	// icon beside the SVG it was drawn as, and the PNG is what this program takes:
	// see MediaTypes for why an SVG from somewhere else is not an option.
	CatalogueImageURL = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/"

	// catalogueMaxIndex bounds the index a little above its current size, so that a
	// response that is not the index at all cannot be read into memory.
	catalogueMaxIndex = 8 << 20

	// catalogueTimeout is how long one lookup may take, both for the index and for
	// one picture. A homelab whose DNS is slow should get the "not right now"
	// answer, not a dashboard that waits.
	catalogueTimeout = 15 * time.Second
)

var (
	// ErrCatalogueUnavailable is the answer when dashboardicons could not be
	// reached. It is a state of the network rather than a fault: a container with
	// no route out says this every time, and the dashboard says so in a sentence.
	ErrCatalogueUnavailable = errors.New("the online icon catalogue could not be reached")

	// ErrBadIconName is a name that cannot be turned into a URL on the catalogue's
	// host. It is a programming error at the call site — the names come from the
	// search — and it is refused here rather than trusted.
	ErrBadIconName = errors.New("that is not a name from the online catalogue")
)

// catalogueSlug is the shape a name from the catalogue has to have before it is
// pasted into a URL: lower case, digits, and the three separators the project
// uses. No slash, no dot-dot, nothing that could name another host or another
// path than the one intended.
var catalogueSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// CatalogueIcon is one entry of the online catalogue.
type CatalogueIcon struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	Categories []string `json:"categories,omitempty"`
}

// Catalogue fetches the online index, remembers it between searches, and hands
// back the picture for one name.
//
// The index is remembered because it is 1.2 MB and would otherwise be downloaded
// on every keystroke. It is memory only: the file is not stored, so a restart
// starts from the network again, and nothing about a search survives in the
// database.
type Catalogue struct {
	// IndexURL and ImageURL are fields rather than constants so that a test can
	// point them at a local server. Nothing else reads them.
	IndexURL string
	ImageURL string
	Client   *http.Client

	mu    sync.Mutex
	icons []CatalogueIcon
	etag  string
}

// NewCatalogue is the catalogue as the server uses it.
func NewCatalogue() *Catalogue {
	return &Catalogue{
		IndexURL: CatalogueURL,
		ImageURL: CatalogueImageURL,
		Client:   &http.Client{Timeout: catalogueTimeout},
	}
}

// Search answers the icons whose name or alias contains term, best match first.
//
// It revalidates the index before it answers, with the validator the last download
// gave it, so that an icon added to the project since this process started is
// found by the search that is looking for it — and so that the common case costs
// one request with no body rather than 1.2 MB.
//
// A network failure is ErrCatalogueUnavailable and not a search over the copy in
// memory: a person who is told "nothing found" would go and look for the icon in
// the wrong place, and the sentence that says the catalogue could not be reached
// is the one that helps.
func (c *Catalogue) Search(ctx context.Context, term string, limit int) ([]CatalogueIcon, error) {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 40
	}
	c.mu.Lock()
	err := c.refresh(ctx)
	icons := c.icons
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	type hit struct {
		icon CatalogueIcon
		rank int
	}
	hits := make([]hit, 0, 8)
	for _, ic := range icons {
		if rank, ok := matchRank(ic, term); ok {
			hits = append(hits, hit{icon: ic, rank: rank})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].icon.Name < hits[j].icon.Name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]CatalogueIcon, len(hits))
	for i, h := range hits {
		out[i] = h.icon
	}
	return out, nil
}

// matchRank says how well one icon answers a search term, and whether it answers
// it at all. Lower is better: the icon somebody typed exactly comes before one
// whose name starts with what they typed, which comes before an alias, which
// comes before a name that only contains it somewhere in the middle.
func matchRank(ic CatalogueIcon, term string) (int, bool) {
	name := strings.ToLower(ic.Name)
	switch {
	case name == term:
		return 0, true
	case strings.HasPrefix(name, term):
		return 1, true
	}
	for _, alias := range ic.Aliases {
		if strings.ToLower(alias) == term {
			return 2, true
		}
	}
	for _, alias := range ic.Aliases {
		if strings.HasPrefix(strings.ToLower(alias), term) {
			return 3, true
		}
	}
	if strings.Contains(name, term) {
		return 4, true
	}
	for _, alias := range ic.Aliases {
		if strings.Contains(strings.ToLower(alias), term) {
			return 5, true
		}
	}
	return 0, false
}

// Fetch downloads the PNG for one name. It is the whole of what is taken from the
// online catalogue: the caller decodes it with Decode, which is what decides
// whether it is an image this program will store.
func (c *Catalogue) Fetch(ctx context.Context, name string) ([]byte, error) {
	if !catalogueSlug.MatchString(name) {
		return nil, ErrBadIconName
	}
	ctx, cancel := context.WithTimeout(ctx, catalogueTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ImageURL+name+".png", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadIconName, err)
	}
	resp, err := c.Client.Do(req) //nolint:bodyclose // closed below, with the error path included
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalogueUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("the online catalogue has no PNG for %q", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: the catalogue answered %s", ErrCatalogueUnavailable, resp.Status)
	}
	// One byte over the limit, so that Decode can say how large the file really is
	// rather than only that it is too large.
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalogueUnavailable, err)
	}
	return data, nil
}

// refresh revalidates the index. It must be called with mu held.
func (c *Catalogue) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, catalogueTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.IndexURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCatalogueUnavailable, err)
	}
	if c.etag != "" {
		req.Header.Set("If-None-Match", c.etag)
	}
	resp, err := c.Client.Do(req) //nolint:bodyclose // closed below, with the error path included
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCatalogueUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && len(c.icons) > 0 {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: the index answered %s", ErrCatalogueUnavailable, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, catalogueMaxIndex))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCatalogueUnavailable, err)
	}
	icons, err := parseCatalogue(body)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCatalogueUnavailable, err)
	}
	c.icons = icons
	c.etag = resp.Header.Get("ETag")
	return nil
}

// parseCatalogue reads the project's index, which is one object keyed by name.
func parseCatalogue(body []byte) ([]CatalogueIcon, error) {
	var raw map[string]struct {
		Aliases    []string `json:"aliases"`
		Categories []string `json:"categories"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("the index could not be read: %w", err)
	}
	icons := make([]CatalogueIcon, 0, len(raw))
	for name, entry := range raw {
		icons = append(icons, CatalogueIcon{
			Name:       name,
			Aliases:    entry.Aliases,
			Categories: entry.Categories,
		})
	}
	sort.Slice(icons, func(i, j int) bool { return icons[i].Name < icons[j].Name })
	return icons, nil
}

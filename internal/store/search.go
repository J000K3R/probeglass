package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// Search is the one query behind the box in the top bar.
//
// It is deliberately not a sensor search that grew: somebody who types a word
// into a field labelled "Search" on a monitoring dashboard is asking where that
// word appears anywhere, and an answer that silently leaves out the channel that
// failed to send and the probe message from last Tuesday is worse than no answer.
//
// Three things bound it, and each one is there for a different reason.
//
// The **kinds** it searches are fixed and short, because the cost of a scan grows
// with the list and these lists are the whole of what a homelab installation
// holds: sensors, sites, channels, incidents, accounts and raw probe messages.
// Everything else — rollups, recovery codes, the settings table — is either
// derived from these or is not a thing a person looks for by name.
//
// The **fields** are chosen to exclude secrets. A search box that will happily
// hand back a bot token or a password hash to whoever can read the page is worse
// than one that finds nothing. So `sensors.options`, `channels.config`, every
// credential column of `users` and the rolled-up tables are never read here, and
// that is a decision rather than an omission — see the comment on each query.
//
// The **bounds** are per kind, plus a time window on the one table that grows
// without limit. Everything else is a list of dozens or low hundreds of rows and
// a scan of it is under a millisecond; `results` accumulates one row per probe
// per sensor forever, and a keystroke must not walk all of it.

const (
	// SearchMinRunes is the shortest query worth running. One character matches
	// most of the installation, which is not a search — it is a redraw.
	SearchMinRunes = 2
	// SearchMaxRunes is the longest query accepted. Anything longer is a pasted
	// paragraph, and searching for it can only ever return nothing.
	SearchMaxRunes = 64

	// defaultSearchPerKind and maxSearchPerKind bound each query. The default is
	// what the dashboard asks for; the maximum is what a caller may ask for.
	defaultSearchPerKind = 20
	maxSearchPerKind     = 50
	// maxSearchHits is the ceiling on the whole answer, however many kinds filled
	// their quota.
	maxSearchHits = 200
)

// SearchHit is one thing the search found, flattened to what one result row shows.
//
// It carries no secrets and no ids that would be meaningless elsewhere: Ref is
// what the browser needs to jump to the thing, and for the two history kinds it is
// the sensor, because a probe message or an incident is read in the context of the
// sensor it belongs to and neither has a screen of its own to open.
type SearchHit struct {
	// Kind is one of sensor, site, channel, incident, result, user.
	Kind string `json:"kind"`
	// Ref is the id (as a string) or the name the browser navigates by.
	Ref string `json:"ref"`
	// Title is the line read first: a sensor's name, an incident's cause, a probe
	// message.
	Title string `json:"title"`
	// Sub is the smaller line under it: a target, a place, a channel type, the
	// sensor an incident belongs to.
	Sub string `json:"sub,omitempty"`
	// State says what the row is, where the database already knows: "open" or
	// "resolved" for an incident, "paused" for a switched-off channel or sensor,
	// "disabled" for an account. It is empty for a sensor's live status — that
	// lives in the scheduler's memory, not in a table, and the dashboard fills it
	// in from the state it already has.
	State string `json:"state,omitempty"`
	// At is when the thing happened, RFC 3339, for the two kinds that are events.
	At string `json:"at,omitempty"`
}

// SearchOptions is what the caller asks for. The zero value searches nothing.
type SearchOptions struct {
	// Query is the text to look for. It is matched literally, case-insensitively.
	Query string
	// PerKind bounds each kind. Zero means defaultSearchPerKind, and anything
	// above maxSearchPerKind is clamped.
	PerKind int
	// IncludeUsers adds account rows. The caller decides by role: a viewer may not
	// list accounts over the API, and a search must not be a second way round that.
	IncludeUsers bool
}

// SearchResult is the whole answer, including whether it was cut short.
type SearchResult struct {
	Hits []SearchHit `json:"hits"`
	// Truncated says that at least one kind had more to give. The dashboard says so
	// rather than letting a person believe they have seen everything.
	Truncated bool `json:"truncated"`
}

// Search looks for one string in everything worth looking in.
func (s *Store) Search(ctx context.Context, opts SearchOptions) (SearchResult, error) {
	out := SearchResult{Hits: []SearchHit{}}
	query := strings.TrimSpace(opts.Query)
	if len([]rune(query)) < SearchMinRunes {
		return out, nil
	}
	if len([]rune(query)) > SearchMaxRunes {
		query = string([]rune(query)[:SearchMaxRunes])
	}
	perKind := opts.PerKind
	if perKind <= 0 {
		perKind = defaultSearchPerKind
	}
	if perKind > maxSearchPerKind {
		perKind = maxSearchPerKind
	}

	// One read transaction so that the six answers describe the same moment. With
	// a single connection the difference is academic, but a result list that mixes
	// a sensor from before a change with an incident from after it would be a
	// small, permanent lie in a product whose whole claim is that its numbers are
	// right.
	var truncated bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		kinds := []func(context.Context, *sql.Tx, string, int) ([]SearchHit, bool, error){
			searchSensors,
			searchSites,
			searchChannels,
			searchIncidents,
			s.searchResults,
		}
		if opts.IncludeUsers {
			kinds = append(kinds, searchUsers)
		}
		for _, run := range kinds {
			hits, cut, err := run(ctx, tx, query, perKind)
			if err != nil {
				return err
			}
			if cut {
				truncated = true
			}
			out.Hits = append(out.Hits, hits...)
		}
		return nil
	})
	if err != nil {
		return SearchResult{Hits: []SearchHit{}}, err
	}

	if len(out.Hits) > maxSearchHits {
		out.Hits = out.Hits[:maxSearchHits]
		truncated = true
	}
	out.Truncated = truncated
	return out, nil
}

// searchSensors matches a sensor on what identifies it.
//
// `options` is not in the list and must not be added to it. That column holds
// `bearer_token`, `basic_auth_pass` and the SNMP `community` string — every one of
// them a credential — and a dashboard that answers "what is my basic auth
// password" by typing three letters into the search box has handed it to whoever
// is looking at the screen. The masked form the API returns is no help either: the
// value searched here is the real one.
func searchSensors(ctx context.Context, tx *sql.Tx, query string, limit int) ([]SearchHit, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, type, target, COALESCE(site,''), enabled, managed
		FROM sensors
		WHERE instr(lower(name || ' ' || target || ' ' || grp || ' ' ||
		              COALESCE(site,'') || ' ' || tags || ' ' || type), lower(?)) > 0
		ORDER BY sort_order ASC, name COLLATE NOCASE ASC
		LIMIT ?`, query, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var (
			id      int64
			name    string
			typ     string
			target  string
			site    string
			enabled int
			managed int
		)
		if err := rows.Scan(&id, &name, &typ, &target, &site, &enabled, &managed); err != nil {
			return nil, false, err
		}
		sub := target
		if site != "" {
			sub = target + " · " + site
		}
		hit := SearchHit{Kind: "sensor", Ref: itoa(id), Title: name, Sub: sub}
		// A paused sensor is worth marking, because "nothing is wrong with it" and
		// "nobody is watching it" are different answers to the same question.
		if enabled == 0 {
			hit.State = "paused"
		}
		hits = append(hits, hit)
	}
	hits, cut := clip(hits, limit)
	return hits, cut, rows.Err()
}

// searchSites matches a place on its name or its kind, which is all it has.
func searchSites(ctx context.Context, tx *sql.Tx, query string, limit int) ([]SearchHit, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, kind FROM sites
		WHERE instr(lower(name || ' ' || kind), lower(?)) > 0
		ORDER BY sort_order ASC, name COLLATE NOCASE ASC
		LIMIT ?`, query, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, false, err
		}
		hits = append(hits, SearchHit{Kind: "site", Ref: name, Title: name, Sub: kind})
	}
	hits, cut := clip(hits, limit)
	return hits, cut, rows.Err()
}

// searchChannels matches a channel on its name and its type.
//
// `config` is not in the list. It holds the Telegram bot token, the Discord and
// Slack webhook URLs and the Matrix access token; and even if searching it were
// harmless, a hit on the masked value (`__stored__`) would match every channel at
// once and tell the reader nothing.
func searchChannels(ctx context.Context, tx *sql.Tx, query string, limit int) ([]SearchHit, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, type, enabled FROM channels
		WHERE instr(lower(name || ' ' || type), lower(?)) > 0
		ORDER BY name COLLATE NOCASE ASC
		LIMIT ?`, query, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var (
			id      int64
			name    string
			typ     string
			enabled int
		)
		if err := rows.Scan(&id, &name, &typ, &enabled); err != nil {
			return nil, false, err
		}
		hit := SearchHit{Kind: "channel", Ref: itoa(id), Title: name, Sub: typ}
		if enabled == 0 {
			hit.State = "paused"
		}
		hits = append(hits, hit)
	}
	hits, cut := clip(hits, limit)
	return hits, cut, rows.Err()
}

// searchIncidents matches an outage on its cause, the note somebody left on it,
// and the sensor it belongs to.
//
// The sensor's name is part of what is searched because that is how somebody
// remembers an outage — "the time the NAS went" — while the cause is what the
// machine said.
func searchIncidents(ctx context.Context, tx *sql.Tx, query string, limit int) ([]SearchHit, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT i.sensor_id, i.cause, i.started_at, i.resolved_at, i.ack_note,
		       s.name, s.target
		FROM incidents i JOIN sensors s ON s.id = i.sensor_id
		WHERE instr(lower(i.cause || ' ' || i.ack_note || ' ' || s.name || ' ' || s.target),
		            lower(?)) > 0
		ORDER BY i.started_at DESC
		LIMIT ?`, query, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var (
			sensorID   int64
			cause      string
			startedAt  string
			resolvedAt sql.NullString
			ackNote    string
			name       string
			target     string
		)
		if err := rows.Scan(&sensorID, &cause, &startedAt, &resolvedAt, &ackNote, &name, &target); err != nil {
			return nil, false, err
		}
		title := cause
		if title == "" {
			title = name
		}
		hit := SearchHit{
			Kind:  "incident",
			Ref:   itoa(sensorID),
			Title: title,
			Sub:   name + " · " + target,
			At:    formatSearchTime(startedAt),
		}
		if resolvedAt.Valid && resolvedAt.String != "" {
			hit.State = "resolved"
		} else {
			hit.State = "open"
		}
		hits = append(hits, hit)
	}
	hits, cut := clip(hits, limit)
	return hits, cut, rows.Err()
}

// searchResults matches a probe message inside the retention window.
//
// Two bounds rather than one, and both are load-bearing. The window: `results`
// holds one row per probe per sensor and is the only table here that grows with
// time, so without it every keystroke would walk the whole history. The limit:
// messages repeat — a sensor that is down writes the same sentence every minute —
// so an unbounded match on a common word would fill the list with one afternoon of
// one broken sensor.
//
// Only the raw table is searched. The hourly and daily rollups carry counts, not
// messages, so there is nothing there to match on.
func (s *Store) searchResults(ctx context.Context, tx *sql.Tx, query string, limit int) ([]SearchHit, bool, error) {
	raw, _ := s.Retention()
	if raw <= 0 {
		raw = 7 * 24 * time.Hour
	}
	since := time.Now().UTC().Add(-raw)

	rows, err := tx.QueryContext(ctx, `
		SELECT r.sensor_id, r.ts, r.ok, r.message, s.name
		FROM results r JOIN sensors s ON s.id = r.sensor_id
		WHERE r.ts >= ? AND r.message <> '' AND instr(lower(r.message), lower(?)) > 0
		ORDER BY r.ts DESC
		LIMIT ?`, fmtTime(since), query, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var (
			sensorID int64
			ts       string
			ok       int
			message  string
			name     string
		)
		if err := rows.Scan(&sensorID, &ts, &ok, &message, &name); err != nil {
			return nil, false, err
		}
		hit := SearchHit{
			Kind:  "result",
			Ref:   itoa(sensorID),
			Title: message,
			Sub:   name,
			At:    formatSearchTime(ts),
		}
		if ok == 0 {
			hit.State = "down"
		}
		hits = append(hits, hit)
	}
	hits, cut := clip(hits, limit)
	return hits, cut, rows.Err()
}

// searchUsers matches an account on its name and its role, and on nothing else.
//
// `password_hash`, `totp_secret` and the recovery codes' `code_hash` are not
// columns of any query in this file, and no future edit may add them: they are
// credential material, and searching is a read.
func searchUsers(ctx context.Context, tx *sql.Tx, query string, limit int) ([]SearchHit, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT username, role, disabled FROM users
		WHERE instr(lower(username || ' ' || role), lower(?)) > 0
		ORDER BY username COLLATE NOCASE ASC
		LIMIT ?`, query, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var (
			username string
			role     string
			disabled int
		)
		if err := rows.Scan(&username, &role, &disabled); err != nil {
			return nil, false, err
		}
		hit := SearchHit{Kind: "user", Ref: username, Title: username, Sub: role}
		if disabled != 0 {
			hit.State = "disabled"
		}
		hits = append(hits, hit)
	}
	hits, cut := clip(hits, limit)
	return hits, cut, rows.Err()
}

// clip drops the extra row that was fetched to notice there is more, and says
// whether it was there.
func clip(hits []SearchHit, limit int) ([]SearchHit, bool) {
	if len(hits) > limit {
		return hits[:limit], true
	}
	return hits, false
}

// formatSearchTime renders a stored timestamp for somebody to read, and gives up
// quietly on one that cannot be parsed: a result row without a date is still a
// result row, and refusing to show it would lose the message along with the date.
func formatSearchTime(value string) string {
	t := parseTime(value)
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// itoa renders an id for the wire as what it is: a string the browser hands back
// unchanged.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }

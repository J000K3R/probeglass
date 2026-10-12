package web

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The message catalogue is what every sentence in the dashboard comes from, and
// it is the one part of the frontend that is data rather than code: nothing
// compiles it, nothing links against it, and `t()` answers with the key itself
// when a lookup misses. A catalogue that has lost a key therefore ships, runs,
// and shows `settings.retentionDaily` on screen in place of a sentence.
//
// These tests are the only thing standing between a translation and that.

// catalogueEntry matches one `'key': 'text',` line in a catalogue.
//
// Anchored at two spaces, which is the indentation every entry in both files
// uses and which no comment line begins with — comments in these files are prose
// about the catalogue and quote keys while explaining them, and a looser pattern
// would read them as entries.
var catalogueEntry = regexp.MustCompile(`(?m)^  '([^']+)': '(.*)',$`)

// placeholders matches the {name} substitutions t() fills in at call time.
var placeholders = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// shippedCatalogue parses one catalogue file.
func shippedCatalogue(t *testing.T, name string) map[string]string {
	t.Helper()
	src := readAsset(t, Assets(), name)
	out := map[string]string{}
	for _, m := range catalogueEntry.FindAllStringSubmatch(src, -1) {
		if _, dup := out[m[1]]; dup {
			t.Errorf("%s lists the key %q twice; the second one wins and the first "+
				"becomes dead text that no test can see", name, m[1])
		}
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no message at all; the pattern this test reads it with "+
			"has gone out of step with the file's shape", name)
	}
	return out
}

// fileFor says which asset holds a language's dictionary.
//
// English is the exception and lives inside i18n.js itself, because it is the
// reference every other catalogue is measured against and the one the engine
// falls back to — keeping it in the file that does the falling back is what makes
// that fallback impossible to lose.
func fileFor(code string) string {
	if code == "en" {
		return "i18n.js"
	}
	return fmt.Sprintf("i18n.%s.js", code)
}

// shippedLanguages reads `available` back out of the engine, which is the list
// the picker is drawn from.
func shippedLanguages(t *testing.T) []string {
	t.Helper()
	src := readAsset(t, Assets(), "i18n.js")
	m := regexp.MustCompile(`(?m)^export const available = \[(.*)\];$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("i18n.js no longer exports `available`; the language picker is built " +
			"from that list and this test cannot tell what ships without it")
	}
	var codes []string
	for _, raw := range strings.Split(m[1], ",") {
		code := strings.Trim(strings.TrimSpace(raw), `'"`)
		if code != "" {
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		t.Fatal("`available` is empty; a dashboard with no language has no sentences")
	}
	return codes
}

// TestEveryCatalogueCarriesEveryKey is the guard that makes a translation
// trustworthy.
//
// A missing German key is invisible to anybody who reads German: t() falls
// through to English, the sentence is a perfectly good sentence, and the only
// person who would notice is the one who cannot read the interface anyway. So
// the promise is kept mechanically instead — every shipped language has every
// key of the reference, no more and no fewer, and its placeholders are spelled
// the same way.
//
// The order matters as much as the contents: both files list their keys in the
// same sequence, so a translator can read them side by side and see a hole where
// a sentence should be. A key that moved is reported too, because it means
// somebody edited one file and not the other.
func TestEveryCatalogueCarriesEveryKey(t *testing.T) {
	reference := shippedCatalogue(t, "i18n.js")

	// The reference order, as written rather than sorted, is needed below.
	refSrc := readAsset(t, Assets(), "i18n.js")
	var refOrder []string
	for _, m := range catalogueEntry.FindAllStringSubmatch(refSrc, -1) {
		refOrder = append(refOrder, m[1])
	}

	for _, code := range shippedLanguages(t) {
		name := fileFor(code)
		catalogue := reference
		if code != "en" {
			catalogue = shippedCatalogue(t, name)
		}

		var missing, extra []string
		for key := range reference {
			if _, ok := catalogue[key]; !ok {
				missing = append(missing, key)
			}
		}
		for key := range catalogue {
			if _, ok := reference[key]; !ok {
				extra = append(extra, key)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s is missing %d message(s) of the %d English ones, starting "+
				"with %s.\nA missing key is not a blank: t() falls back to English, so the "+
				"reader sees an English sentence in the middle of their own language and "+
				"is the one person who cannot tell that it is a bug.",
				name, len(missing), len(reference), strings.Join(missing[:min(5, len(missing))], ", "))
		}
		if len(extra) > 0 {
			t.Errorf("%s has %d message(s) English does not: %s.\nAn extra key is either "+
				"a typo in the key or a message that was removed from the reference and "+
				"left behind here — neither shows up on screen, which is why it has to "+
				"show up here.", name, len(extra), strings.Join(extra, ", "))
		}

		// A translated sentence may reorder what it interpolates, and German does;
		// what it may not do is drop a placeholder or invent one, because both end
		// as a sentence with a literal "{count}" in it or a value that never
		// appears.
		for key, text := range catalogue {
			if code == "en" {
				continue
			}
			want := placeholders.FindAllStringSubmatch(reference[key], -1)
			got := placeholders.FindAllStringSubmatch(text, -1)
			if !sameMultiset(want, got) {
				t.Errorf("%s: the message %q interpolates %s, English interpolates %s",
					name, key, spell(got), spell(want))
			}
		}

		// And the order, for every language but the reference itself.
		if code == "en" {
			continue
		}
		src := readAsset(t, Assets(), name)
		var got []string
		for _, m := range catalogueEntry.FindAllStringSubmatch(src, -1) {
			got = append(got, m[1])
		}
		if !slices.Equal(got, refOrder) {
			t.Errorf("%s lists its %d keys in a different order than the reference in "+
				"i18n.js.\nThe two files are meant to be read against each other line by "+
				"line, which is how a gap is spotted as a gap; a file that has been "+
				"reordered or appended to cannot be compared that way any more.",
				name, len(got))
		}
	}
}

// min and sameMultiset and spell are small helpers for the messages above.

func sameMultiset(a, b [][]string) bool {
	flat := func(in [][]string) []string {
		out := make([]string, 0, len(in))
		for _, m := range in {
			out = append(out, m[1])
		}
		sort.Strings(out)
		return out
	}
	return slices.Equal(flat(a), flat(b))
}

func spell(in [][]string) string {
	if len(in) == 0 {
		return "nothing"
	}
	seen := map[string]bool{}
	var names []string
	for _, m := range in {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, "{"+m[1]+"}")
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// TestEveryShippedLanguageIsNamed pins the picker's names to the catalogue list.
//
// The picker offers each language under its own name — "Deutsch", not "German" —
// because the reader who needs it is the one who cannot read the language the
// picker is currently written in. That only works while every code in
// `available` has a name in `names`; a code with none falls back to its own
// capitals, which is a two-letter puzzle in a list of words.
func TestEveryShippedLanguageIsNamed(t *testing.T) {
	src := readAsset(t, Assets(), "i18n.js")
	block := regexp.MustCompile(`(?m)^const names = \{(.*)\};$`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("i18n.js no longer declares `names`; the language picker reads its " +
			"labels from there and would fall back to bare two-letter codes")
	}
	named := map[string]string{}
	for _, part := range strings.Split(block[1], ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 {
			continue
		}
		named[strings.Trim(strings.TrimSpace(kv[0]), `'"`)] = strings.Trim(strings.TrimSpace(kv[1]), `'"`)
	}

	for _, code := range shippedLanguages(t) {
		name, ok := named[code]
		if !ok || name == "" {
			t.Errorf("the language %q ships but has no entry in `names`, so the picker "+
				"would offer it as %q", code, strings.ToUpper(code))
		}
	}
	for code := range named {
		if !slices.Contains(shippedLanguages(t), code) {
			t.Errorf("`names` names the language %q, which `available` does not ship", code)
		}
	}
}

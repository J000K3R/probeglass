// The browser test lives in its own module on purpose.
//
// The product's dependency tree is part of what it sells: a handful of
// libraries you can read in an afternoon, and no npm anywhere. A browser
// automation library and its transitive dependencies belong to the test
// harness, not to the thing being shipped, so they are kept where they cannot
// become part of the binary or of anyone's go.sum.
//
// The product module is required for one thing only, and it pulls nothing in:
// model.SensorTypes, the list of types the form has to offer. Written out by
// hand it would be a second copy of a list that already drifts between Go and
// the dashboard, and a type implemented in one and missing from the other is
// exactly what the test using it exists to catch.
//
// Run it with:  cd e2e && go test ./... -v
module github.com/j000k3r/probeglass/e2e

go 1.27.1

replace github.com/j000k3r/probeglass => ../

require (
	github.com/chromedp/cdproto v0.157.9
	github.com/chromedp/chromedp v0.20.1
	github.com/j000k3r/probeglass v0.0.0-00010101000000-000000000000
)

require github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect

package status

import (
	"fmt"
	"html/template"
	"math"
	"strings"
)

// The response-time drawing is deliberately made of a path and a few grid lines,
// with the numbers beside it as ordinary HTML. The SVG is stretched to whatever
// width the page has (preserveAspectRatio="none"), which keeps the curve sharp on
// a phone; text inside such an SVG would be stretched with it, so there is none.
const (
	chartWidth  = 1000
	chartHeight = 100
)

// chartView is the scaled drawing plus the three labels beside it.
type chartView struct {
	Max string
	Mid string
	SVG template.HTML //nolint:gosec // built only from numbers and fixed attributes
}

// buildChart draws the daily average response time over the strip's window.
// It returns nil when the window holds no probe at all: an empty grid is not a
// chart, and the grey strip above already says there is nothing to show.
func buildChart(days []DayPoint, l labels) *chartView {
	peak := 0.0
	has := false
	for _, d := range days {
		if !d.HasLatency {
			continue
		}
		has = true
		if d.LatencyMS > peak {
			peak = d.LatencyMS
		}
	}
	if !has {
		return nil
	}
	max := niceMax(peak)

	var b strings.Builder
	b.WriteString(`<svg class="lat-svg" viewBox="0 0 1000 100" preserveAspectRatio="none" aria-hidden="true">`)

	for _, y := range []int{0, 50, 100} {
		fmt.Fprintf(&b, `<line class="lat-grid" x1="0" y1="%d" x2="1000" y2="%d" vector-effect="non-scaling-stroke"/>`, y, y)
	}
	for i, d := range days {
		if !d.Outage {
			continue
		}
		x := chartX(i, len(days))
		fmt.Fprintf(&b, `<line class="lat-outage" x1="%s" y1="0" x2="%s" y2="100" vector-effect="non-scaling-stroke"/>`, x, x)
	}

	// A gap in the data breaks the curve rather than joining across it: a day the
	// sensor was not probed is not a straight line to the next one.
	var run []string
	flush := func() {
		if len(run) < 2 {
			run = run[:0]
			return
		}
		line := "M" + strings.Join(run, " L")
		area := "M" + firstPointX(run) + ",100 L" + strings.Join(run, " L") +
			" L" + lastPointX(run) + ",100 Z"
		fmt.Fprintf(&b, `<path class="lat-area" d="%s"/>`, area)
		fmt.Fprintf(&b, `<path class="lat-line" d="%s" vector-effect="non-scaling-stroke"/>`, line)
		run = run[:0]
	}
	for i, d := range days {
		if !d.HasLatency {
			flush()
			continue
		}
		run = append(run, chartX(i, len(days))+","+chartY(d.LatencyMS, max))
	}
	flush()
	b.WriteString(`</svg>`)

	return &chartView{
		Max: l.measure(max),
		Mid: l.measure(max / 2),
		SVG: template.HTML(b.String()), //nolint:gosec // numbers and fixed attributes only
	}
}

// chartX is the horizontal position of point i in the 1000-unit viewBox.
func chartX(i, n int) string {
	if n <= 1 {
		return "0"
	}
	return fmt.Sprintf("%.1f", float64(i)*chartWidth/float64(n-1))
}

// chartY is the vertical position, with 0 at the top of the viewBox.
func chartY(v, max float64) string {
	if max <= 0 {
		return "100"
	}
	y := chartHeight - (v/max)*chartHeight
	if y < 0 {
		y = 0
	}
	if y > chartHeight {
		y = chartHeight
	}
	return fmt.Sprintf("%.1f", y)
}

func firstPointX(run []string) string {
	return strings.SplitN(run[0], ",", 2)[0]
}

func lastPointX(run []string) string {
	return strings.SplitN(run[len(run)-1], ",", 2)[0]
}

// niceMax rounds a peak up to 1, 2 or 5 times a power of ten, so the axis has
// label values a person would write down instead of 137 ms.
func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	n := v / p
	m := 10.0
	switch {
	case n <= 1:
		m = 1
	case n <= 2:
		m = 2
	case n <= 5:
		m = 5
	}
	return m * p
}

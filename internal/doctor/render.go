package doctor

import (
	"fmt"
	"io"
)

// Render writes the report.
//
// Plain aligned text: no colour, no box drawing, no text/tabwriter. The three
// places this runs are a distroless container, a docker exec and a CI log, and
// none of them is a terminal where colour helps — while all three are places a
// fixed layout survives intact and can be grepped.
func (r Report) Render(w io.Writer) {
	fmt.Fprintln(w, "weatherglass doctor")
	fmt.Fprintln(w)

	for _, f := range r.Findings {
		fmt.Fprintf(w, "%-4s  %-16s  %s\n", f.Level, f.Label, f.Detail)
		if f.Hint != "" {
			fmt.Fprintf(w, "%-4s  %-16s  hint: %s\n", "", "", f.Hint)
		}
	}

	_, warn, fail := r.Counts()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s, %s\n", plural(warn, "warning"), plural(fail, "failure"))
}

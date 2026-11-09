package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

//go:embed report.html
var htmlTemplate []byte

func comma(n uint64) string {
	s := strconv.FormatUint(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n uint64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return comma(n) + " " + word + "s"
}

// shown returns the blocks to print, in log order, limited to the top-ranked.
func shown(r *Result, top int) []*Block {
	if top <= 0 || top >= len(r.Blocks) {
		return r.Blocks
	}
	var out []*Block
	for _, b := range r.Blocks {
		if b.Rank <= top {
			out = append(out, b)
		}
	}
	return out
}

func headline(r *Result) string {
	odd := r.NewLines + r.ChangedLines + r.Spikes
	if odd == 0 {
		return fmt.Sprintf("Nothing in %s distinguishes it from the baseline.", r.Failing)
	}
	return fmt.Sprintf("%s of %s do not fit the baseline (%s).",
		plural(odd, "line"), comma(r.Lines), plural(uint64(len(r.Blocks)), "block"))
}

func learned(r *Result) string {
	s := fmt.Sprintf("baseline: %s, %s -> %s", plural(uint64(r.Runs), "run"), plural(r.BaselineLines, "line"), plural(uint64(r.Templates), "line shape"))
	if r.Wildcards > 0 {
		s += fmt.Sprintf(", %s learned", plural(uint64(r.Wildcards), "wildcard"))
	}
	return s
}

func annotate(e *Entry) string {
	var notes []string
	for _, c := range e.Changes {
		notes = append(notes, fmt.Sprintf("%s was %s", c.Value, strings.Join(c.Baseline, " | ")))
	}
	if e.Early {
		notes = append(notes, "changed before the first failure")
	}
	if e.Kind == "spike" {
		notes = append(notes, fmt.Sprintf("%dx in this log, at most %dx in a baseline run", e.Total, e.BaselineMax))
	} else {
		if e.Repeat > 0 {
			notes = append(notes, fmt.Sprintf("repeated %dx", e.Repeat+1))
		}
		if e.Total > 0 {
			notes = append(notes, fmt.Sprintf("%dx in this log", e.Total))
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "   <- " + strings.Join(notes, "; ")
}

var markers = map[string]string{"": " ", "new": "+", "changed": "~", "spike": "*"}

// renderText is deliberately ASCII-only: it must survive any console code page.
func renderText(w io.Writer, r *Result, top int, color bool) {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	colors := map[string]string{"": "2", "new": "1;31", "changed": "33", "spike": "35"}

	fmt.Fprintf(w, "%s\n", paint("1", "whyred: "+headline(r)))
	fmt.Fprintf(w, "  %s\n", paint("2", learned(r)))
	blocks := shown(r, top)
	for _, b := range blocks {
		head := fmt.Sprintf("#%d  line %d  score %.1f", b.Rank, b.Line, b.Score)
		if b.Rank == 1 {
			head += "   <- start here"
		}
		fmt.Fprintf(w, "\n%s\n", paint("1;36", head))
		for _, e := range b.Entries {
			line := fmt.Sprintf(" %s %7d | %s", markers[e.Kind], e.Line, e.Text)
			fmt.Fprintf(w, "%s%s\n", paint(colors[e.Kind], line), paint("2", annotate(e)))
		}
	}
	if n := len(r.Blocks) - len(blocks); n > 0 {
		fmt.Fprintf(w, "\n%s\n", paint("2", fmt.Sprintf("%s lower-ranked not shown (--top 0 for all, or --format html)", plural(uint64(n), "block"))))
	}
	if r.MissingTotal > 0 {
		fmt.Fprintf(w, "\n%s\n", paint("1;36", fmt.Sprintf("never happened: %s present in every baseline run", plural(uint64(r.MissingTotal), "line shape"))))
		show := r.Missing[:min(len(r.Missing), 10)]
		for _, t := range show {
			fmt.Fprintf(w, " -         | %s\n", t)
		}
		if n := r.MissingTotal - len(show); n > 0 {
			fmt.Fprintf(w, "%s\n", paint("2", fmt.Sprintf("           | ... and %d more", n)))
		}
	}
	if r.Truncated {
		fmt.Fprintf(w, "\nnote: more than %d anomalous lines; the rest were counted but not kept. Is this the right baseline?\n", maxEntries)
	}
}

// renderMarkdown targets $GITHUB_STEP_SUMMARY and PR comments.
func renderMarkdown(w io.Writer, r *Result, top int) {
	fmt.Fprintf(w, "### whyred: %s\n\n%s\n", headline(r), learned(r))
	blocks := shown(r, top)
	for _, b := range blocks {
		tag := ""
		if b.Rank == 1 {
			tag = " — start here"
		}
		fmt.Fprintf(w, "\n**#%d · line %d · score %.1f**%s\n\n```diff\n", b.Rank, b.Line, b.Score, tag)
		for _, e := range b.Entries {
			m := markers[e.Kind]
			if e.Kind == "changed" || e.Kind == "spike" {
				m = "!"
			}
			fmt.Fprintf(w, "%s %6d | %s%s\n", m, e.Line, strings.ReplaceAll(e.Text, "```", "'''"), annotate(e))
		}
		fmt.Fprint(w, "```\n")
	}
	if n := len(r.Blocks) - len(blocks); n > 0 {
		fmt.Fprintf(w, "\n_%s lower-ranked not shown._\n", plural(uint64(n), "block"))
	}
	if r.MissingTotal > 0 {
		fmt.Fprintf(w, "\n**Never happened** (%s present in every baseline run)\n\n```\n", plural(uint64(r.MissingTotal), "line shape"))
		show := r.Missing[:min(len(r.Missing), 10)]
		for _, t := range show {
			fmt.Fprintf(w, "%s\n", strings.ReplaceAll(t, "```", "'''"))
		}
		if n := r.MissingTotal - len(show); n > 0 {
			fmt.Fprintf(w, "... and %d more\n", n)
		}
		fmt.Fprint(w, "```\n")
	}
}

func renderJSON(w io.Writer, r *Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// renderHTML writes one self-contained file. encoding/json escapes <, > and &,
// so log content cannot break out of the script element; the page itself only
// ever inserts log text through textContent.
func renderHTML(w io.Writer, r *Result) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(bytes.Replace(htmlTemplate, []byte("__WHYRED_DATA__"), data, 1))
	return err
}

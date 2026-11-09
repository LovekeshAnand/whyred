// whyred finds the lines that make a failing log different from passing ones.
package main

import (
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

const version = "0.1.0"

const usage = `whyred - why is this run red?

Shows the lines of a failing log that passing runs of the same job never print.
Timestamps, durations, hashes, ports and reordered parallel output are ignored;
with two or more passing runs, whatever they disagree on is learned as noise.

usage:
  whyred [flags] <passing.log>... <failing.log>
  whyred [flags] --model <file> <failing.log>
  whyred learn -o <model> <passing.log>...

The failing log may be "-" for stdin. Inputs may be gzip or UTF-16.

flags:
  -f, --format text|md|json|html   output format (default text, or from -o extension)
  -o, --output FILE                write the report (or the model) to FILE
  -C, --context N                  context lines around each anomaly (default 2)
  -n, --top N                      show only the N highest-ranked blocks, 0 = all (default 25)
  -m, --model FILE                 use a model saved by "whyred learn" as the baseline
      --mask REGEX                 treat matches as noise (repeatable; give it to learn too)
      --color auto|always|never    colour for text output (default auto, honours NO_COLOR)
      --exit-code                  exit 1 when anomalies are found (default: 0)
      --version

markers:  + line shape never seen in a passing run
          ~ known line, but a value that was constant in passing runs changed
          * known line repeated far more often than in any passing run
`

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "whyred: "+format+"\n", a...)
		return 2
	}
	learn := len(args) > 0 && args[0] == "learn"
	if learn {
		args = args[1:]
	}

	fs := flag.NewFlagSet("whyred", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		format, out, modelPath, colorMode string
		ctx, top                          int
		exitCode, showVersion             bool
		maskSrc                           multiFlag
	)
	for _, n := range []string{"f", "format"} {
		fs.StringVar(&format, n, "", "")
	}
	for _, n := range []string{"o", "output"} {
		fs.StringVar(&out, n, "", "")
	}
	for _, n := range []string{"m", "model"} {
		fs.StringVar(&modelPath, n, "", "")
	}
	for _, n := range []string{"C", "context"} {
		fs.IntVar(&ctx, n, 2, "")
	}
	for _, n := range []string{"n", "top"} {
		fs.IntVar(&top, n, 25, "")
	}
	fs.Var(&maskSrc, "mask", "")
	fs.StringVar(&colorMode, "color", "auto", "")
	fs.BoolVar(&exitCode, "exit-code", false, "")
	fs.BoolVar(&showVersion, "version", false, "")

	if err := fs.Parse(permute(fs, args)); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprint(stdout, usage)
			return 0
		}
		return fail("%v (see whyred --help)", err)
	}
	if showVersion {
		fmt.Fprintln(stdout, "whyred", version)
		return 0
	}
	pos := fs.Args()
	if len(pos) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if ctx < 0 || ctx > 1000 {
		return fail("--context must be between 0 and 1000")
	}
	var masks []*regexp.Regexp
	for _, src := range maskSrc {
		re, err := regexp.Compile(src) // RE2: linear time, no catastrophic backtracking
		if err != nil {
			return fail("--mask: %v", err)
		}
		masks = append(masks, re)
	}

	if learn {
		if out == "" {
			return fail("learn needs -o <model file>")
		}
		m, err := buildModel(pos, masks, maskSrc, stderr)
		if err != nil {
			return fail("%v", err)
		}
		if err := m.save(out); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "learned %s from %s (%s) -> %s\n", plural(uint64(len(m.Templates)), "line shape"),
			plural(uint64(m.Runs), "run"), plural(m.Lines, "line"), out)
		return 0
	}

	// Resolve the baseline model and the failing log.
	var m *Model
	var err error
	failing := pos[len(pos)-1]
	switch {
	case modelPath != "":
		if len(pos) != 1 {
			return fail("with --model, give exactly one failing log")
		}
		if len(masks) > 0 {
			return fail("--mask is stored in the model; pass it to \"whyred learn\" instead")
		}
		if m, err = loadModel(modelPath); err != nil {
			return fail("%v", err)
		}
		for _, src := range m.Masks {
			re, err := regexp.Compile(src)
			if err != nil {
				return fail("%s: bad mask in model: %v", modelPath, err)
			}
			masks = append(masks, re)
		}
	case len(pos) < 2:
		return fail("need at least one passing log and one failing log (see whyred --help)")
	default:
		if m, err = buildModel(pos[:len(pos)-1], masks, maskSrc, stderr); err != nil {
			return fail("%v", err)
		}
	}

	if format == "" {
		format = "text"
		switch strings.ToLower(filepath.Ext(out)) {
		case ".html", ".htm":
			format = "html"
		case ".json":
			format = "json"
		case ".md":
			format = "md"
		}
	}
	switch format {
	case "text", "md", "json", "html":
	default:
		return fail("unknown format %q (text, md, json, html)", format)
	}

	in, closeIn, err := openLog(failing, stdin)
	if err != nil {
		return fail("%v", err)
	}
	res, err := analyze(m, in, masks, ctx)
	closeIn()
	if err != nil {
		return fail("%s: %v", failing, err)
	}
	res.Failing = failing
	if failing == "-" {
		res.Failing = "stdin"
	}

	w := stdout
	var file *os.File
	if out != "" {
		if file, err = os.Create(out); err != nil {
			return fail("%v", err)
		}
		w = file
	}
	bw := bufio.NewWriter(w)
	switch format {
	case "text":
		color := colorMode == "always"
		if f, ok := w.(*os.File); colorMode == "auto" && ok && out == "" && os.Getenv("NO_COLOR") == "" {
			color = isTerminal(f)
		}
		renderText(bw, res, top, color)
	case "md":
		renderMarkdown(bw, res, top)
	case "json":
		err = renderJSON(bw, res)
	case "html":
		err = renderHTML(bw, res)
	}
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	if file != nil {
		if cerr := file.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return fail("writing report: %v", err)
	}
	if out != "" {
		fmt.Fprintf(stderr, "whyred: %s Report: %s\n", headline(res), out)
	}
	if exitCode && len(res.Blocks) > 0 {
		return 1
	}
	return 0
}

// permute moves flags in front of positional arguments so that
// "whyred a.log b.log -o x.html" works with the standard flag package.
func permute(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(append(flags, "--"), pos...)
}

// buildModel learns every baseline in parallel (one goroutine per file,
// bounded by GOMAXPROCS) and merges them in argument order, so the result
// does not depend on scheduling.
func buildModel(patterns []string, masks []*regexp.Regexp, maskSrc []string, stderr io.Writer) (*Model, error) {
	var paths []string
	for _, p := range patterns {
		// PowerShell and cmd do not expand globs for us.
		if _, err := os.Stat(p); err != nil && strings.ContainsAny(p, "*?[") {
			if matches, _ := filepath.Glob(p); len(matches) > 0 {
				paths = append(paths, matches...)
				continue
			}
		}
		paths = append(paths, p)
	}
	if len(paths) > maxBaselines {
		return nil, fmt.Errorf("at most %d baseline logs, got %d", maxBaselines, len(paths))
	}
	runs := make([]*Model, len(paths))
	errs := make([]error, len(paths))
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			in, closeIn, err := openLog(p, nil)
			if err != nil {
				errs[i] = err
				return
			}
			defer closeIn()
			if runs[i], err = learnRun(in, masks); err != nil {
				errs[i] = fmt.Errorf("%s: %w", p, err)
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, err
		}
		if len(runs[i].Templates) == 0 {
			fmt.Fprintf(stderr, "whyred: warning: baseline %s is empty\n", paths[i])
		}
	}
	m := mergeRuns(runs)
	m.Sources = paths
	m.Masks = maskSrc
	return m, nil
}

// openLog opens a log for reading, transparently handling gzip and the
// UTF-16 that Windows PowerShell's ">" redirection produces.
func openLog(path string, stdin io.Reader) (io.Reader, func(), error) {
	var r io.Reader
	closer := func() {}
	if path == "-" {
		if stdin == nil {
			return nil, nil, fmt.Errorf("only the failing log can be read from stdin")
		}
		r = stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		if st, err := f.Stat(); err == nil && st.IsDir() {
			f.Close()
			return nil, nil, fmt.Errorf("%s is a directory", path)
		}
		r, closer = f, func() { f.Close() }
	}
	br := bufio.NewReaderSize(r, maxLine)
	head, _ := br.Peek(3)
	switch {
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			closer()
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		return gz, closer, nil
	case len(head) >= 2 && head[0] == 0xff && head[1] == 0xfe:
		br.Discard(2)
		return &utf16Reader{r: br}, closer, nil
	case len(head) >= 2 && head[0] == 0xfe && head[1] == 0xff:
		br.Discard(2)
		return &utf16Reader{r: br, be: true}, closer, nil
	case len(head) == 3 && head[0] == 0xef && head[1] == 0xbb && head[2] == 0xbf:
		br.Discard(3)
	}
	return br, closer, nil
}

// utf16Reader transcodes a UTF-16 stream to UTF-8.
type utf16Reader struct {
	r    io.Reader
	be   bool
	in   [4096]byte
	k    int  // carried-over odd byte
	hi   rune // pending high surrogate
	out  []byte
	done error
}

func (u *utf16Reader) Read(p []byte) (int, error) {
	for len(u.out) == 0 {
		if u.done != nil {
			return 0, u.done
		}
		n, err := u.r.Read(u.in[u.k:])
		n += u.k
		u.k = 0
		i := 0
		for ; i+1 < n; i += 2 {
			c := rune(u.in[i]) | rune(u.in[i+1])<<8
			if u.be {
				c = rune(u.in[i])<<8 | rune(u.in[i+1])
			}
			switch {
			case u.hi != 0:
				c, u.hi = utf16.DecodeRune(u.hi, c), 0
			case c >= 0xd800 && c < 0xdc00:
				u.hi = c
				continue
			}
			u.out = utf8.AppendRune(u.out, c)
		}
		if i < n {
			u.in[0], u.k = u.in[i], 1
		}
		u.done = err
	}
	n := copy(p, u.out)
	u.out = u.out[n:]
	return n, nil
}

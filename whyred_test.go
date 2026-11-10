package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// ---- helpers ---------------------------------------------------------------

func model(t *testing.T, baselines ...string) *Model {
	t.Helper()
	var runs []*Model
	for _, b := range baselines {
		m, err := learnRun(strings.NewReader(b), nil)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, m)
	}
	return mergeRuns(runs)
}

func diff(t *testing.T, failing string, baselines ...string) *Result {
	t.Helper()
	res, err := analyze(model(t, baselines...), strings.NewReader(failing), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// flagged returns "kind:text" for every anomalous entry, in log order.
func flagged(r *Result) []string {
	out := []string{}
	for _, b := range r.Blocks {
		for _, e := range b.Entries {
			if e.Kind != "" {
				out = append(out, e.Kind+":"+e.Text)
			}
		}
	}
	return out
}

func want(t *testing.T, r *Result, exp ...string) {
	t.Helper()
	got := flagged(r)
	if strings.Join(got, "\n") != strings.Join(exp, "\n") {
		t.Fatalf("flagged lines:\n got: %q\nwant: %q", got, exp)
	}
}

// render returns the human-readable template of b; call it right after scan(b).
func (sc *scanner) render(b []byte) string {
	var out []byte
	for i, tk := range sc.toks {
		if i > 0 {
			out = append(out, ' ')
		}
		t := b[tk.s:tk.e]
		if tk.slot {
			sc.shapeOf(t)
			out = append(out, sc.shape...)
		} else {
			out = append(out, t...)
		}
	}
	return clip(out)
}

func template(line string) string {
	var sc scanner
	b := sc.cleanLine([]byte(line))
	sc.scan(b)
	return sc.render(b)
}

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func cli(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = run(args, strings.NewReader(""), &o, &e)
	return code, o.String(), e.String()
}

// ---- tokeniser -------------------------------------------------------------

func TestTemplateShapes(t *testing.T) {
	for in, exp := range map[string]string{
		"2026-11-10T12:00:01.1234567Z Run tests":       "<*>-<*>-<*>:<*>:<*>.<*> Run tests",
		"ok  \tgithub.com/x/y\t0.123s":                 "ok github.com/x/y #.#s",
		"req id=550e8400-e29b-41d4-a716-446655440000":  "req id <*>-<*>-<*>-<*>-<*>",
		"HEAD is now at 3fa9c21 fix":                   "HEAD is now at <*> fix",
		"src/a.ts(42,13): error TS2345: bad":           "src/a.ts # # error TS# bad",
		"+ lodash@4.17.21":                             "+ lodash@#.#.#",
		"Process completed with exit code 1.":          "Process completed with exit code #",
		"dist/index-BkH3d9Qa.js 143.21 kB":             "dist/index-<*>.js #.# kB",
		"\x1b[32m✓\x1b[0m handles k8s e2e (12 ms)":     "✓ handles k8s e2e # ms",
		"downloading... 45%\rdownloading... 100%":      "downloading #%",
		"Nov 10 12:00:01 host sshd[4242]: ok":          "Nov # #:#:# host sshd # ok",
		"sha256:9f86d081884c7d659a2feaa0c55ad015 done": "<*>:<*> done",
	} {
		if got := template(in); got != exp {
			t.Errorf("template(%q)\n got: %q\nwant: %q", in, got, exp)
		}
	}
}

func TestSameShapeSameHash(t *testing.T) {
	var sc scanner
	h := func(s string) uint64 { v, _ := sc.scan(sc.cleanLine([]byte(s))); return v }
	same := [][2]string{
		{"2026-11-10T12:00:01.1Z PASS a.test.ts (1.2 s)", "2026-11-11T09:13:55.9Z PASS a.test.ts (0.4 s)"},
		{"listening on port 43121", "listening on port 51877"},
		{"\x1b[1mBuild\x1b[0m done", "Build done"},
		{"Tue Nov 10 2026", "Wed Dec 11 2027"},
	}
	for _, p := range same {
		if h(p[0]) != h(p[1]) {
			t.Errorf("expected same template: %q / %q", p[0], p[1])
		}
	}
	differ := [][2]string{
		{"PASS a.test.ts", "FAIL a.test.ts"},
		{"a b c", "a c b"}, // order matters within a line
		{"a b", "a b b"},
	}
	for _, p := range differ {
		if h(p[0]) == h(p[1]) {
			t.Errorf("expected different templates: %q / %q", p[0], p[1])
		}
	}
}

// ---- detection -------------------------------------------------------------

const green = `12:00:01 Installing dependencies
12:00:05 added 120 packages in 4s
12:00:06 + lodash@4.17.20
12:00:07 PASS src/a.test.ts (1.2 s)
12:00:08 PASS src/b.test.ts (0.8 s)
12:00:09 Tests: 0 failed, 14 passed
12:00:10 Uploading coverage
12:00:11 Done
`

func TestIdenticalRunIsClean(t *testing.T) {
	r := diff(t, strings.ReplaceAll(green, "12:00", "17:45"), green)
	want(t, r)
	if r.MissingTotal != 0 {
		t.Fatalf("missing = %d", r.MissingTotal)
	}
}

func TestNewChangedMissing(t *testing.T) {
	red := `09:00:01 Installing dependencies
09:00:05 added 120 packages in 9s
09:00:06 + lodash@4.17.21
09:00:08 PASS src/b.test.ts (0.7 s)
09:00:07 FAIL src/a.test.ts (3.1 s)
09:00:07   TypeError: _.flatMapDeep is not a function
09:00:09 Tests: 1 failed, 13 passed
`
	r := diff(t, red, green, green)
	want(t, r,
		"changed:09:00:06 + lodash@4.17.21",
		"new:09:00:07 FAIL src/a.test.ts (3.1 s)",
		"new:09:00:07   TypeError: _.flatMapDeep is not a function",
		"changed:09:00:09 Tests: 1 failed, 13 passed",
	)
	if got := strings.Join(r.Missing, "|"); got != "12:00:07 PASS src/a.test.ts (1.2 s)|12:00:10 Uploading coverage|12:00:11 Done" {
		t.Fatalf("missing = %q", got)
	}
	ch := r.Blocks[0].Entries[0].Changes
	if len(ch) != 1 || ch[0].Value != "lodash@4.17.21" || ch[0].Baseline[0] != "lodash@4.17.20" {
		t.Fatalf("changes = %+v", ch)
	}
	// The failure outranks the silent version drift, which still gets reported.
	// The first failure is where to start; the silent version drift before it comes next.
	if b := r.Blocks[1]; b.Rank != 1 || b.Line != 5 {
		t.Fatalf("the FAIL block should be rank 1, got rank %d at line %d", b.Rank, b.Line)
	}
	if b := r.Blocks[0]; b.Rank != 2 || !b.Entries[0].Early {
		t.Fatalf("the version drift should be rank 2 and marked early, got %d", b.Rank)
	}
}

func TestVolatilityIsLearnedFromDisagreement(t *testing.T) {
	g1 := "runner 7 ready\nshard 3 of 3\n"
	g2 := "runner 12 ready\nshard 3 of 3\n"
	red := "runner 99 ready\nshard 2 of 3\n"
	// One baseline cannot know the runner number is noise.
	want(t, diff(t, red, g1), "changed:runner 99 ready", "changed:shard 2 of 3")
	// Two baselines disagree on it, so it is; they agree on the shard count, so that stays.
	want(t, diff(t, red, g1, g2), "changed:shard 2 of 3")
}

func TestWildcardsLearnedFromDisagreement(t *testing.T) {
	g1 := "using temp dir /tmp/build-aXkQzP for artifacts\nstatus PASS for suite api\n"
	g2 := "using temp dir /tmp/build-pLmRtW for artifacts\nstatus PASS for suite api\n"
	red := "using temp dir /tmp/build-ZZtopQ for artifacts\nstatus PASS for suite api\n"
	want(t, diff(t, red, g1), "new:using temp dir /tmp/build-ZZtopQ for artifacts")
	want(t, diff(t, red, g1, g2))

	// A wildcard never swallows a token that reads like a failure.
	g1 = "suite api finished PASS today\n"
	g2 = "suite api finished SKIP today\n"
	want(t, diff(t, "suite api finished FAILED today\n", g1, g2), "new:suite api finished FAILED today")
	want(t, diff(t, "suite api finished RETRY today\n", g1, g2))
	// Short lines are never generalised: "Killed" must not match "Done".
	want(t, diff(t, "Killed\n", "Done\nx\n", "Fine\nx\n"), "new:Killed")
}

func TestFreeTextTailsAreLearned(t *testing.T) {
	g1 := "HEAD is now at 3fa9c21 fix typo in readme\nbuild ok\n"
	g2 := "HEAD is now at 77b0e1d speed up the search index a lot\nbuild ok\n"
	red := "HEAD is now at 0c0ffee rename order status enum\nbuild ok\n"
	want(t, diff(t, red, g1), "new:HEAD is now at 0c0ffee rename order status enum")
	r := diff(t, red, g1, g2)
	want(t, r)
	if r.MissingTotal != 0 || r.Wildcards == 0 {
		t.Fatalf("missing=%d wildcards=%d", r.MissingTotal, r.Wildcards)
	}
	// ...but a tail that reads like a failure is still shown.
	want(t, diff(t, "HEAD is now at 0c0ffee fatal: bad object\nbuild ok\n", g1, g2), "new:HEAD is now at 0c0ffee fatal: bad object")
	// A prefix shared by only some runs is not free text.
	g3 := "something else entirely\nbuild ok\n"
	want(t, diff(t, red, g1, g2, g3), "new:HEAD is now at 0c0ffee rename order status enum")
}

func TestGeneratedIdPositionsAcceptAnyId(t *testing.T) {
	// One baseline, id happens to contain digits; the failing run's does not.
	g := "using temp dir /tmp/upload-a8Xk2Q for client\nemitted dist/index-BkH3d9Qa.js ok\n"
	want(t, diff(t, "using temp dir /tmp/upload-QwErTy for client\nemitted dist/index-BkHdxQaz.js ok\n", g))
	want(t, diff(t, "using temp dir /tmp/upload-FAILED for client\n", g), "new:using temp dir /tmp/upload-FAILED for client")
}

func TestSpike(t *testing.T) {
	g := "connect\nretrying download\nok\n"
	red := "connect\n" + strings.Repeat("retrying download\n", 40) + "ok\n"
	r := diff(t, red, g)
	want(t, r, "spike:retrying download")
	e := r.Blocks[0].Entries[0]
	if e.Total != 40 || e.BaselineMax != 1 || r.Spikes != 1 {
		t.Fatalf("spike entry = %+v", e)
	}
}

func TestRepeatsCollapseAndSuppress(t *testing.T) {
	g := strings.Repeat("a\n", 60) + "b\n"
	red := "a\n" + strings.Repeat("boom at 0xdeadbeef1\n", 500) + "b\n" +
		strings.Repeat("a\nscattered failure\n", 50)
	r := diff(t, red, g)
	want(t, r, "new:boom at 0xdeadbeef1", "new:scattered failure", "new:scattered failure", "new:scattered failure")
	if e := r.Blocks[0].Entries[0]; e.Repeat != 499 {
		t.Fatalf("repeat = %d", e.Repeat)
	}
	if e := r.Blocks[1].Entries[0]; e.Total != 50 {
		t.Fatalf("total = %d", e.Total)
	}
	if r.NewLines != 550 {
		t.Fatalf("new lines = %d", r.NewLines)
	}
}

func TestContextAndBlockMerging(t *testing.T) {
	var g, red strings.Builder
	for i := range 30 {
		fmt.Fprintf(&g, "step %c\n", 'A'+i)
		fmt.Fprintf(&red, "step %c\n", 'A'+i)
		if i == 9 || i == 11 || i == 25 {
			fmt.Fprintf(&red, "kaboom %c\n", 'A'+i)
		}
	}
	res, err := analyze(model(t, g.String()), strings.NewReader(red.String()), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (two nearby anomalies merge)", len(res.Blocks))
	}
	var lines []int
	for _, e := range res.Blocks[0].Entries {
		lines = append(lines, e.Line)
	}
	if fmt.Sprint(lines) != "[9 10 11 12 13 14 15 16]" {
		t.Fatalf("block 1 lines = %v", lines)
	}
	seen := map[int]bool{}
	for _, b := range res.Blocks {
		for _, e := range b.Entries {
			if seen[e.Line] {
				t.Fatalf("line %d shown twice", e.Line)
			}
			seen[e.Line] = true
		}
	}
}

func TestOrderOfParallelOutputIsIgnored(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(green), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	want(t, diff(t, strings.Join(lines, "\n"), green))
}

// ---- hostile and awkward input ---------------------------------------------

func TestAwkwardInput(t *testing.T) {
	long := strings.Repeat("x", 3*maxLine)
	cases := map[string]string{
		"empty":            "",
		"no newline":       "a b c",
		"crlf":             "a b c\r\nd e f\r\n",
		"blank lines":      "\n\n\n",
		"long line":        long + "\nafter\n",
		"long no newline":  long,
		"nul bytes":        "a\x00b\x00\n\x00\n",
		"invalid utf8":     "bad \xff\xfe bytes\n",
		"only punctuation": "....\n::::\n====\n",
		"many tokens":      strings.Repeat("t ", 5000) + "\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			r := diff(t, in, "baseline line\n")
			if _, err := json.Marshal(r); err != nil {
				t.Fatal(err)
			}
			want(t, diff(t, in, in)) // any log matches itself
		})
	}
	r := diff(t, long+"\nafter\n", "x\n")
	if r.Lines != 2 || len(r.Blocks[0].Entries[0].Text) > maxText+3 {
		t.Fatalf("long line: lines=%d text=%d", r.Lines, len(r.Blocks[0].Entries[0].Text))
	}
}

func TestControlCharactersNeverReachTheTerminal(t *testing.T) {
	evil := "pwn \x1b]0;title\x07 \x1b[2J \x1bc \x9b31m \x08\x08 \u009b done\n"
	r := diff(t, evil, "hello\n")
	var buf bytes.Buffer
	renderText(&buf, r, 0, false)
	renderMarkdown(&buf, r, 0)
	for _, c := range buf.String() {
		if c < 0x20 && c != '\n' && c != '\t' || c >= 0x7f && c <= 0x9f {
			t.Fatalf("control character %#x in output %q", c, buf.String())
		}
	}
}

func TestHTMLCannotBeBrokenOutOf(t *testing.T) {
	evil := "</script><script>alert(1)</script><img src=x onerror=alert(2)>   __WHYRED_DATA__\n"
	r := diff(t, evil, "hello\n")
	var buf bytes.Buffer
	if err := renderHTML(&buf, r); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if strings.Count(html, "</script>") != 2 || strings.Contains(html, "<img") || strings.Contains(html, "<script>alert") {
		t.Fatal("log content escaped the data island")
	}
	// The embedded JSON must still decode to the original text.
	start := strings.Index(html, `type="application/json">`) + len(`type="application/json">`)
	var back Result
	if err := json.Unmarshal([]byte(html[start:start+strings.Index(html[start:], "</script>")]), &back); err != nil {
		t.Fatal(err)
	}
	if got := back.Blocks[0].Entries[0].Text; got != strings.TrimSpace(evil) {
		t.Fatalf("round trip: %q", got)
	}
}

func TestReportIsBounded(t *testing.T) {
	var red strings.Builder
	for i := range maxEntries + 5000 {
		fmt.Fprintf(&red, "unique%s line\n", strings.Repeat("z", i%97)+strings.Repeat("q", i/97))
	}
	r := diff(t, red.String(), "hello\n")
	n := 0
	for _, b := range r.Blocks {
		n += len(b.Entries)
	}
	if !r.Truncated || n > maxEntries || r.NewLines != uint64(maxEntries+5000) {
		t.Fatalf("truncated=%v kept=%d new=%d", r.Truncated, n, r.NewLines)
	}
}

// ---- files, encodings, model round trip, CLI -------------------------------

func TestGzipAndUTF16Inputs(t *testing.T) {
	plain := write(t, "g.log", green)

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(green))
	zw.Close()
	gzPath := write(t, "g.log.gz", gz.String())

	text := green + "emoji 😀 line\n"
	u := utf16.Encode([]rune(text))
	le, be := []byte{0xff, 0xfe}, []byte{0xfe, 0xff}
	for _, c := range u {
		le = append(le, byte(c), byte(c>>8))
		be = append(be, byte(c>>8), byte(c))
	}
	lePath, bePath := write(t, "le.log", string(le)), write(t, "be.log", string(be))
	bomPath := write(t, "bom.log", "\xef\xbb\xbf"+green)

	for _, p := range []string{gzPath, bomPath} {
		if code, out, errs := cli("-f", "json", plain, p); code != 0 || !strings.Contains(out, `"blocks": []`) {
			t.Fatalf("%s: code=%d out=%s err=%s", p, code, out, errs)
		}
	}
	for _, p := range []string{lePath, bePath} {
		code, out, _ := cli("-f", "json", "-C", "0", plain, p)
		var r Result
		if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 {
			t.Fatal(code, err)
		}
		want(t, &r, "new:emoji 😀 line")
	}
}

func TestModelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	g1 := write(t, "g1.log", green+"runner 7\ntmp /tmp/a-QwErTy done\n")
	g2 := write(t, "g2.log", green+"runner 9\ntmp /tmp/a-AsDfGh done\n")
	red := write(t, "red.log", green+"runner 3\ntmp /tmp/a-ZxCvBn done\nsecret=hunter2 leaked\nBOOM\n")
	mp := filepath.Join(dir, "m.whyred")

	if code, _, errs := cli("learn", "--mask", `secret=\S+`, "-o", mp, g1, g2); code != 0 {
		t.Fatal(errs)
	}
	_, direct, _ := cli("-f", "json", "--mask", `secret=\S+`, g1, g2, red)
	_, viaModel, _ := cli("-f", "json", "--model", mp, red)
	if direct != viaModel {
		t.Fatalf("model round trip changed the result:\n%s\n---\n%s", direct, viaModel)
	}
	if strings.Contains(direct, "hunter2") {
		t.Fatal("masked value leaked into the template or vocabulary path")
	}
	var r Result
	json.Unmarshal([]byte(direct), &r)
	if len(flagged(&r)) != 2 { // the masked line (new shape) and BOOM
		t.Fatalf("flagged = %q", flagged(&r))
	}
	// Two runs of the same command are byte-identical.
	if _, again, _ := cli("-f", "json", "--mask", `secret=\S+`, g1, g2, red); again != direct {
		t.Fatal("output is not deterministic")
	}
}

func TestCLI(t *testing.T) {
	g := write(t, "green.log", green)
	red := write(t, "red.log", green+"panic: nil map\n")
	dir := t.TempDir()

	if code, out, _ := cli(g, red); code != 0 || !strings.Contains(out, "panic: nil map") || !strings.Contains(out, "start here") {
		t.Fatalf("text: %d %s", code, out)
	}
	if code, _, _ := cli("--exit-code", g, red); code != 1 {
		t.Fatalf("--exit-code with findings = %d", code)
	}
	if code, _, _ := cli("--exit-code", g, write(t, "same.log", green)); code != 0 {
		t.Fatalf("--exit-code without findings = %d", code)
	}
	// Flags after positionals, format inferred from the extension.
	html := filepath.Join(dir, "r.html")
	if code, _, errs := cli(g, red, "-o", html); code != 0 {
		t.Fatal(errs)
	}
	if b, _ := os.ReadFile(html); !bytes.Contains(b, []byte("<!doctype html>")) || !bytes.Contains(b, []byte("panic: nil map")) {
		t.Fatal("html report not written")
	}
	if code, out, _ := cli("-f", "md", g, red); code != 0 || !strings.Contains(out, "```diff") {
		t.Fatalf("md: %s", out)
	}
	if code, out, _ := cli("--color", "always", g, red); code != 0 || !strings.Contains(out, "\x1b[1;31m") {
		t.Fatal("no colour with --color always")
	}
	// Globs are expanded for shells that do not.
	if code, out, _ := cli(filepath.Join(filepath.Dir(g), "*.log"), red); code != 0 || !strings.Contains(out, "panic") {
		t.Fatalf("glob: %d %s", code, out)
	}
	// stdin
	var o, e bytes.Buffer
	if code := run([]string{g, "-"}, strings.NewReader("panic: from stdin\n"), &o, &e); code != 0 || !strings.Contains(o.String(), "from stdin") {
		t.Fatalf("stdin: %d %s %s", code, o.String(), e.String())
	}

	notModel := write(t, "junk", "not gzip")
	for name, args := range map[string][]string{
		"no args":         {},
		"one file":        {g},
		"missing file":    {g, filepath.Join(dir, "nope.log")},
		"missing base":    {filepath.Join(dir, "nope.log"), red},
		"directory":       {dir, red},
		"bad regex":       {"--mask", "(", g, red},
		"bad format":      {"-f", "pdf", g, red},
		"bad flag":        {"--frobnicate", g, red},
		"bad context":     {"-C", "-1", g, red},
		"bad model":       {"--model", notModel, red},
		"model plus base": {"--model", notModel, g, red},
		"learn no output": {"learn", g},
		"stdin baseline":  {"-", red},
	} {
		if code, _, errs := cli(args...); code != 2 || errs == "" {
			t.Errorf("%s: code=%d stderr=%q", name, code, errs)
		}
	}
	if code, out, _ := cli("--help"); code != 0 || !strings.Contains(out, "usage:") {
		t.Fatal("--help")
	}
	if code, out, _ := cli("--version"); code != 0 || !strings.Contains(out, version) {
		t.Fatal("--version")
	}
}

func TestCorruptModelsAreRejected(t *testing.T) {
	red := write(t, "red.log", "x\n")
	gzJSON := func(s string) string {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		w.Write([]byte(s))
		w.Close()
		return write(t, "m", b.String())
	}
	for _, body := range []string{``, `[]`, `{"version":99,"runs":1,"templates":{}}`, `{"version":1}`, `{"version":1,"runs":1,"templates":{"1":null,"x":{}}}`} {
		if code, _, errs := cli("--model", gzJSON(body), red); code != 2 {
			t.Errorf("model %q: code=%d stderr=%q", body, code, errs)
		}
	}
	// A model with a null template and inconsistent slots must not panic.
	ok := gzJSON(`{"version":1,"runs":1,"templates":{"1":null},"vocab":null,"wild":null}`)
	if code, _, errs := cli("--model", ok, red); code != 0 {
		t.Errorf("sparse model: code=%d stderr=%q", code, errs)
	}
}

func TestManyBaselinesInParallel(t *testing.T) {
	var paths []string
	for i := range 40 {
		paths = append(paths, write(t, "g.log", green+fmt.Sprintf("runner %d up\n", i)))
	}
	red := write(t, "red.log", green+"runner 4000 up\nsegfault\n")
	code, out, errs := cli(append(append([]string{"-f", "json", "-C", "0"}, paths...), red)...)
	var r Result
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 {
		t.Fatal(code, errs, err)
	}
	want(t, &r, "new:segfault")
	if code, _, _ := cli(append(append(paths, paths...), red)...); code != 2 {
		t.Fatal("more than 64 baselines should be rejected")
	}
}

// ---- fuzzing and benchmarks ------------------------------------------------

func FuzzAnalyze(f *testing.F) {
	f.Add("a b c\n", "a b d\n")
	f.Add(green, "\x1b[31mFAIL\x1b[0m 12:00:01 x=1\r\n")
	f.Add("id 550e8400-e29b-41d4-a716-446655440000\n", "\xff\xfe\x00 1.2.3 4% 5ms\n")
	f.Fuzz(func(t *testing.T, base, failing string) {
		m1, _ := learnRun(strings.NewReader(base), nil)
		m2, _ := learnRun(strings.NewReader(failing), nil)
		for _, m := range []*Model{mergeRuns([]*Model{m1}), mergeRuns([]*Model{m1, m2})} {
			r, err := analyze(m, strings.NewReader(failing), nil, 1)
			if err != nil {
				t.Fatal(err)
			}
			if m.Runs == 2 && len(r.Blocks) != 0 {
				t.Fatalf("a log is anomalous against a baseline that contains it: %q", flagged(r))
			}
			last := 0
			for _, b := range r.Blocks {
				for _, e := range b.Entries {
					if e.Line <= last {
						t.Fatalf("lines out of order: %d after %d", e.Line, last)
					}
					last = e.Line + e.Repeat
				}
			}
			var buf bytes.Buffer
			renderText(&buf, r, 0, true)
			renderMarkdown(&buf, r, 0)
			if err := renderHTML(&buf, r); err != nil {
				t.Fatal(err)
			}
		}
	})
}

var benchLines = strings.Split(strings.TrimSpace(`
2026-11-09T12:00:01.1234567Z PASS src/checkout/cart.test.ts (1.234 s)
2026-11-09T12:00:01.1934567Z   ✓ rejects expired token (12 ms)
2026-11-09T12:00:02.0034567Z [server] listening on port 43121
2026-11-09T12:00:02.4534567Z request id=550e8400-e29b-41d4-a716-446655440000 took 12ms status=200
2026-11-09T12:00:03.0000001Z #8 sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 12.3MB / 45.1MB 0.4s
2026-11-09T12:00:03.5000001Z npm warn deprecated inflight@1.0.6: This module is not supported, and leaks memory.
`), "\n")

func benchLog(mb int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < mb<<20; i++ {
		b.WriteString(benchLines[i%len(benchLines)])
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func BenchmarkLearn(b *testing.B) {
	data := benchLog(32)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		learnRun(bytes.NewReader(data), nil)
	}
}

func BenchmarkAnalyze(b *testing.B) {
	data := benchLog(32)
	m, _ := learnRun(bytes.NewReader(data), nil)
	m = mergeRuns([]*Model{m})
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		analyze(m, bytes.NewReader(data), nil, 2)
	}
}

// ---- regressions from the independent review -------------------------------

func TestReviewTimestampLevelPrefixIsNotFreeText(t *testing.T) {
	common := "2026-11-09 10:00:01 INFO [main] Starting service\n2026-11-09 10:00:03 INFO [main] Ready\n"
	p1 := common + "2026-11-09 10:00:02 INFO [main] Connected to replica alpha\n"
	p2 := common + "2026-11-09 11:00:02 INFO [main] Using fallback mirror bravo\n"
	red := common + "2026-11-09 12:00:02 INFO [main] Skipping migrations because schema lock is held by another process\n" +
		"2026-11-09 12:00:02 INFO [main] Dropping table users\n"
	if got := flagged(diff(t, red, p1, p2)); len(got) != 2 {
		t.Fatalf("a date, a time, a level and a component must not free the rest of the line: %q", got)
	}
}

func TestReviewWildcardLearningIsDeterministic(t *testing.T) {
	// 17 templates sharing a prefix, spread over 6 runs so that the capped
	// mask list would depend on iteration order if it were not sorted.
	runs := make([]strings.Builder, 6)
	for s := 0; s < 15; s++ {
		for r := range 6 {
			if r < 2 || s>>(r-2)&1 == 1 {
				fmt.Fprintf(&runs[r], "alpha beta gamma delta tail%c\n", 'a'+s)
			}
		}
	}
	runs[0].WriteString("alpha beta gamma delta onlyone\n")
	runs[1].WriteString("alpha beta gamma delta onlytwo\n")
	var logs []string
	for i := range runs {
		logs = append(logs, runs[i].String())
	}
	first := fmt.Sprint(flagged(diff(t, "alpha beta gamma delta surprise\n", logs...)))
	for range 40 {
		if got := fmt.Sprint(flagged(diff(t, "alpha beta gamma delta surprise\n", logs...))); got != first {
			t.Fatalf("non-deterministic: %s vs %s", got, first)
		}
	}
}

func TestReviewSilentMisses(t *testing.T) {
	for name, c := range map[string][2]string{
		"hex status code":   {"Process exited with code 0x00000000\n", "Process exited with code 0xC0000005\n"},
		"operator":          {"check: actual == expected\n", "check: actual != expected\n"},
		"id became a word":  {"Deployed revision 3fa9c21b7 to staging\n", "Deployed revision NONE to staging\n"},
		"past token cap":    {strings.Repeat("w ", 600) + "fine\n", strings.Repeat("w ", 600) + "broken\n"},
		"hidden by CR":      {"ready ok\n", "ERROR: disk full, build failed\rready ok\n"},
		"unterminated OSC":  {"fatal: all good here\n", "fatal: \x1b]0;title crash and burn\n"},
		"same-length is ok": {"Deployed revision 3fa9c21b7 to staging\n", "Deployed revision abcdefabc to staging\n"},
	} {
		got := flagged(diff(t, c[1], c[0]))
		if name == "same-length is ok" {
			if len(got) != 0 {
				t.Errorf("%s: %q", name, got)
			}
		} else if len(got) != 1 {
			t.Errorf("%s: want one flagged line, got %q", name, got)
		}
	}
	// Progress bars are still collapsed to their final state.
	want(t, diff(t, "fetch 10%\rfetch 55%\rfetch 100%\ndone\n", "fetch 100%\ndone\n"))
}

func TestReviewDayOfMonthIsNotAValue(t *testing.T) {
	g := "Nov  9 10:00:01 host app: started\nNov  9 10:00:02 host app: ready\n"
	want(t, diff(t, strings.ReplaceAll(g, "Nov  9", "Nov 10"), g, g))
	want(t, diff(t, strings.ReplaceAll(g, "Nov  9", "Dec  1"), g))
}

func TestReviewLongLinesAreNotACPUCliff(t *testing.T) {
	head := strings.Repeat("w ", 255)
	red := strings.Repeat(head+strings.Repeat("z", 60000)+" error\n", 40)
	start := time.Now()
	diff(t, red, head+"xa\n", head+"xb\n")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestReviewHostileModel(t *testing.T) {
	red := write(t, "red.log", "port 9\nx\n")
	gz := func(s string) string {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		w.Write([]byte(s))
		w.Close()
		return write(t, "m", b.String())
	}
	// Escape sequences in a model must not reach the terminal, and a tracked
	// slot without values must not break the report.
	var sc scanner
	h, _ := sc.scan([]byte("port 9"))
	evil := fmt.Sprintf(`{"version":1,"runs":1,"templates":{"7":{"text":"gone \u001b[2J\u001b]0;pwned\u0007","runs":1,"max":1},"%d":{"text":"port 1","runs":1,"max":1,"slots":[{}]}}}`, h)
	for _, f := range []string{"text", "md", "html", "json"} {
		code, out, errs := cli("-f", f, "--model", gz(evil), red)
		if code != 0 || strings.ContainsAny(out, "\x1b\x07") {
			t.Fatalf("%s: code=%d err=%q raw control bytes=%v", f, code, errs, strings.ContainsAny(out, "\x1b\x07"))
		}
	}
	// A model that decompresses to more than the cap is rejected, not loaded.
	bomb := gz(`{"version":1,"runs":1,"templates":{"7":{"text":"` + strings.Repeat("A", maxModel+1) + `"}}}`)
	if code, _, errs := cli("--model", bomb, red); code != 2 {
		t.Fatalf("bomb: code=%d err=%q", code, errs)
	}
}

func TestReviewBaselineGlobSkipsTheFailingLog(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "passing.log"), []byte(green), 0o644)
	red := filepath.Join(dir, "failing.log")
	os.WriteFile(red, []byte(green+"panic: boom\n"), 0o644)
	code, out, errs := cli(filepath.Join(dir, "*.log"), red)
	if code != 0 || !strings.Contains(out, "panic: boom") || !strings.Contains(errs, "ignoring") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	if code, _, _ := cli(red, red); code != 2 {
		t.Fatal("a log compared with itself has no baseline")
	}
}

func TestReviewUTF16LoneSurrogate(t *testing.T) {
	units := append(utf16.Encode([]rune("a b")), 0xd83d)
	units = append(units, utf16.Encode([]rune("\nFAIL x\n"))...)
	raw := []byte{0xff, 0xfe}
	for _, c := range units {
		raw = append(raw, byte(c), byte(c>>8))
	}
	_, out, _ := cli("-f", "json", "-C", "0", write(t, "g.log", "hello\n"), write(t, "u.log", string(raw)))
	var r Result
	json.Unmarshal([]byte(out), &r)
	want(t, &r, "new:a b�", "new:FAIL x")
}

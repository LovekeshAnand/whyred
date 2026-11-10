// Command bench generates synthetic CI logs with labelled faults and measures
// how well whyred and the usual alternatives surface them.
//
//	go run ./bench gen  -dir examples/ci -seed 7 -fault dep_drift
//	go run ./bench eval -bin ./whyred.exe -n 120
//	go run ./bench big  -dir /tmp/big -mb 200
//
// The generator is a model of a GitHub Actions job for a Node project
// (checkout, npm install, vite build, parallel jest, docker build). It is NOT
// real CI output; see docs/BENCHMARKS.md for what that does and does not show.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var faults = []string{"test_fail", "compile_error", "dep_drift", "oom", "network", "hang"}

// ---- generator -------------------------------------------------------------

type project struct {
	files []string            // test files
	tests map[string][]string // test names per file
	deps  []string            // name@version
	tmp   map[string]bool     // test files that log a temp dir
}

var (
	areas   = []string{"auth", "billing", "cart", "checkout", "search", "profile", "inventory", "orders", "payments", "notify", "session", "catalog", "shipping", "reports", "admin", "webhooks", "ratelimit", "cache", "queue", "uploads", "audit", "export", "flags", "geo"}
	units   = []string{"service", "controller", "repository", "validator", "mapper", "client", "policy", "reducer"}
	verbs   = []string{"returns", "rejects", "handles", "renders", "parses", "validates", "retries", "caches", "emits", "rounds", "normalises", "ignores"}
	objects = []string{"empty input", "expired tokens", "error responses", "unicode names", "duplicate ids", "large payloads", "missing fields", "concurrent writes", "a stale cache", "invalid json", "timeouts gracefully", "partial failures", "negative totals", "trailing slashes"}
	pkgs    = []string{"lodash", "axios", "express", "zod", "dayjs", "uuid", "chalk", "debug", "ws", "pino", "yargs", "glob", "semver", "minimist", "qs", "ms", "nanoid", "undici", "fastify", "knex", "pg", "redis", "bullmq", "sharp", "jose", "bcrypt", "helmet", "cors", "dotenv", "commander", "ora", "execa", "tslib", "rxjs", "immer", "date-fns", "ramda", "cheerio", "marked", "mime"}
	commits = []string{"fix typo in readme", "bump deps", "refactor cart totals", "add retry to webhook sender", "speed up search index", "remove dead code", "tune cache ttl", "handle empty profile", "update lockfile", "rename order status enum", "tidy imports", "document rate limits"}
)

func newProject(rng *rand.Rand, scale int) *project {
	p := &project{tests: map[string][]string{}, tmp: map[string]bool{}}
	for i := 0; i < 20*scale; i++ {
		f := fmt.Sprintf("src/%s/%s.test.ts", areas[rng.IntN(len(areas))], units[rng.IntN(len(units))])
		if scale > 1 {
			f = fmt.Sprintf("src/%s/m%d/%s.test.ts", areas[rng.IntN(len(areas))], i, units[rng.IntN(len(units))])
		}
		if p.tests[f] != nil {
			continue
		}
		p.files = append(p.files, f)
		for range 5 + rng.IntN(9) {
			p.tests[f] = append(p.tests[f], verbs[rng.IntN(len(verbs))]+" "+objects[rng.IntN(len(objects))])
		}
		if rng.IntN(6) == 0 {
			p.tmp[f] = true
		}
	}
	for _, name := range pkgs {
		p.deps = append(p.deps, fmt.Sprintf("%s@%d.%d.%d", name, rng.IntN(9), rng.IntN(20), rng.IntN(30)))
	}
	return p
}

type runLog struct {
	lines []string
	fault []bool // fault[i]: line i exists because of the injected fault
	root  int    // index of the root-cause line, -1 for a passing run
}

type gen struct {
	rng  *rand.Rand
	p    *project
	now  time.Time
	out  runLog
	mark bool
}

func (g *gen) l(format string, a ...any) {
	g.now = g.now.Add(time.Duration(1+g.rng.IntN(400)) * time.Millisecond)
	g.out.lines = append(g.out.lines, g.now.UTC().Format("2006-01-02T15:04:05.0000000Z")+" "+fmt.Sprintf(format, a...))
	g.out.fault = append(g.out.fault, g.mark)
}

// f emits a line caused by the fault; the first one is the root unless set.
func (g *gen) f(format string, a ...any) {
	g.mark = true
	if g.out.root < 0 {
		g.out.root = len(g.out.lines)
	}
	g.l(format, a...)
	g.mark = false
}

func (g *gen) hex(n int) string {
	const d = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = d[g.rng.IntN(16)]
	}
	return string(b)
}

func (g *gen) b62(n int) string {
	const d = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	for i := range b {
		b[i] = d[g.rng.IntN(62)]
	}
	return string(b)
}

func (g *gen) uuid() string {
	return fmt.Sprintf("%s-%s-4%s-a%s-%s", g.hex(8), g.hex(4), g.hex(3), g.hex(3), g.hex(12))
}

func (g *gen) exit(code int) {
	g.f("##[error]Process completed with exit code %d.", code)
}

// generate produces one run of the project's CI job. fault "" is a passing run.
func generate(p *project, rng *rand.Rand, fault string) runLog {
	g := &gen{rng: rng, p: p, now: time.Date(2026, 11, 1+rng.IntN(9), rng.IntN(24), rng.IntN(60), 0, 0, time.UTC)}
	g.out.root = -1
	ok := g.setup(fault) && g.build(fault) && g.test(fault) && g.docker()
	if ok {
		g.l("Uploading coverage report to artifacts")
		g.l("Artifact coverage-%s has been successfully uploaded! Final size is %d bytes.", g.hex(7), 80000+g.rng.IntN(9000))
	}
	g.l("Post job cleanup.")
	g.l("[command]/usr/bin/git version")
	g.l("git version 2.47.0")
	g.l("Cleaning up orphan processes")
	return g.out
}

func (g *gen) setup(fault string) bool {
	rng, p := g.rng, g.p
	g.l("Current runner version: '2.319.1'")
	g.l("Runner name: 'GitHub Actions %d'", 1+rng.IntN(40))
	g.l("##[group]Operating System")
	g.l("Ubuntu 24.04.1 LTS")
	g.l("##[endgroup]")
	g.l("Download action repository 'actions/checkout@v4' (SHA:%s)", "11bd71901bbe5b1630ceea73d27597364c9af683")
	g.l("##[group]Run actions/checkout@v4")
	g.l("Syncing repository: acme/storefront")
	sha := g.hex(40)
	g.l("[command]/usr/bin/git -c protocol.version=2 fetch --no-tags --prune --depth=1 origin +%s:refs/remotes/origin/main", sha)
	g.l("remote: Total %d (delta %d), reused %d (delta %d), pack-reused 0", 900+rng.IntN(80), rng.IntN(30), 400+rng.IntN(80), rng.IntN(9))
	g.l("HEAD is now at %s %s", sha[:7], commits[rng.IntN(len(commits))])
	g.l("##[endgroup]")
	g.l("##[group]Run actions/setup-node@v4")
	g.l("Found in cache @ /opt/hostedtoolcache/node/20.11.0/x64")
	key := "node-cache-Linux-x64-npm-" + g.hex(64)
	if rng.IntN(100) < 85 {
		g.l("Cache restored from key: %s", key)
	} else {
		g.l("Cache not found for input keys: %s", key)
	}
	g.l("##[endgroup]")
	g.l("##[group]Run npm ci")
	g.l("(node:%d) [DEP0040] DeprecationWarning: The `punycode` module is deprecated. Please use a userland alternative instead.", 1500+rng.IntN(400))
	g.l("npm warn deprecated inflight@1.0.6: This module is not supported, and leaks memory. Do not use it.")
	g.l("npm warn deprecated rimraf@3.0.2: Rimraf versions prior to v4 are no longer supported")
	g.l("npm warn deprecated glob@7.2.3: Glob versions prior to v9 are no longer supported")

	retries := 0
	if rng.IntN(100) < 15 {
		retries = 1 // passing runs hiccup too
	}
	if fault == "network" {
		retries = 8 + rng.IntN(8)
	}
	for range retries {
		g.mark = fault == "network"
		if g.mark && g.out.root < 0 {
			g.out.root = len(g.out.lines)
		}
		g.l("npm warn retry will retry, error on last attempt: Error: read ECONNRESET")
		g.mark = false
	}
	if fault == "network" {
		g.f("npm error code ECONNRESET")
		g.f("npm error syscall read")
		g.f("npm error errno ECONNRESET")
		g.f("npm error network Invalid response body while trying to fetch https://registry.npmjs.org/%s: read ECONNRESET", pkgs[rng.IntN(len(pkgs))])
		g.f("npm error network This is a problem related to network connectivity.")
		g.f("npm error A complete log of this run can be found in: /home/runner/.npm/_logs/%s-debug-0.log", g.now.Format("2006-01-02T15_04_05_000Z"))
		g.exit(1)
		return false
	}
	drift := -1
	if fault == "dep_drift" {
		drift = rng.IntN(len(p.deps))
	}
	for i, d := range p.deps {
		if i == drift { // a transitive dependency floated to a new patch release
			at := strings.LastIndexByte(d, '.')
			patch, _ := strconv.Atoi(d[at+1:])
			g.f("+ %s.%d", d[:at], patch+1)
			continue
		}
		g.l("+ %s", d)
	}
	g.l("added %d packages, and audited %d packages in %ds", 1423, 1424, 9+rng.IntN(14))
	g.l("%d packages are looking for funding", 187)
	g.l("found 0 vulnerabilities")
	g.l("##[endgroup]")
	return true
}

func (g *gen) build(fault string) bool {
	rng := g.rng
	g.l("##[group]Run npm run build")
	g.l("> storefront@2.8.0 build")
	g.l("> tsc --noEmit && vite build")
	if fault == "compile_error" {
		n := 1 + rng.IntN(4)
		files := map[string]bool{}
		for range n {
			f := strings.Replace(g.p.files[rng.IntN(len(g.p.files))], ".test.ts", ".ts", 1)
			files[f] = true
			g.f("%s(%d,%d): error TS2345: Argument of type 'string | undefined' is not assignable to parameter of type 'string'.", f, 10+rng.IntN(200), 1+rng.IntN(60))
			g.f("  Type 'undefined' is not assignable to type 'string'.")
		}
		g.f("Found %d errors in %d files.", n, len(files))
		g.exit(2)
		return false
	}
	g.l("vite v5.4.10 building for production...")
	g.l("transforming...")
	if fault == "oom" {
		pid := 2000 + rng.IntN(500)
		g.f("<--- Last few GCs --->")
		for range 3 {
			g.f("[%d:0x%s]    %d ms: Mark-Compact %d.%d (%d.%d) -> %d.%d (%d.%d) MB, %d.%d / 0.00 ms  (average mu = 0.%d, current mu = 0.0%d) allocation failure; scavenge might not succeed",
				pid, g.hex(7), 40000+rng.IntN(9000), 2040+rng.IntN(8), rng.IntN(10), 2084, rng.IntN(10), 2040+rng.IntN(8), rng.IntN(10), 2085, rng.IntN(10), 1500+rng.IntN(300), rng.IntN(100), 100+rng.IntN(200), rng.IntN(9))
		}
		g.f("<--- JS stacktrace --->")
		g.f("FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory")
		g.f("----- Native stack trace -----")
		for i, fn := range []string{"node::OOMErrorHandler(char const*, v8::OOMDetails const&) [node]", "v8::Utils::ReportOOMFailure(v8::internal::Isolate*, char const*, v8::OOMDetails const&) [node]", "v8::internal::V8::FatalProcessOutOfMemory(v8::internal::Isolate*, char const*, v8::OOMDetails const&) [node]", "v8::internal::Heap::CollectGarbage(v8::internal::AllocationSpace, v8::internal::GarbageCollectionReason, v8::GCCallbackFlags) [node]"} {
			g.f(" %d: 0x%s %s", i+1, g.hex(6), fn)
		}
		g.f("Aborted (core dumped)")
		g.exit(134)
		return false
	}
	g.l("✓ %d modules transformed.", 1342)
	g.l("rendering chunks...")
	g.l("computing gzip size...")
	g.l("dist/index.html                   %d.%02d kB │ gzip:  0.%02d kB", 0, 46+rng.IntN(3), 30+rng.IntN(3))
	for _, n := range []string{"index", "vendor", "checkout", "admin"} {
		g.l("dist/assets/%s-%s.js   %d.%02d kB │ gzip: %d.%02d kB", n, g.b62(8), 100+rng.IntN(300), rng.IntN(100), 30+rng.IntN(60), rng.IntN(100))
	}
	g.l("✓ built in %d.%02ds", 3+rng.IntN(4), rng.IntN(100))
	g.l("##[endgroup]")
	return true
}

func (g *gen) test(fault string) bool {
	rng, p := g.rng, g.p
	g.l("##[group]Run npm test -- --ci")
	g.l("> storefront@2.8.0 test")
	g.l("> jest --ci")
	order := rng.Perm(len(p.files)) // jest workers finish in a different order every run
	bad := map[int]bool{}
	switch fault {
	case "test_fail", "hang":
		bad[rng.IntN(len(p.files))] = true
	case "dep_drift":
		bad[rng.IntN(len(p.files))] = true
		bad[rng.IntN(len(p.files))] = true
	}
	failedTests, total := 0, 0
	for _, fi := range order {
		f := p.files[fi]
		tests := p.tests[f]
		total += len(tests)
		if rng.IntN(4) == 0 {
			g.l("[server] listening on port %d", 30000+rng.IntN(30000))
		}
		if p.tmp[f] {
			g.l("[upload] using temp dir /tmp/upload-%s for %s", g.b62(6), filepath.Base(f))
		}
		if rng.IntN(3) == 0 {
			g.l("[http] request id=%s took %dms status=200", g.uuid(), 1+rng.IntN(90))
		}
		if rng.IntN(9) == 0 { // tests that exercise error paths log errors when they pass
			g.l("[http] Error: upstream timeout (expected by test) id=%s", g.uuid())
		}
		if bad[fi] && fault == "hang" {
			continue // the file never reports
		}
		if !bad[fi] {
			g.l("PASS %s (%d.%03d s)", f, rng.IntN(4), rng.IntN(1000))
			for _, t := range tests {
				g.l("  ✓ %s (%d ms)", t, 1+rng.IntN(80))
			}
			continue
		}
		victim := rng.IntN(len(tests))
		failedTests++
		g.f("FAIL %s (%d.%03d s)", f, rng.IntN(4), rng.IntN(1000))
		for i, t := range tests {
			if i != victim {
				g.l("  ✓ %s (%d ms)", t, 1+rng.IntN(80))
				continue
			}
			g.f("  ✕ %s (%d ms)", t, 1+rng.IntN(80))
		}
		g.f("  ● %s › %s", strings.TrimSuffix(filepath.Base(f), ".test.ts"), tests[victim])
		src := strings.Replace(f, ".test.ts", ".ts", 1)
		if fault == "dep_drift" {
			g.f("    TypeError: (0 , _util.normalise) is not a function")
			g.f("      at Object.normalise (%s:%d:%d)", src, 10+rng.IntN(200), 1+rng.IntN(40))
		} else {
			g.f("    expect(received).toBe(expected) // Object.is equality")
			g.f("    Expected: %d", 200)
			g.f("    Received: %d", 500)
		}
		g.f("      at Object.<anonymous> (%s:%d:%d)", f, 10+rng.IntN(200), 1+rng.IntN(40))
	}
	if fault == "hang" {
		g.f("Jest did not exit one second after the test run has completed.")
		g.f("This usually means that there are asynchronous operations that weren't stopped in your tests. Consider running Jest with `--detectOpenHandles` to troubleshoot this issue.")
		g.f("##[error]The job running on runner GitHub Actions %d has exceeded the maximum execution time of 30 minutes.", 1+rng.IntN(40))
		return false
	}
	if failedTests > 0 {
		g.f("Test Suites: %d failed, %d passed, %d total", failedTests, len(p.files)-failedTests, len(p.files))
		g.f("Tests:       %d failed, %d passed, %d total", failedTests, total-failedTests, total)
	} else {
		g.l("Test Suites: %d passed, %d total", len(p.files), len(p.files))
		g.l("Tests:       %d passed, %d total", total, total)
	}
	g.l("Snapshots:   0 total")
	g.l("Time:        %d.%03d s", 20+rng.IntN(30), rng.IntN(1000))
	g.l("Ran all test suites.")
	if failedTests > 0 {
		g.exit(1)
		return false
	}
	g.l("##[endgroup]")
	return true
}

func (g *gen) docker() bool {
	rng := g.rng
	g.l("##[group]Run docker build -t storefront .")
	g.l("#1 [internal] load build definition from Dockerfile")
	g.l("#1 transferring dockerfile: %dB done", 612)
	g.l("#2 [internal] load metadata for docker.io/library/node:20-slim")
	g.l("#2 DONE %d.%ds", rng.IntN(2), rng.IntN(10))
	digest := "sha256:" + "5fd3b0b5c1a6d1b2c0e9f7a48b3d2e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d"
	g.l("#3 [1/5] FROM docker.io/library/node:20-slim@%s", digest)
	for range 3 + rng.IntN(9) { // progress lines: a different number every run
		g.l("#3 sha256:%s %d.%dMB / 48.2MB %d.%ds", g.hex(64), rng.IntN(48), rng.IntN(10), rng.IntN(5), rng.IntN(10))
	}
	g.l("#3 DONE %d.%ds", 2+rng.IntN(5), rng.IntN(10))
	for i, step := range []string{"WORKDIR /app", "COPY package*.json ./", "RUN npm ci --omit=dev", "COPY dist ./dist"} {
		if rng.IntN(3) == 0 {
			g.l("#%d [%d/5] %s", 4+i, 2+i, step)
			g.l("#%d CACHED", 4+i)
		} else {
			g.l("#%d [%d/5] %s", 4+i, 2+i, step)
			g.l("#%d DONE %d.%ds", 4+i, rng.IntN(20), rng.IntN(10))
		}
	}
	g.l("#8 exporting to image")
	g.l("#8 writing image sha256:%s done", g.hex(64))
	g.l("#8 naming to docker.io/library/storefront done")
	g.l("#8 DONE %d.%ds", rng.IntN(3), rng.IntN(10))
	g.l("##[endgroup]")
	return true
}

func (r runLog) write(path string) {
	must(os.WriteFile(path, []byte(strings.Join(r.lines, "\n")+"\n"), 0o644))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

// ---- comparators -----------------------------------------------------------

type whyredOut struct {
	Blocks []struct {
		Rank    int
		Entries []struct {
			Line, Repeat int
			Kind         string
		}
	}
}

// whyred returns the flagged line indices (0-based) and line -> block rank.
func whyred(bin string, baselines []string, failing string) (map[int]bool, map[int]int) {
	args := append([]string{"-f", "json", "-C", "0", "--top", "0"}, baselines...)
	out, err := exec.Command(bin, append(args, failing)...).Output()
	must(err)
	var res whyredOut
	must(json.Unmarshal(out, &res))
	set, rank := map[int]bool{}, map[int]int{}
	for _, b := range res.Blocks {
		for _, e := range b.Entries {
			if e.Kind == "" {
				continue
			}
			for l := e.Line; l <= e.Line+e.Repeat; l++ {
				set[l-1], rank[l-1] = true, b.Rank
			}
		}
	}
	return set, rank
}

var hunk = regexp.MustCompile(`(?m)^[0-9,]+[ac]([0-9]+)(?:,([0-9]+))?$`)

// gnuDiff returns the lines of b that `diff a b` reports as added or changed.
func gnuDiff(a, b string) map[int]bool {
	out, _ := exec.Command("diff", a, b).Output() // exit status 1 just means "different"
	set := map[int]bool{}
	for _, m := range hunk.FindAllStringSubmatch(string(out), -1) {
		from, _ := strconv.Atoi(m[1])
		to := from
		if m[2] != "" {
			to, _ = strconv.Atoi(m[2])
		}
		for l := from; l <= to; l++ {
			set[l-1] = true
		}
	}
	return set
}

// normalise is the regex approach of scripts like diff-logs: replace the
// volatile patterns you thought of, then diff. Line count is preserved.
var normalisers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\dT[\d:.]+Z `), ""},
	{regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`), "UUID"},
	{regexp.MustCompile(`\b[0-9a-f]{7,64}\b`), "HASH"},
	{regexp.MustCompile(`\b\d+(\.\d+)?\s?(ms|s|kB|MB|GB|B)\b`), "N"},
	{regexp.MustCompile(`/tmp/[\w.-]+`), "/tmp/X"},
}

func normalised(src string) []byte {
	b, err := os.ReadFile(src)
	must(err)
	for _, n := range normalisers {
		b = n.re.ReplaceAll(b, []byte(n.with))
	}
	return b
}

func normalise(src, dst string) { must(os.WriteFile(dst, normalised(src), 0o644)) }

// setDiff is the strongest simple script: the same regex normalisation, but
// order-insensitive - flag every line of the failing log whose normalised
// form no passing run contains (sort | uniq | comm, in effect).
func setDiff(baselines []string, failing string) map[int]bool {
	known := map[string]bool{}
	for _, p := range baselines {
		for _, l := range strings.Split(string(normalised(p)), "\n") {
			known[l] = true
		}
	}
	set := map[int]bool{}
	for i, l := range strings.Split(strings.TrimSuffix(string(normalised(failing)), "\n"), "\n") {
		if !known[l] {
			set[i] = true
		}
	}
	return set
}

var keywords = regexp.MustCompile(`(?i)error|fail|fatal|exception|panic`)

func grep(lines []string) map[int]bool {
	set := map[int]bool{}
	for i, l := range lines {
		if keywords.MatchString(l) {
			set[i] = true
		}
	}
	return set
}

// ---- evaluation ------------------------------------------------------------

type score struct {
	Tool                   string
	Runs                   int
	Output, Recall, Prec   float64 // means over runs
	Root, Top1, Top3       float64 // fraction of runs
	sumOut, sumRec, sumPre float64
	root, top1, top3       int
}

func (s *score) add(flagged map[int]bool, r runLog, rank map[int]int) {
	hit, faultLines := 0, 0
	for i, f := range r.fault {
		if f {
			faultLines++
			if flagged[i] {
				hit++
			}
		}
	}
	s.Runs++
	s.sumOut += float64(len(flagged))
	s.sumRec += float64(hit) / float64(faultLines)
	if len(flagged) > 0 {
		s.sumPre += float64(hit) / float64(len(flagged))
	}
	if flagged[r.root] {
		s.root++
	}
	if rank != nil {
		best := 1 << 30
		for i, f := range r.fault {
			if f && rank[i] > 0 && rank[i] < best {
				best = rank[i]
			}
		}
		if best == 1 {
			s.top1++
		}
		if best <= 3 {
			s.top3++
		}
	}
}

func (s *score) finish() {
	n := float64(s.Runs)
	s.Output, s.Recall, s.Prec = s.sumOut/n, s.sumRec/n, s.sumPre/n
	s.Root, s.Top1, s.Top3 = float64(s.root)/n, float64(s.top1)/n, float64(s.top3)/n
}

func eval(bin string, n int, outPath string) {
	dir, err := os.MkdirTemp("", "whyred-bench")
	must(err)
	defer os.RemoveAll(dir)
	_, diffErr := exec.LookPath("diff")

	names := []string{"grep -iE 'error|fail|fatal|exception|panic'", "diff (1 passing run)", "regex-normalise + diff (1 passing run)",
		"regex-normalise + set difference (1 passing run)", "regex-normalise + set difference (3 passing runs)",
		"whyred (1 passing run)", "whyred (2 passing runs)", "whyred (3 passing runs)", "whyred (5 passing runs)"}
	all := map[string]*score{}
	perFault := map[string]map[string]*score{}
	get := func(fault, tool string) []*score {
		if all[tool] == nil {
			all[tool] = &score{Tool: tool}
		}
		if perFault[fault] == nil {
			perFault[fault] = map[string]*score{}
		}
		if perFault[fault][tool] == nil {
			perFault[fault][tool] = &score{Tool: tool}
		}
		return []*score{all[tool], perFault[fault][tool]}
	}
	var totalLines float64
	clean := map[string]*score{} // false positives on a passing run: nothing should be flagged

	for seed := 1; seed <= n; seed++ {
		rng := rand.New(rand.NewPCG(uint64(seed), 99))
		p := newProject(rng, 1)
		fault := faults[seed%len(faults)]
		var greens []string
		for i := range 5 {
			path := filepath.Join(dir, fmt.Sprintf("green-%d.log", i))
			generate(p, rng, "").write(path)
			greens = append(greens, path)
		}
		red := generate(p, rng, fault)
		redPath := filepath.Join(dir, "red.log")
		red.write(redPath)
		totalLines += float64(len(red.lines))

		record := func(tool string, flagged map[int]bool, rank map[int]int) {
			for _, s := range get(fault, tool) {
				s.add(flagged, red, rank)
			}
		}
		record(names[0], grep(red.lines), nil)
		if diffErr == nil {
			record(names[1], gnuDiff(greens[0], redPath), nil)
			ng, nr := filepath.Join(dir, "ng.log"), filepath.Join(dir, "nr.log")
			normalise(greens[0], ng)
			normalise(redPath, nr)
			record(names[2], gnuDiff(ng, nr), nil)
		}
		record(names[3], setDiff(greens[:1], redPath), nil)
		record(names[4], setDiff(greens[:3], redPath), nil)
		for i, k := range []int{1, 2, 3, 5} {
			set, rank := whyred(bin, greens[:k], redPath)
			record(names[5+i], set, rank)
		}

		// Control: a sixth passing run against the same baselines.
		ctl := generate(p, rng, "")
		ctlPath := filepath.Join(dir, "ctl.log")
		ctl.write(ctlPath)
		addClean := func(tool string, flagged map[int]bool) {
			if clean[tool] == nil {
				clean[tool] = &score{Tool: tool}
			}
			clean[tool].Runs++
			clean[tool].sumOut += float64(len(flagged))
		}
		addClean(names[0], grep(ctl.lines))
		if diffErr == nil {
			addClean(names[1], gnuDiff(greens[0], ctlPath))
			nc := filepath.Join(dir, "nc.log")
			normalise(ctlPath, nc)
			addClean(names[2], gnuDiff(filepath.Join(dir, "ng.log"), nc))
		}
		addClean(names[3], setDiff(greens[:1], ctlPath))
		addClean(names[4], setDiff(greens[:3], ctlPath))
		for i, k := range []int{1, 2, 3, 5} {
			set, _ := whyred(bin, greens[:k], ctlPath)
			addClean(names[5+i], set)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d failing runs (%d per fault type), mean %.0f lines each.\n\n", n, n/len(faults), totalLines/float64(n))
	table := func(m map[string]*score) {
		fmt.Fprintln(&b, "| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |")
		fmt.Fprintln(&b, "|---|---:|---:|---:|---:|---:|---:|")
		for _, name := range names {
			s := m[name]
			if s == nil {
				continue
			}
			s.finish()
			top1, top3 := "–", "–"
			if strings.HasPrefix(name, "whyred") {
				top1, top3 = fmt.Sprintf("%.0f%%", 100*s.Top1), fmt.Sprintf("%.0f%%", 100*s.Top3)
			}
			fmt.Fprintf(&b, "| %s | %.1f | %.0f%% | %.0f%% | %.0f%% | %s | %s |\n", name, s.Output, 100*s.Recall, 100*s.Root, 100*s.Prec, top1, top3)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintln(&b, "#### All faults")
	table(all)
	fmt.Fprintln(&b, "#### False alarms on a passing run (lines flagged; ideal is 0)")
	fmt.Fprintln(&b, "| tool | lines flagged |\n|---|---:|")
	for _, name := range names {
		if s := clean[name]; s != nil {
			fmt.Fprintf(&b, "| %s | %.1f |\n", name, s.sumOut/float64(s.Runs))
		}
	}
	fmt.Fprintln(&b)
	keys := make([]string, 0, len(perFault))
	for k := range perFault {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "#### %s\n", k)
		table(perFault[k])
	}
	fmt.Print(b.String())
	if outPath != "" {
		must(os.WriteFile(outPath, []byte(b.String()), 0o644))
	}
}

// ---- main ------------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bench gen|eval|big [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("dir", ".", "output directory")
	seed := fs.Uint64("seed", 7, "random seed")
	fault := fs.String("fault", "dep_drift", "one of "+strings.Join(faults, ", "))
	greens := fs.Int("greens", 3, "passing runs to generate")
	bin := fs.String("bin", "./whyred", "whyred binary")
	n := fs.Int("n", 120, "failing runs to evaluate")
	out := fs.String("out", "", "also write the tables to this file")
	mb := fs.Int("mb", 200, "approximate size of each generated log")
	fs.Parse(os.Args[2:])

	switch os.Args[1] {
	case "gen":
		must(os.MkdirAll(*dir, 0o755))
		rng := rand.New(rand.NewPCG(*seed, 99))
		p := newProject(rng, 1)
		for i := 1; i <= *greens; i++ {
			generate(p, rng, "").write(filepath.Join(*dir, fmt.Sprintf("passing-%d.log", i)))
		}
		red := generate(p, rng, *fault)
		red.write(filepath.Join(*dir, "failing.log"))
		fmt.Printf("wrote %d passing runs and failing.log (%d lines, fault %s, root cause on line %d) to %s\n", *greens, len(red.lines), *fault, red.root+1, *dir)
	case "eval":
		eval(*bin, *n, *out)
	case "big":
		must(os.MkdirAll(*dir, 0o755))
		rng := rand.New(rand.NewPCG(*seed, 99))
		// ~75 bytes per line, ~11 lines per test file.
		p := newProject(rng, max(1, *mb<<20/75/11/20))
		for _, name := range []string{"passing-1.log", "passing-2.log"} {
			generate(p, rng, "").write(filepath.Join(*dir, name))
		}
		red := generate(p, rng, "test_fail")
		red.write(filepath.Join(*dir, "failing.log"))
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "wrote 3 logs of %d lines to %s\n", len(red.lines), *dir)
		os.Stdout.Write(buf.Bytes())
	default:
		fmt.Fprintln(os.Stderr, "usage: bench gen|eval|big [flags]")
		os.Exit(2)
	}
}

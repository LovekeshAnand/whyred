# Benchmarks

Everything here was measured on one machine with the commands shown; nothing
is estimated. Where a result is unflattering or a comparison could not be
made, it says so.

**Machine:** Intel Core i7-11850H (8 cores / 16 threads), 16 GB RAM, Windows 11,
Go 1.27, NVMe SSD. GNU grep / diff / sort from Git for Windows.

## 1. Does it find the failure? (synthetic corpus)

```
go build -o whyred.exe .
go run ./bench eval -bin ./whyred.exe -n 120 -out docs/bench-accuracy.md
```

The harness generates a Node project's GitHub Actions job (checkout, `npm ci`,
vite build, parallel jest, docker build) with the noise real jobs have:
timestamps, durations, SHAs, UUIDs, random ports, PIDs, temp dirs, hashed
asset names, a different commit message every run, test files finishing in a
different order every run, a varying number of progress lines, the occasional
retry warning or cache miss, and tests that log *expected* errors. For each of
120 seeds it writes five passing runs and one failing run with a labelled
fault of one of six kinds (20 each):

| fault | what the log shows |
|---|---|
| `test_fail` | a jest assertion failure |
| `compile_error` | TypeScript errors |
| `dep_drift` | a dependency floats to a new patch version (**root cause, no error text**), tests then throw a `TypeError` |
| `oom` | V8 heap exhaustion with a native stack trace |
| `network` | `npm ci` retries, then `ECONNRESET` |
| `hang` | a test file never reports; the job is cancelled by the timeout |

"Lines to read" is the number of lines a tool flags (no context). "Fault lines
found" is recall over the labelled lines. "Root cause line found" asks only
about the first line of the fault. Precision is the share of flagged lines
that belong to the fault.

### All 120 failing runs (mean 184 lines each)

| tool | lines to read | fault lines found | root cause line found | precision |
|---|---:|---:|---:|---:|
| `grep -iE 'error\|fail\|fatal\|exception\|panic'` | 20.1 | 57% | 50% | 56% |
| `diff` against 1 passing run | 183.9 | 100% | 100% | 13% |
| regex-normalise + `diff`, 1 passing run | 81.2 | 100% | 97% | 40% |
| regex-normalise + set difference, 1 passing run | 20.0 | 98% | 97% | 56% |
| regex-normalise + set difference, 3 passing runs | 19.1 | 95% | 92% | 56% |
| **whyred**, 1 passing run | 18.0 | 97% | 97% | 60% |
| **whyred**, 2 passing runs | 11.6 | 97% | 97% | 93% |
| **whyred**, 3 passing runs | 10.9 | 95% | 92% | 97% |
| **whyred**, 5 passing runs | 10.6 | 94% | 91% | 98% |

### False alarms: a sixth *passing* run against the same baselines

| tool | lines flagged (ideal: 0) |
|---|---:|
| grep | 26.3 |
| `diff`, 1 passing run | 329.6 |
| regex-normalise + `diff`, 1 passing run | 141.5 |
| regex-normalise + set difference, 1 passing run | 16.0 |
| regex-normalise + set difference, 3 passing runs | 14.1 |
| **whyred**, 1 passing run | 10.3 |
| **whyred**, 2 passing runs | 1.1 |
| **whyred**, 3 passing runs | 0.5 |
| **whyred**, 5 passing runs | 0.2 |

### Ranking (whyred only)

| passing runs | a fault line is in block #1 | in the top 3 blocks |
|---:|---:|---:|
| 1 | 80% | 100% |
| 2 | 85% | 99% |
| 3 | 87% | 100% |
| 5 | 89% | 100% |

Per-fault tables are in [bench-accuracy.md](bench-accuracy.md), exactly as the
harness printed them.

### What these numbers say

- **With one passing run, whyred is about level with a well-written script**
  (normalise with regexes, then take the set difference): 18 vs 20 lines,
  60% vs 56% precision. If one baseline is all you will ever have, the gain is
  convenience, ranking and the report, not accuracy.
- **With two or more passing runs it is a different tool.** Precision goes from
  60% to 93–98% and false alarms from ~10 lines to ~0.5, because noise is
  learned from the passing runs disagreeing. The script cannot do that: more
  baselines barely help it (56% → 56%, 16 → 14 false alarms).
- **grep misses half the root causes** (0% for `dep_drift`, `oom` and `hang`,
  where the first line of the fault has no failure keyword) and a quarter of
  what it prints on a *passing* run is noise from tests that log expected errors.
- Plain `diff` flags everything: timestamps alone change every line.

### Where whyred loses

- **`network` faults: root-cause recall falls as baselines are added**
  (80% → 45% at five runs), which is what pulls the all-faults figure from 97%
  down to 91%. The injected root is the *first* retry warning. Passing runs
  print that same warning now and then, so once it is in a baseline it is a
  known line; whyred then reports the retries once, as a repeat spike, and the
  `npm error` lines that follow as new. The failure is still found (a fault
  line is in block #1 in 100% of `network` runs), but the first retry line
  itself is not flagged. grep scores 100% here.
- `grep` has the best precision on `compile_error`, `oom` and `network`
  (97–100%): when the failure announces itself with the word "error", grep is
  hard to beat.
- Block #1 is not a fault line in 11–20% of runs. In the cases inspected, the
  top block was a genuine difference that happened not to be the cause: a
  cache miss or a retry warning that no passing run had.
- "Lines to read" counts flagged lines only, not the `-C 2` context whyred
  prints by default nor the "never happened" list.

### Limits of this benchmark

- **The corpus is synthetic and written by the author of the tool.** It models
  one ecosystem. The generator, seeds and labels are in `bench/main.go`; run it,
  change it, add your own noise.
- The "regex-normalise" comparators are a reimplementation of the approach of
  scripts such as diff-logs (five patterns: timestamp prefix, UUIDs, hex ≥ 7,
  durations and sizes, `/tmp` paths), not those tools themselves.
- **LogJuicer / logreduce, the closest existing tools, were not benchmarked**:
  the machine had no Rust toolchain and no running Docker daemon.

## 2. Real logs: `go test -v` on this repository

`examples/go-test/` holds unedited output of `go test -v -count=1 -shuffle=on .`
— three passing runs, and one run with a real regression introduced for the
purpose (`maxPerKey` changed from 3 to 2, which breaks one test).

| tool | lines flagged | of 77 |
|---|---:|---|
| `grep -ciE 'error\|fail\|fatal\|exception\|panic'` | 6 | misses the `got:` / `want:` lines that say what is wrong |
| `diff passing-1.log failing.log` | 48 | test order is shuffled |
| whyred, 1 passing run | 8 | 7 fault lines + the shuffle seed |
| whyred, 3 passing runs | 7 | all 7 are the failure; the shuffle seed was learned as noise |

This is one small example, not a study.

## 3. Speed and memory

Three ~192 MB logs (2.76 million lines each) from `go run ./bench big -mb 200`.
Wall time and peak working set, each run twice:

| command | input | time | peak memory |
|---|---|---:|---:|
| `whyred passing-1.log failing.log` | 384 MB | 4.2–4.4 s | 21–24 MB |
| `whyred passing-1.log passing-2.log failing.log` | 576 MB | 5.0–5.5 s | 77 MB |
| `whyred learn -o m passing-1.log passing-2.log` | 384 MB | 2.3 s | 100 MB |
| `whyred --model m failing.log` (model file: 2.4 MB) | 192 MB | 2.9 s | 50 MB |
| `grep -ciE 'error\|fail\|…' failing.log` | 192 MB | 0.39–0.45 s | 8 MB |
| `diff passing-1.log failing.log` (prints 5.5 M lines) | 384 MB | 4.2 s | 659 MB¹ |
| `sort -u` both + `comm -13` (set difference) | 384 MB | 14.5 s | not measured |

¹ measured with `diff --brief` (3.1 s).

`go test -bench .` (one core, 32 MB in memory): learn 122–143 MB/s, analyse
120–130 MB/s, 20 heap allocations per 32 MB analysed.

Notes:

- **These timings were taken on battery.** Earlier in the same session the
  same benchmarks ran about 1.5× faster (184–193 MB/s; 2.5–2.7 s for the first
  row); re-running the earlier code afterwards gave the slower numbers too, so
  the difference is the machine's power state, not the code. Read the table as
  the pessimistic end.
- grep is ~10× faster. It is a keyword scan; whyred tokenises, shapes and
  hashes every line. Passing logs are read in parallel (one core each); a
  single log is processed on one core.
- Memory tracks the number of *distinct line shapes*, not log size: 22,124
  shapes for 2.76 M lines here. A log of mostly unique lines is the worst case
  (see Known limits in [ARCHITECTURE.md](ARCHITECTURE.md)).

## 4. Robustness checks that were run

- `go test ./...`: 30 tests, including regression tests for every confirmed
  finding of the independent review.
- Native fuzzing of learn + analyse + all four renderers (`FuzzAnalyze`):
  three runs of 30–45 s, ~1.3 M executions in total, no failures. Invariants
  checked: no panic, a log is never anomalous against a baseline containing
  it, reported line numbers strictly increase.
- **Not run locally: the race detector** (`go test -race` needs 64-bit cgo; the
  machine's gcc is 32-bit). The CI workflow runs it on Linux. The only shared
  state between goroutines is two slices written at distinct indices.

# whyred

**Why is this run red?** Give `whyred` the logs of a few passing CI runs and the
log of a failing one. It prints the handful of lines the failing run has that
passing runs never do — and the values that silently changed.

```
$ whyred passing-1.log passing-2.log passing-3.log failing.log -C 1 --top 3
whyred: 16 lines of 263 do not fit the baseline (6 blocks).
  baseline: 3 runs, 866 lines -> 223 line shapes, 3 wildcards learned

#2  line 44  score 1.8
        43 | 2026-11-02T20:19:09.4200000Z + redis@5.5.7
 ~      44 | 2026-11-02T20:19:09.6380000Z + bullmq@0.14.22   <- bullmq@0.14.22 was bullmq@0.14.21; changed before the first failure
        45 | 2026-11-02T20:19:09.6600000Z + sharp@4.12.26

#1  line 169  score 5.7   <- start here
       168 | 2026-11-02T20:19:33.8530000Z   ✓ rounds expired tokens (1 ms)
 +     169 | 2026-11-02T20:19:34.1340000Z   ● controller › renders empty input
 +     170 | 2026-11-02T20:19:34.2910000Z     TypeError: (0 , _util.normalise) is not a function   <- 2x in this log
 +     171 | 2026-11-02T20:19:34.4000000Z       at Object.normalise (src/cart/controller.ts:90:2)
 +     172 | 2026-11-02T20:19:34.5540000Z       at Object.<anonymous> (src/cart/controller.test.ts:67:21)
 ...
never happened: 22 line shapes present in every baseline run
 -         | 2026-11-03T17:03:38.7250000Z PASS src/auth/mapper.test.ts (3.159 s)
 -         | 2026-11-03T17:03:51.5710000Z Test Suites: 16 passed, 16 total
 -         | 2026-11-03T17:03:52.4540000Z ##[group]Run docker build -t storefront .
```

That is the real output for [`examples/ci`](examples/ci). The cause of that
failure is line 44: a dependency floated to a new patch release. Nothing on
that line says "error", so `grep` never shows it, and `diff` buries it under
260 timestamp changes.

![HTML report](docs/report.png)

## Why

When CI fails, the log is thousands of lines and the usual tools are
`grep -i error` and scrolling. grep misses every failure that doesn't use the
word, and shows every test that logs an *expected* error. `diff` against a
passing run reports every line, because every line has a timestamp. Scripts
that regex away the timestamps break when a parallel test runner prints files
in a different order, and only know the noise someone wrote a pattern for.

whyred's idea: **two passing runs disagree with each other exactly where the
noise is.** So it doesn't need a list of noise patterns — it learns them.

| | finds failures that don't say "error" | ignores timestamps, ids, durations | ignores reordered output | learns project-specific noise | catches values that changed |
|---|:-:|:-:|:-:|:-:|:-:|
| `grep -i error` | no | — | — | no | no |
| `diff` | yes | no | no | no | drowned |
| regex-normalise + `diff` | yes | the ones you listed | no | no | drowned |
| logreduce / LogJuicer¹ | yes | yes | yes | from baselines | no |
| **whyred** | yes | yes | yes | from passing runs disagreeing | yes |

¹ from their documentation; not benchmarked here (see [BENCHMARKS.md](docs/BENCHMARKS.md)).

## Install

Needs Go 1.24 or newer. No other dependencies.

```sh
go install github.com/LovekeshAnand/whyred@latest
# or, from a clone:
go build -o whyred .        # whyred.exe on Windows
```

## Use

```sh
whyred passing.log failing.log                  # one baseline works
whyred pass-*.log failing.log                   # two or three is much better
some-command 2>&1 | whyred passing.log -        # failing log from stdin
whyred pass-*.log failing.log -o report.html    # self-contained HTML report
whyred pass-*.log failing.log -f md >> "$GITHUB_STEP_SUMMARY"
whyred pass-*.log failing.log -f json           # for other tools
```

Reuse a baseline without keeping the logs:

```sh
whyred learn -o ci.whyred pass-1.log pass-2.log pass-3.log
whyred --model ci.whyred failing.log
```

Three markers:

| | meaning |
|---|---|
| `+` | a line shape no passing run ever printed |
| `~` | a known line, but a value every passing run agreed on is different (`0 failed` → `2 failed`, `exit code 0` → `1`, a version, a status code) |
| `*` | a known line repeated far more often than in any passing run (retry loops) |

…and **never happened**: lines every passing run printed and this one didn't,
in order. The first few show where the run stopped.

Flags: `-C N` context lines (2), `--top N` highest-ranked blocks to show (25,
0 = all), `--mask REGEX` to declare something noise yourself, `--exit-code` to
exit 1 when anything is found, `--color auto|always|never`. `whyred --help`
has the rest. Inputs may be gzip-compressed or UTF-16 (what Windows
PowerShell's `>` writes).

### With GitHub Actions

```sh
gh run view <failed-run-id> --log > failing.log
for id in $(gh run list -w ci.yml -s success -L 3 --json databaseId -q '.[].databaseId'); do
  gh run view "$id" --log > "pass-$id.log"
done
whyred pass-*.log failing.log
```

This recipe uses only documented `gh` commands, but it has **not been run as
part of this project** (`gh` was not installed on the development machine).

## How it works

1. Each line becomes a **template**: words stay, anything with a digit becomes a
   slot (`PASS src/a.test.ts (1.2 s)` → `PASS src/a.test.ts #.# s`), and
   machine-generated ids (hashes, UUIDs, timestamps) collapse to `<*>`.
2. Passing runs are learned in parallel and merged. A slot whose values
   **differ between passing runs** is noise. A slot they all agree on is a
   tracked value. Lines that differ between runs in one word, or in a free-text
   tail, become wildcards.
3. The failing log is streamed once and compared as a multiset of templates,
   so the order of parallel output does not matter.
4. What is left is grouped into blocks and ranked: failure-looking lines first,
   earliest first; then values that changed before the first failure.

Design, data structures, trade-offs and limits: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## How well does it work

Measured, on a synthetic corpus of 120 failing CI runs with labelled faults
(method, per-fault tables and caveats in [docs/BENCHMARKS.md](docs/BENCHMARKS.md)):

| tool | lines to read | root cause line found | precision | lines flagged on a *passing* run |
|---|---:|---:|---:|---:|
| `grep -iE 'error\|fail\|fatal\|exception\|panic'` | 20.1 | 50% | 56% | 26.3 |
| `diff`, 1 passing run | 183.9 | 100% | 13% | 329.6 |
| regex-normalise + set difference, 3 passing runs | 19.1 | 92% | 56% | 14.1 |
| whyred, 1 passing run | 18.0 | 97% | 60% | 10.3 |
| **whyred, 3 passing runs** | **10.9** | 92% | **97%** | **0.5** |

Read this honestly:

- With **one** passing run whyred is roughly level with a good regex script.
  The difference shows with **two or more**, where it learns the noise.
- The corpus is synthetic and written by the tool's author. One real example
  (`go test -v` output with a real regression) is in `examples/go-test`.
- It loses to grep on failures that announce themselves and repeat a line
  passing runs also print (`network` faults: 45–80% root-cause recall vs 100%).
- 190 MB logs: about 4 s and 21 MB of memory per baseline+failing pair on a
  laptop on battery; grep is ~10× faster.

## Security

CI logs are untrusted: a pull request can print anything. whyred strips ANSI
sequences and neutralises control characters before anything reaches your
terminal, embeds report data in HTML as escaped JSON rendered with
`textContent` only, uses a linear-time regex engine for `--mask`, caps and
sanitises model files, and never touches the network or runs a subprocess.

A model file contains example lines and a few tracked values from your logs.
Treat it like the logs.

## Limits

- It needs a passing run of the *same job*. A baseline from a different
  workflow makes everything look new.
- It tells you what is different, not what is wrong. A cache miss that no
  passing run had will be reported, and sometimes ranked first.
- Ranking uses an English failure lexicon.
- More in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#9-known-limits).

## Development

```sh
go test ./...                                         # unit + regression tests
go test -run xxx -fuzz FuzzAnalyze -fuzztime 30s      # fuzz learn, analyse and renderers
go test -run xxx -bench .                             # throughput
go run ./bench gen -dir /tmp/demo -fault oom          # make a scenario to play with
go run ./bench eval -bin ./whyred -n 120              # reproduce the accuracy tables
```

How the problem was chosen: [docs/RESEARCH.md](docs/RESEARCH.md).
What was built when, and what the independent review found: [PROGRESS.md](PROGRESS.md).

## License

MIT

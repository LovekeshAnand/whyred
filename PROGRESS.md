# Progress log

Decisions and checkpoints, newest last.

## 1. Research and choice

- Searched for CI-log pain, existing baseline-diff tools, log template miners,
  test-pollution and Docker-cache tooling. Shortlist of ten and scoring in
  [docs/RESEARCH.md](docs/RESEARCH.md).
- Chose a baseline-relative log differ. Differentiator: learn noise from
  passing runs disagreeing with each other; track values that passing runs
  agree on.

## 2. Architecture decisions

- Go, standard library only, single `package main`, one binary.
- Exact template hashing (position-keyed sum, so leave-one-out and prefix
  hashes are O(1)) instead of a Drain similarity tree: deterministic and
  order-independent.
- Bag-of-templates comparison; no alignment.
- One streaming pass over the failing log with a ring buffer for context.
- Model persisted as gzip JSON.

## 3. Build checkpoints

1. Tokeniser, model, analyser, four renderers, CLI — tests green.
2. First run on generated CI logs showed three problems, all fixed:
   the silent root cause ranked last (→ tiered ranking, "changed before the
   first failure"); touching blocks were split (→ merge while context is
   contiguous); the "never happened" list showed templates (→ example lines).
3. Evaluation harness exposed over-promotion of coincidental value changes
   with two baselines and unlearned noise (hashed asset names without digits,
   commit messages) → generated-id positions, prefix wildcards.
4. Real `go test` logs captured as a second example.
5. HTML report checked in headless Edge at desktop width (screenshot in
   `docs/report.png`). The Chrome extension was not connected, so the report
   was **not** exercised interactively (filters, keyboard navigation and the
   theme toggle are untested by hand), and the phone-width screenshot was
   inconclusive because headless Edge would not go below ~500 px.

## 4. Independent review

A separate reviewer agent attacked the code and reproduced its findings.
All confirmed findings and what was done:

| # | Finding | Resolution |
|---|---|---|
| 1 | Prefix wildcard silenced every new line in `date time LEVEL [component]` logs | fixed: prefix needs ≥4 *literal* tokens and once-per-run lines; regression test |
| 2 | Output non-deterministic (mask cap filled in map order) | fixed: sorted iteration; regression test |
| 3 | Model file memory bomb (8 MB → 5 GB) | fixed: 256 MiB decompression cap; regression test |
| 4 | Terminal escape injection through model strings | fixed: model text and values sanitised on load; regression test |
| 5 | CPU cliff: lexicon re-run per prefix length; slow on 60 KB lines | fixed: one search per line, capped at 8 KiB; regression test |
| 6 | Hex status codes (`0x0` → `0xC0000005`) invisible | fixed: `0x…` is a tracked value |
| 7 | Day-of-month flagged on every syslog line | fixed: numbers next to a month/weekday name are volatile |
| 8 | Silent misses: past token 256; `==` → `!=`; id → `NONE` | fixed (fold remainder; keep `!`; same-length rule) |
| 8 | Silent misses: `0 bytes` → `4096 bytes`; 5 repeats below spike threshold | **not fixed**, documented as limits |
| 9 | `\r` and unterminated OSC could hide a failure | fixed |
| 10 | Baseline glob could include the failing log | fixed: skipped with a warning |
| 11 | UTF-16 lone surrogate swallowed the next unit | fixed |
| 12 | Usage text claimed ports are ignored | fixed |
| 13 | Benchmark omitted the order-insensitive regex script | fixed: added as a comparator; README and BENCHMARKS now say whyred is about level with it at one baseline |
| – | Unbounded memory with millions of unique baseline lines | **not fixed**, documented |

## 5. Verification status

- `go vet ./...` and `go test ./...` pass (30 tests).
- Fuzzing: three runs, ~1.3 M executions, no failures.
- Accuracy and speed numbers in [docs/BENCHMARKS.md](docs/BENCHMARKS.md) were
  produced by the final code.
- **Not verified:** race detector (no 64-bit cgo locally; CI runs it on Linux,
  and that workflow itself has never run); the `gh` recipe in the README;
  running on macOS and Linux (both cross-compile; tests only ran on Windows);
  interactive behaviour of the HTML report;
  LogJuicer comparison.

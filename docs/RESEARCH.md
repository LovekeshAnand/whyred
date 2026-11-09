# How this project was chosen

This records the research and the shortlist behind whyred. Scores are one
engineer's judgment after a few hours of reading, not market research.

## What was looked at

Web searches (November 2026) on CI-log debugging complaints, existing log-anomaly
tools, log template mining libraries, order-dependent test tooling and Docker
build-cache debugging. Sources that shaped the decision:

- Developers describing CI failures as "exit code 1" with hundreds of unrelated
  lines before the real error, and GitHub's log viewer being slow or failing to
  load long logs:
  [OpenReplay](https://blog.openreplay.com/tips-tricks-debugging-github-actions/),
  [Zebra issue #7637](https://github.com/ZcashFoundation/zebra/issues/7637),
  [InfoQ on GitHub's log rewrite](https://www.infoq.com/news/2021/03/large-logs-ux-github),
  [GitLab #295654](https://gitlab.com/gitlab-org/gitlab/-/issues/295654).
- The standard advice is still `grep -i error` or `gh run view --log-failed`.
- Prior art for "compare against a passing run":
  [logreduce](https://pypi.org/project/logreduce/) (Python, k-NN over hashed
  tokens, built around Zuul) and its Rust successor
  [LogJuicer](https://logjuicer.github.io) (needs a Rust toolchain or their
  release binaries; small user base).
  [diff-logs](https://github.com/kernc/diff-logs) and the
  [compare_logs](https://www.ruby-toolbox.com/projects/compare_logs) gem
  normalise known patterns with regexes and then run `diff`.
- Log template mining (Drain) exists as libraries in Go, Rust and Python, but no
  result was a tool that diffs two logs with it.
- Order-dependent test bisection exists for pytest
  ([detect-test-pollution](https://pypi.org/project/detect-test-pollution/1.0.0))
  and RSpec; a Docker cache-miss explainer exists
  ([whycache](https://pypi.org/project/whycache/)).

## Shortlist

Scored 1–5 on demand, novelty, technical depth, feasibility as a small
self-contained product, and impact. Rows marked † were scored from prior
knowledge of the space, not from searches in this session.

| # | Opportunity | Demand | Novelty | Depth | Feasible | Impact | Total |
|---|---|:-:|:-:|:-:|:-:|:-:|:-:|
| 1 | **Baseline-relative CI log differ** — show only what a failing log has that passing logs don't | 5 | 3 | 4 | 5 | 4 | **21** |
| 2 | Local, faithful GitHub Actions debugging ("push and pray") † | 5 | 1 | 5 | 1 | 5 | 17 |
| 3 | Flaky-test diagnosis by perturbing time / order / concurrency | 4 | 2 | 4 | 2 | 4 | 16 |
| 4 | Docker build cache-miss explainer (which file invalidated which layer) | 4 | 2 | 4 | 2 | 3 | 15 |
| 5 | Order-dependent test polluter bisection for Jest / Go | 3 | 3 | 3 | 3 | 3 | 15 |
| 6 | Lockfile "what actually changed" explainer across ecosystems † | 3 | 2 | 2 | 4 | 3 | 14 |
| 7 | API schema drift detection from recorded traffic † | 3 | 2 | 3 | 3 | 3 | 14 |
| 8 | Stand-alone log template miner CLI | 2 | 1 | 3 | 5 | 2 | 13 |
| 9 | Cross-run CI log search | 3 | 1 | 3 | 3 | 2 | 12 |
| 10 | Postgres migration lock analysis † | 3 | 1 | 3 | 3 | 2 | 12 |

## Why #1

- The pain is universal and the usual answer (`grep error`) is known to be bad:
  it misses failures that don't say "error" and drowns in tests that log
  expected errors.
- The existing baseline-diff tools leave a clear gap. Regex-normalise-then-diff
  breaks as soon as output is reordered (parallel test runners) and only knows
  the noise patterns someone wrote down. logreduce/LogJuicer learn from a
  baseline but are tied to one CI ecosystem and are not a zero-install binary.
- There is a technically interesting idea that none of them use: **two passing
  runs disagree with each other exactly where the noise is.** Noise can be
  learned from green-vs-green instead of guessed.
- It fits in one small, dependency-free binary, and its quality is measurable.

What was rejected and why: #2 is the biggest pain but is a multi-year emulation
project (`act` already shows how hard it is). #3–#5 need deep integration with
specific test runners or BuildKit internals. #6–#10 are either well served or
low impact.

## Known competitor not benchmarked

LogJuicer is the closest tool. It was not benchmarked here: the development
machine had no Rust toolchain and no running Docker daemon. The benchmark
report says so rather than guessing its numbers.

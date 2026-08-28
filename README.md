# 🎏 koi — keeper of issues

[![GitHub release](https://img.shields.io/github/v/release/katbyte/koi?color=blueviolet)](https://github.com/katbyte/koi/releases/latest)
![build](https://github.com/katbyte/koi/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/koi/actions/workflows/test.yaml/badge.svg)
![lint](https://github.com/katbyte/koi/actions/workflows/lint.yaml/badge.svg)
![govulncheck](https://github.com/katbyte/koi/actions/workflows/govulncheck.yaml/badge.svg)
![CodeQL](https://github.com/katbyte/koi/actions/workflows/codeql-analysis.yml/badge.svg)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/koi?color=00ADD8)](https://github.com/katbyte/koi/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/koi?color=blue)](https://github.com/katbyte/koi/blob/main/LICENSE)

`koi` is a keeper of issues: assisted bulk triage of a GitHub repository's
issues, milestones, and changelog bookkeeping. Built for
`hashicorp/terraform-provider-azurerm` (~3,500 open issues, provider now on v5.x,
hundreds of issues reported against long-unsupported majors) — it started as a
tool to **k**lose **o**ld **i**ssues and kept the name when it became the keeper
of them.

It fetches every open issue — **including all comments, which is where the gold
is on old issues** — into a local SQLite database, runs deterministic triage
rules first, uses an AI CLI only for what rules can't decide, and then walks a
human through approving closes one evidence-packed card at a time. Nothing
touches GitHub without an approved action, applies are throttled and capped into
waves, and every mutation is audited and reversible (`koi reopen`).

See [PLAN.md](PLAN.md) for the full design.

## Workflow

```sh
export GITHUB_TOKEN=$(gh auth token)

koi fetch              # everything non-AI in one step: open issues + comments + changelogs +
                       # the all-issues milestone scan -> issues.db, rules run automatically
                       # (resumable; later runs sync incrementally — the only required setup step)
koi review             # interactive card-by-card decisions (y/n/s/e/c/b/t/o/u)
koi stats              # the funnel: what can close, what keeps, what needs AI
koi classify           # AI passes: classify the undetermined, double-check closes for "still an issue on 4.x/5.x" claims
koi review --reason legacy-bug --min-confidence 0.9 --approve-all   # bulk after spot-checking
koi report             # report.html + decisions.csv for async (community manager) review
koi import report/decisions.csv --as manager
koi apply --max 100 --dry-run   # preview a wave
koi apply --max 100             # comment + close, throttled, staleness-guarded
koi reopen 1234 --comment "reopening, closed in error"   # mistake recovery
koi analyse            # (optional) re-run the rules verbosely — review/report/stats/classify do it automatically

koi milestone                    # scan ALL issues (open+closed, light fields) + audit release milestones
koi milestone --skip-scan --csv audit.csv   # re-audit offline, full findings to csv
koi milestone --skip-scan --bucket open-released    # list every finding in one bucket
koi milestone closed-by-pr --skip-scan --apply      # apply using only the strongest evidence class
koi milestone --skip-scan --apply --max 200 # set determinable missing milestones (closed issues only)
```

`koi milestone` maps the PRs tied to each issue to the release that shipped them
via the changelog and checks the issue carries that milestone. Evidence is
ranked: the PR that **closed** the issue, then closing-keyword **linked** PRs
("fixes #N"), then changelog bullets **citing** the issue directly, then bare
**mentions** — the strongest class that yields a release wins, and every
proposal says which class it came from. The `closed-by-pr` / `linked-to-pr` /
`cited` / `mentioned-by-pr` subcommands restrict determination to one class
(e.g. apply the sure things first). Closed issues missing a determinable
milestone are fixable with `--apply` (dry-run previews the complete list);
mismatches and open issues sitting on released milestones are report-only.

## What closes, what keeps

Close proposals (each with its own comment template in `assets/templates/`):
`legacy-bug` (reported against v1–v3, no recent repro claim), `fixed-merged-pr`
(a merged PR references it), `no-response`, `stale-question`, `upstream-core`,
`retired-service`.

Keep protections run **before** any close rule: a credible comment claim of the
issue on v4/v5, an open linked PR, 👍 ≥ `--keep-reactions`, or a recent version
label all pin an issue open. The AI `still-open` pass re-checks every proposed
close with comment activity as a second safety net.

## Config

Flags, env vars, or a `.koi` file (env format) in `$HOME` or `.`:
`GITHUB_TOKEN`, `KOI_REPO`, `KOI_DB`, `KOI_CURRENT_MAJOR`, `KOI_NO_AUTO_FETCH`
(never touch the network for freshness — audit the local db as-is), `KOI_AI_CMD`,
`KOI_AI_MODEL`, `KOI_AS`, `KOI_LOG` (debug/trace HTTP dumps). AI calls shell out
to an already-authenticated CLI — no API key management. `claude` (default),
`gemini`, antigravity's `agy`, and IBM's `bob` are recognised by binary name;
anything speaking one of those dialects works via `KOI_AI_CMD`.

## Building

```sh
make        # fmt + build -> ./koi
make test
make lint
```

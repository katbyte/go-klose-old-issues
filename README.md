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

## The lenses

Each command asks one question about an open issue, from one kind of evidence:

| command | the question | the evidence |
|---|---|---|
| `koi fixed` | a merged **PR** touches this — did it fix it? | code that shipped |
| `koi resolved` | a linked **issue** was dealt with — does its outcome cover this one? | another ticket's fate |
| `koi legacy` | this **bug is old** (v1–v3) and nobody says it's still alive — close as stale? | version + silence |
| `koi milestone` | which release dealt with each issue/PR? (bookkeeping, not closing) | the changelog |

Every lens works the same way: evidence classes as subcommands (strongest
first), an AI that judges the actual substance, and three apply modes —
`--apply` acts on the evidence with no AI, `--apply-with-ai` shows each card
with its score and asks you, `--apply-with-ai-auto[=t]` acts alone above a
confidence threshold. Bare invocation is always a report; `--dry-run` previews
any apply. The lenses overlap on purpose: one issue can be seen by several, and
whichever closes it first removes it from the others.

## Workflow

```sh
export GITHUB_TOKEN=$(gh auth token)

koi fetch              # everything non-AI in one step: open issues + comments + changelogs +
                       # the all-issues milestone scan -> issues.db, rules run automatically
                       # (resumable; later runs sync incrementally — the only required setup step)
koi review             # interactive card-by-card decisions (a/n/s/e/c/b/t/o/u — single keypress)
koi stats              # the funnel: what can close, what keeps, what needs AI
koi classify           # AI passes: classify the undetermined, double-check closes for "still an issue on 4.x/5.x" claims
koi review --reason legacy-bug --min-confidence 0.9 --approve-all   # bulk after spot-checking
koi report             # report.html: every close candidate each lens sees, with its evidence, linked
koi report --with-ai --limit 10   # AI-score a small slice per lens first — cheap end-to-end test
koi apply --max 100 --dry-run   # preview a wave
koi apply --max 100             # comment + close, throttled, staleness-guarded
koi reopen 1234 --comment "reopening, closed in error"   # mistake recovery

koi milestone                    # scan ALL issues (open+closed, light fields) + audit release milestones
koi milestone --skip-scan --csv audit.csv   # re-audit offline, full findings to csv
koi milestone --skip-scan --bucket open-released    # list every finding in one bucket
koi milestone closed-by-pr --skip-scan --apply      # apply using only the strongest evidence class
koi milestone --skip-scan --apply --max 200 # fill missing + correct mismatched milestones (closed issues only)
koi milestone --skip-scan --apply-with-ai   # AI scores each issue↔evidence pairing, you confirm each set (a/s/o/q)
koi milestone --skip-scan --apply-with-ai-auto=0.85 # auto-apply pairings the AI scores at/above the threshold
koi milestone changelog-check --apply       # the PR-side audit: every changelog-cited PR carries the citing release

koi fixed                        # OPEN issues a merged PR references — likely fixed, AI-scored on the match
koi fixed mentioned-by --apply-with-ai      # confirm each close: comments cite the fix PR + shipped version
koi fixed --apply-with-ai-auto=0.9          # auto-close the matches the AI is confident about

koi resolved                     # OPEN issues referencing a CLOSED issue — duplicates of things already dealt with
koi resolved completed --apply-with-ai      # confirm each duplicate close, pointing at the resolved issue

koi legacy                       # closeable bug/crash reports against v1–v3, AI-scored on staleness
koi legacy --major 1 --apply --dry-run      # preview the sure things one major at a time
koi legacy --apply-with-ai                  # AI reads issue + comments, you confirm each close

koi cache                        # list the local db's caches and sizes
koi cache clear ai               # drop AI verdicts (or issues|milestones|prs|texts|changelog|all)
```

`koi milestone` maps the PRs tied to each issue to the release that shipped them
via the changelog and checks the issue carries that milestone. Evidence is
ranked: the PR that **closed** the issue, then closing-keyword **linked** PRs
("fixes #N"), then changelog bullets **citing** the issue directly, then bare
**mentions** — the strongest class that yields a release wins, and every
proposal says which class it came from (colour-coded everywhere it appears).
The `closed-by-pr` / `linked-to-pr` / `cited` / `mentioned-by-pr` subcommands
restrict determination to one class (e.g. apply the sure things first).
`--apply` fills missing milestones and corrects mismatched ones — the changelog
is the ground truth of what shipped where — on closed issues only; open issues
sitting on released milestones stay report-only, and findings blocked by a
release whose milestone was never created are called out with the exact command
to create it.

The weaker evidence classes are where wrong matches hide (number collisions,
PRs that merely mention an issue), so `--apply-with-ai` puts an AI match check
in front of the apply: each candidate issue (title + body) and its evidence PRs
(title + body + changelog bullet) go to the AI CLI, which scores how likely the
evidence actually resolves that issue — verdicts are cached per model, batches
are judged in the background while you review the previous batch, and each card
shows the score and a one-line reason before you accept or skip. `--apply-with-ai-auto`
applies everything at or above the threshold without asking (bare flag = 0.70).

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
anything speaking one of those dialects works via `KOI_AI_CMD`. With a blank
`KOI_AI_MODEL` the CLI's default model is discovered and shown (and aliases like
`fable` resolve to their canonical id) so cached verdicts always record which
model produced them.

## Building

```sh
make        # fmt + build -> ./koi
make test
make lint
```

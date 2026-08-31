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
is on old issues** — into a local SQLite database, then runs a battery of
evidence **checks**: deterministic sweeps tuned for recall surface the
candidates, an AI judge reads each one's actual substance for precision, and a
human approves closes one evidence-packed card at a time. Nothing touches
GitHub without an approved action, applies are throttled and capped into waves,
and every mutation is audited and reversible (`koi reopen`).

## The command shape

Every command follows the same three-level grammar:

```
koi <action> <check> [class]
```

- **action** — what koi changes: `close` (close issues), `label` (add labels),
  `milestone` (set milestones). Each action also has a `report` writing a
  stamped HTML page of every candidate (`close-<yyyymmdd-hhmm>.html`,
  `label-…`, `milestone-…`) so runs never overwrite each other.
- **check** — the question asked and the kind of evidence that answers it:
  `close fixed`, `close stale`, `label question`, …
- **class** — the shape or strength of the evidence, as a subcommand
  (strongest first): `close stale waiting`, `close fixed mentioned-by`,
  `close duplicates similar`. Bare check = all classes.

## The checks

Each check asks one question about an open issue, from one kind of evidence:

| check | the question | the evidence | classes |
|---|---|---|---|
| `koi close fixed` | a merged **PR** touches this — did it fix it? | code that shipped | fixed-by · mentioned-by |
| `koi close resolved` | a linked **issue** was dealt with — does its outcome cover this one? | another ticket's fate | completed · duplicate · not-planned |
| `koi close duplicates` | this duplicates another **OPEN** issue — is it the same ask? | links + near-identical titles | linked · similar |
| `koi close comments` | somebody in the **thread** says it can be closed — were they right? | what people wrote (incl. the reporter's own "my mistake") | maintainer-says · community-says |
| `koi close questions` | this **question** was answered, or died unanswered long ago — close it out? | the thread's replies | answered · dead |
| `koi close stale` | a **maintainer** had the last word and nobody ever answered — thread over? | the maintainer's own words + the waiting-response label | waiting · asked · said |
| `koi close exists` | this **request** asks for something the provider already has — did it ship? | the docs + the changelog | resource · property |
| `koi close legacy` | this **bug is old** (v1–v3) and nobody says it's still alive — close as stale? | version + silence | `--major N` |
| `koi close errors` | the **error it quotes** no longer exists in the provider source — obsolete as written? | git grep, now and at the reported version | verified · panic · unverified |
| `koi close docs` | its **doc page** has been revised since the report — addressed now? | the current page content | — |
| `koi close deprecated` | this references something that has been **removed** — is it moot now? | the upgrade guides | resource · property |
| `koi label version` | its evidence names **affected versions** its labels don't record — label them? | reported versions + comment claims | — |
| `koi label question` | it **reads as a question** its labels don't record — label it? | interrogative titles + ask phrases in prose | — |
| `koi milestone` | which release dealt with each issue/PR? (bookkeeping, not closing) | the changelog | closed-by-pr · linked-to-pr · cited · mentioned-by-pr |

Every check works the same way: evidence classes as subcommands (strongest first), an AI that judges the actual substance, and three apply modes — `--apply` acts on the evidence with no AI (combine with `--dry-run` to get a sense of the changes), `--apply-with-ai` shows each card with its score and asks you, `--apply-with-ai-auto[=t]` acts alone above a
confidence threshold. Bare invocation is always a report; `--dry-run` previews any apply. The checks overlap on purpose: one issue can be seen by several, and whichever closes it first removes it from the others. Labels are only ever added, never removed.

## Workflow

```sh
export GITHUB_TOKEN=$(gh auth token)
export KOI_AI_CMD=claude KOI_AI_MODEL=opus   # required by every --apply-with-ai mode (no default)

koi fetch              # everything non-AI in one step: open issues + comments + changelogs +
                       # the provider docs and removals inventories + the all-issues milestone
                       # scan -> issues.db, rules run automatically (resumable; later runs sync
                       # incrementally — the only required setup step)

koi report --provider-src ~/src/azurerm   # close-<yyyymmdd-hhmm>.html: every close candidate each
                       # check sees, with its evidence, linked. The provider checkout is required —
                       # the errors and docs checks read it, and a report missing checks would
                       # be acted on as if complete (put provider-src in .koi to skip the flag)
koi report --with-ai --limit 10   # AI-score a small slice per check first — cheap end-to-end test
koi label report       # label-<stamp>.html: every label candidate (version, question)
koi milestone report   # milestone-<stamp>.html: the audit's findings by bucket

# then work one check at a time. Bare is always a report; the apply modes act:
#   --apply              act on the evidence, no AI
#   --apply-with-ai      card + score + (a)ccept (s)kip (p)review (o)pen (q)uit per issue
#   --apply-with-ai-auto[=t]  unattended at or above a confidence
koi close fixed --apply-with-ai        # a merged PR references it — did it fix it?
koi close resolved --apply-with-ai     # it references a CLOSED issue — was that its answer?
koi close duplicates --apply-with-ai   # it duplicates another OPEN issue, linked or by title
koi close comments --apply-with-ai     # somebody in the thread says it can be closed
koi close questions --apply-with-ai    # the question was answered, or died unanswered
koi close stale --apply-with-ai        # a maintainer's last word hung unanswered (waiting-response
                                       # labelled: 90 days is enough; otherwise a year)
koi close exists --apply-with-ai       # the request already exists in the provider
koi close legacy --apply-with-ai       # old bug on v1–v3, nobody says it is still alive
koi close errors --apply-with-ai --provider-src ~/src/azurerm   # its quoted error is gone from the source
koi close docs --apply-with-ai --provider-src ~/src/azurerm     # its doc page was revised since — addressed?
koi close deprecated --apply-with-ai   # it leans on something that has been removed

koi label version --apply-with-ai      # add the v/N.x labels the evidence supports (add-only)
koi label question --apply-with-ai     # add the question label to issues that read as asks (add-only)

koi reopen 1234 --comment "reopening, closed in error"   # mistake recovery

koi milestone                    # scan ALL issues (open+closed, light fields) + audit release milestones
koi milestone --skip-scan --apply --max 200 # fill missing + correct mismatched milestones (closed issues only)
koi milestone --skip-scan --apply-with-ai   # AI scores each issue↔evidence pairing, you confirm each set (a/s/o/q)
koi milestone --skip-scan --apply-with-ai-auto=0.85 # auto-apply pairings the AI scores at/above the threshold

# every check takes its evidence classes as subcommands, strongest first:
koi close stale waiting --apply-with-ai           # only waiting-response issues the reporter abandoned
koi close fixed mentioned-by --apply-with-ai      # one class only: comments cite the fix PR + shipped version
koi close duplicates similar --dry-run            # the unlinked half, preview only
koi close exists resource --apply-with-ai-auto=0.9  # only the asked-for resource now existing, above 0.90
koi close legacy --major 1 --apply --dry-run      # legacy scopes by major instead
koi close deprecated property --apply-with-ai     # removed properties, judged one at a time

koi cache                        # list the local db's caches and sizes
koi cache clear ai               # drop AI verdicts (or issues|milestones|prs|texts|changelog|all)

# the older rules-only path — superseded by the checks (questions covers
# stale-question; stale's waiting/asked classes cover no-response) and
# pending retirement:
koi stats                        # the funnel: what can close, what keeps
koi review                       # interactive card-by-card decisions over rules proposals
koi review --reason legacy-bug --min-confidence 0.9 --approve-all   # bulk after spot-checking
koi apply --max 100 --dry-run    # preview a wave of approved actions
koi apply --max 100              # comment + close, throttled, staleness-guarded
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

Every close comments first, from a per-check template in `assets/comments/` —
citing the fix PR and shipped release, the duplicate and its resolution, the
claim in the thread with a deep link, the maintainer's unanswered ask, what was
removed and its successor — then closes as completed (the ask was delivered)
or not planned (the thread is over), and records an auditable action row so
`koi reopen` can undo it.

Keep protections run **before** any close rule: a credible comment claim of the
issue on the current major, an open linked PR, or 👍 ≥ `--keep-reactions` all
pin an issue open. Each check's own judge re-checks every proposed close in
full thread context as a second safety net — commitments ("we'll fix this"),
disputes after a claim, and bug reports wearing the wrong label all score low.

## Config

Flags, env vars, or a `.koi` file (env format) in `$HOME` or `.`:
`GITHUB_TOKEN`, `KOI_REPO`, `KOI_DB`, `KOI_CURRENT_MAJOR`, `KOI_NO_AUTO_FETCH`
(never touch the network for freshness — audit the local db as-is), `KOI_AI_CMD`,
`KOI_AI_MODEL`, `KOI_AS`, `KOI_LOG` (debug/trace HTTP dumps). Per-command flags
like `--provider-src` (the local provider clone the errors and docs checks read)
have no env var — set them on the command line or as `provider-src` in `.koi`.
AI calls shell out to an already-authenticated CLI — no API key management, but
`KOI_AI_CMD` and `KOI_AI_MODEL` are required (no defaults). `claude`, `gemini`,
antigravity's `agy`, and IBM's `bob` are recognised by binary name; anything
speaking one of those dialects works. Model aliases like `fable` resolve to
their canonical id up front, so cached verdicts always record exactly which
model produced them.

## Building

```sh
make        # fmt + build -> ./koi
make test
make lint
```

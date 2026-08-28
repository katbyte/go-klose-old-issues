# koi — assisted close for terraform-provider-azurerm issues

A Go CLI (`koi` — fishing old issues out of the pond) that fetches all open
issues on `hashicorp/terraform-provider-azurerm` into a local SQLite database, runs cheap
deterministic triage rules first, uses AI only where rules can't decide, and then walks a
human (kt or the community manager) through approving and applying closes in throttled
waves. Structure copies `tctest` (golden CLI); workflow philosophy copies `rjg`
(AI advises, human decides, dry-run everywhere, everything resumable).

## The numbers (as of 2026-08-13)

| Slice | Count |
|---|---|
| Open issues total | 3,491 |
| `bug` | 545 |
| `enhancement` | 1,454 |
| `question` | 247 |
| `v/1.x (legacy)` | 9 |
| `v/2.x (legacy)` | 247 |
| `v/3.x (legacy)` | 705 |
| `v/4.x` | 568 |
| `v/5.x` | 11 |
| Created before 2022 (pre-3.0 era) | 524 |
| Created before 2024 (pre-4.0 era) | 1,818 |
| `waiting-response` | 28 |

~961 issues already carry a legacy version label. ~1,950 have no version label at all, but
the issue template has a parseable `### Terraform (and AzureRM Provider) Version` fenced
block (`+ provider.azurerm v1.31.0`) and an `### Affected Resource(s)` list, so a regex
pass will version-classify most of the rest without AI.

Signals worth noting from sampling:
- Some issues carry *multiple* version labels (e.g. #3780 has v/1.x + v/2.x + v/3.x) —
  that means it was re-confirmed across majors and is a strong KEEP signal, not a close.
- Changelogs are split per major (`CHANGELOG-v0.md` … `CHANGELOG-v4.md`, `CHANGELOG.md`
  for v5) with highly structured bullets:
  `* **New Resource**: azurerm_x ([#N](link))` / `* azurerm_x - support for y ([#N](link))`
  — ideal for "was this FR shipped?" matching.
- `service/*` labels + Affected Resource(s) give natural buckets for duplicate detection
  (never compare all 1,454 FRs pairwise; compare within buckets).

## Repo layout (mirrors tctest)

```
main.go                    # build cmd, execute, exit codes — nothing else
cli/
  cmds.go                  # Make(): root + all subcommands, ValidateParams, SilenceUsage idiom
  flags.go                 # FlagData{mapstructure:",squash"}, configureFlags() map, GetFlags()
  fetch.go analyze.go classify.go dupes.go shipped.go
  review.go report.go apply.go stats.go
lib/
  chttp/                   # copied from tctest: named debug transport + retry transport
  clog/  cout/             # copied from tctest (cout gains nothing new; keep JSON collector)
  ai/                      # copied from rjg: shell out to claude CLI, ExtractJSON
  gh/                      # go-github/v89 for REST mutations (close/comment/label)
  ghql/                    # small hand-rolled GraphQL client over chttp (bulk reads)
  db/                      # sqlite: schema, migrations, typed queries (no ORM)
  triage/                  # pure domain logic: template parsing, version extraction,
                           #   rules engine, changelog parsing — all unit-testable
  version/
prompts/                   # embedded (go:embed) AI prompt templates, versioned by hash
templates/                 # embedded close-comment templates, one per reason code
```

Same makefile / .golangci.yml / goreleaser / CI shape as tctest. Vendored deps.
Config: cobra+viper, flags→env→`.koi` file, env prefix `KOI_`, `GITHUB_TOKEN` reused.
Key flags: `--repo` (default hashicorp/terraform-provider-azurerm), `--db` (default
`./issues.db`), `--current-major` (default 5), `--dry-run`, `--yes`, `--max-apply`,
rjg's AI flags (`--ai-cmd`, `--ai-model`, `--ai-timeout`).

## Storage: SQLite

`modernc.org/sqlite` (pure Go — keeps `CGO_ENABLED=0` for goreleaser; it's a big vendor
drop but the only real option without cgo). `database/sql`, hand-written DDL + tiny
migration runner keyed off `PRAGMA user_version`. WAL mode.

Design rule carried over from rjg: **store raw payloads alongside parsed columns** so any
stage can be re-run without refetching, and `sqlite3 issues.db` / `jq` remain usable for
ad-hoc inspection.

```sql
meta(key TEXT PRIMARY KEY, value TEXT);           -- cursors, last_sync, schema info

issues(
  number INTEGER PRIMARY KEY,
  title TEXT, body TEXT, state TEXT, state_reason TEXT,
  author TEXT, author_association TEXT,
  created_at TEXT, updated_at TEXT, closed_at TEXT,
  labels TEXT,                                     -- JSON array
  comment_count INTEGER, thumbs_up INTEGER, reactions_total INTEGER,
  url TEXT, raw TEXT,                              -- raw GraphQL node JSON
  fetched_at TEXT
);

comments(
  id TEXT PRIMARY KEY, issue_number INTEGER REFERENCES issues,
  author TEXT, author_association TEXT, created_at TEXT,
  body TEXT, thumbs_up INTEGER, raw TEXT
);

crossrefs(                                         -- from timelineItems(CROSS_REFERENCED_EVENT)
  issue_number INTEGER, ref_repo TEXT, ref_number INTEGER,
  is_pr INTEGER, state TEXT, merged INTEGER, merged_at TEXT, title TEXT,
  PRIMARY KEY(issue_number, ref_repo, ref_number)
);

changelog(                                         -- parsed CHANGELOG-v*.md bullets
  version TEXT, section TEXT,                      -- FEATURES / ENHANCEMENTS / BUG FIXES
  resource TEXT, text TEXT, pr_number INTEGER
);

signals(                                           -- output of `analyze`, one row per issue
  issue_number INTEGER PRIMARY KEY,
  kind TEXT,                                       -- bug|enhancement|question|crash|docs (from labels + AI later)
  template_version TEXT,                           -- "1.31.0" parsed from body
  version_major INTEGER,                           -- best-effort: label > template > body mentions
  resources TEXT,                                  -- JSON array of azurerm_* names
  service TEXT,                                    -- service/* label if present
  last_activity TEXT, maintainer_commented INTEGER,
  newest_claim_major INTEGER, newest_claim_at TEXT, -- highest provider major anyone claims repro on
  has_open_linked_pr INTEGER, has_merged_linked_pr INTEGER,
  multi_version_labels INTEGER,
  computed_at TEXT
);

ai_verdicts(                                       -- cache: rerun is free unless issue or prompt changed
  issue_number INTEGER, pass TEXT,                 -- classify|still_open|shipped|dupe
  prompt_hash TEXT, model TEXT,
  verdict TEXT,                                    -- raw JSON verdict
  confidence REAL, created_at TEXT,
  PRIMARY KEY(issue_number, pass, prompt_hash)
);

dupe_groups(id INTEGER PRIMARY KEY, canonical INTEGER, resource TEXT,
            confidence REAL, rationale TEXT);
dupe_members(group_id INTEGER, issue_number INTEGER, PRIMARY KEY(group_id, issue_number));

actions(                                           -- the audit trail / work queue
  id INTEGER PRIMARY KEY,
  issue_number INTEGER, action TEXT,               -- close|comment|label|keep
  reason TEXT,                                     -- reason code, see taxonomy
  template TEXT,                                   -- which comment template
  evidence TEXT,                                   -- JSON: quotes, changelog lines, dupe target…
  confidence REAL,
  status TEXT,                                     -- proposed|approved|rejected|applied|failed|stale
  proposed_at TEXT, decided_by TEXT, decided_at TEXT,
  issue_updated_at TEXT,                           -- snapshot at proposal time (staleness guard)
  applied_at TEXT, error TEXT
);
```

## Fetch (GraphQL bulk, REST writes)

REST would need thousands of calls for comments; GraphQL gets everything nested:
~50 issues/page with `comments(first:50)`, `reactions` summary,
`timelineItems(itemTypes:[CROSS_REFERENCED_EVENT], first:20)` → roughly 70 pages for the
whole repo, minutes of wall time, well inside the 5,000-point/hr budget. Hand-rolled
`lib/ghql` over chttp — control the query shape, plain structs, no client dependency. Issues with >50 comments (rare) get a follow-up paginated comment fetch.

- **Resumable:** commit each page + its `endCursor` into `meta` in one transaction; a
  killed fetch continues where it stopped.
- **Incremental:** subsequent runs use GraphQL search `repo:… is:issue updated:>LAST_SYNC`
  (state:all, so we also learn about issues closed by others) and upsert. `fetch --full`
  forces a re-walk.
- **Also fetched:** all `CHANGELOG*.md` files, parsed into the `changelog` table; and the
  version→release-date index from the headings (`## 4.81.0 (July 14, 2026)`).
- Mutations stay on go-github/v89 REST (close with state_reason, comment, labels) — the
  library rjg/tctest already vendor and wrap.

## Analyze (deterministic, free, re-runnable)

Pure Go over the DB — no network, no AI. This should settle the *majority* of the backlog.

Per issue, compute `signals`:
1. **Version**: precedence = version label (`v/N.x`) > template block regex
   (`provider[\.\s"]azurerm[\s"]+v?(\d+)\.` etc.) > body/comment mentions
   (`azurerm ~> 2`, `provider version 2.57`, `required_providers` blocks).
2. **Resources**: `azurerm_[a-z0-9_]+` extraction from Affected Resource(s) + title;
   `service/*` label.
3. **"Still an issue on newer" scan**: regex comment sweep for provider-major mentions;
   record the *highest* major claimed with a repro-ish context and when. (AI later
   confirms semantics for close candidates only — someone quoting "v1.27" in a fix
   discussion must not count as a v1 repro claim, and "still broken in 4.20" must count.)
4. **Cross-refs**: does a merged PR reference this issue? An open PR? (open PR ⇒ KEEP.)
5. **Engagement**: 👍 count, comment count, distinct participants, maintainer involvement,
   last activity age, multiple version labels.

Then the **rules engine** proposes actions (each rule emits reason + evidence; AI passes
can only add or upgrade, never silently delete a KEEP):

| Rule | Proposal |
|---|---|
| bug + version_major ≤ 3 + no claim ≥ 4 + no open linked PR | `close/legacy-bug` |
| bug + merged linked PR (or changelog BUG FIXES bullet matching resource) | `close/fixed` (as completed, cite PR/version) |
| any + claim ≥ 4 in comments | `keep/confirmed-recent` (pinned — excluded from all close queues) |
| any + open linked PR | `keep/has-pr` |
| enhancement + changelog FEATURES/ENHANCEMENTS candidate match | → `shipped` AI pass |
| enhancement + shares resource bucket with other FRs | → `dupes` AI pass |
| `waiting-response` + no author reply > 90d | `close/no-response` |
| `question` + last activity > 12mo | `close/stale-question` |
| resource deprecated/removed in 4.0/5.0 (list from upgrade guides) | `close/deprecated-resource` (point at upgrade guide) |
| retired Azure services (azure/germany label, classic/ACS/etc.) | `close/retired-service` |
| `upstream/terraform` label + stale | `close/upstream` (redirect to core repo) |
| thumbs_up ≥ threshold (e.g. 20) | never auto-proposed for close; `keep/high-engagement`, needs explicit human override |
| everything undetermined | → `classify` AI pass |

`koi stats` prints the funnel after every stage: how many issues in each bucket, how many
proposals by reason, how many awaiting AI / review / apply.

## AI passes (advice, cached, never autonomous)

Same mechanism as rjg: shell out to the already-authenticated claude CLI
(`-p --output-format json`, prompt on stdin, `ExtractJSON` tolerant parsing), model
selectable via `--ai-model`, failures non-fatal, timeout per call. Two upgrades over rjg:

1. **Batched prompts** — classification-type passes send 10–20 issues per invocation
   (numbered, bodies truncated to ~1500 runes, `cleanBody()` strips the Community Note
   boilerplate) and expect a JSON array back keyed by issue number. 3,491 issues would be
   miserable one-at-a-time; batched it's a few hundred invocations, and only the
   rules-undetermined subset needs it anyway.
2. **Verdicts persist** — every verdict lands in `ai_verdicts` keyed by
   `(issue, pass, prompt_hash)` where prompt_hash covers the template + issue
   `updated_at`. Re-running a pass costs nothing unless the issue changed or the prompt
   was edited. (rjg printed and discarded; at this scale that's wasteful.)

The `lib/ai` interface stays narrow so a second backend using `anthropic-sdk-go` +
the Message Batches API (50% price, perfect for a 2k-issue overnight pass; default model
`claude-opus-5`) can be added later without touching callers. Not needed for v1.

Passes:
- **classify** — for rules-undetermined issues: `{number, kind, version_major_evidence,
  still_relevant: bool, recommend: close/<reason>|keep/<reason>|human, confidence, quote}`.
  Prompt carries the anti-hallucination clauses that worked in rjg ("if the evidence is
  thin, lower the confidence instead of guessing"; "a version string being quoted is not
  evidence the bug reproduces on that version").
- **still_open** — runs over every `close/*` proposal that has *any* comment activity:
  given the last N comments, "does anyone credibly claim this still occurs on provider
  4.x/5.x?" A yes flips the proposal to `keep/confirmed-recent`. This is the safety net
  for the user's "1.x bug but confirmed on 4.z" requirement.
- **shipped** — per FR: candidate changelog bullets (same resource, release ≥ issue
  creation) + merged crossref PR titles → `{shipped: bool, version, pr, confidence}`.
  Close as completed citing the release.
- **dupes** — within each resource/service bucket, cluster FR titles+summaries →
  groups with a canonical pick. Canonical = open + max(👍, comment activity), tie → oldest.
  Members get `close/duplicate` pointing at the canonical; canonical gets `keep`.

## Review — the "assisted" part, for two different users

**kt (terminal):** `koi review [--reason X] [--min-confidence N]` — rjg-style interactive
queue, ordered oldest-first. Per proposal: coloured card (title, age, version evidence,
reaction count, AI quote + confidence, proposed template) then
`[y]approve [n]reject [e]dit reason [o]pen browser [s]kip [q]uit`. Decisions write
`actions.status` + `decided_by`. Bulk mode: `koi review --approve-all --reason legacy-bug
--min-confidence 0.9` for the mechanical slices after spot-checking.

**Community manager (no terminal required):**
- `koi report` → static HTML (self-contained, grouped by reason, sortable, every issue a
  link, evidence inline) + a CSV (`number,reason,confidence,decision,notes`).
- CM reviews in browser/spreadsheet, fills the decision column.
- `koi import decisions.csv` records their approvals/rejections (`decided_by` from
  `--as <name>`).

Close-comment templates live in `templates/` (embedded, one per reason code), reviewed by
the CM before the first wave. Tone: thanks, why it's being closed, what to do if it still
matters ("please open a new issue including details for the current provider version and
reference this one" — commenters can't reopen closed issues themselves, so don't ask them
to).

## Apply — throttled, guarded, auditable

`koi apply [--reason X] [--max N]`:
- Only `status=approved` actions. `--dry-run` prints exactly what would happen (default
  off but the first run should use it; `--max` defaults to something conservative, ~100).
- **Staleness guard:** before mutating, re-fetch the issue (cheap single REST GET). If
  `updated_at` moved past the snapshot in `actions.issue_updated_at`, mark the action
  `stale` and skip — new activity means a human re-look, not a robo-close.
- Sequence per issue: post comment (from template, with evidence substitutions) → apply
  labels (e.g. keep the existing legacy label, optionally add a `triage/closed-legacy`
  marker) → close with the right `state_reason` (`not_planned` for legacy/stale/dupe,
  `completed` for shipped/fixed).
- Throttle ~2s between mutations (GitHub secondary-rate-limit safe), retry transport
  underneath, every result (or error) recorded on the action row.
- **Waves:** deliberately cap daily volume (`--max`) — closing ~2,000 issues means
  thousands of notification emails and some upset users; 100–200/day over a few weeks is
  the sane cadence and leaves room to react to pushback.
- `koi reopen <n>` exists for mistakes (reopen + apologise comment), reading the audit row.

## Suggested execution order

1. **Phase 1 — skeleton + fetch + stats.** tctest scaffolding, db package, GraphQL fetch,
   `stats`. Validates the data model against reality.
2. **Phase 2 — analyze + review + apply.** Rules engine, interactive review, throttled
   apply with the staleness guard. This alone unlocks the biggest slice: ~960
   label-confirmed legacy issues plus whatever template parsing adds, no AI required.
   First wave: the 9 `v/1.x` + 247 `v/2.x` bugs.
3. **Phase 3 — AI classify + still_open.** Safety net before closing anything
   template-parsed rather than label-confirmed; unlocks the unlabeled ~1,950.
4. **Phase 4 — shipped + dupes** over the 1,454 FRs.
5. **Phase 5 — report/import** for the community manager, then run the FR waves.

By-product worth keeping: after the dust settles, `signals` + reactions give a ranked
"what the community actually wants" list for the FRs that survive — useful roadmap input,
free.

## Open questions / assumptions

- Binary/module name assumed `koi` under `github.com/katbyte/` — trivial to change.
- v/3.x policy: user said "bugs for 3.0 2.0 and 1.0 should just be closed" — 3.x bugs are
  705-strong and only ~2 majors old; plan treats them the same as 1.x/2.x per instruction,
  but the `--reason` filter means each major can be its own wave if 3.x deserves a gentler
  pass (e.g. require the still_open AI check for 3.x, label-only trust for 1.x/2.x).
- Deprecated/removed-resource list for the 4.0/5.0 upgrade-guide rule needs to be sourced
  from the website docs — could be a small embedded list maintained by hand, or scraped.
- Whether the CM wants HTML+CSV or would rather sit on a call and drive `koi review`
  together — the CSV import path is cheap insurance either way.

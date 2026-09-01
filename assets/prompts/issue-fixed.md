You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below has shipped-fix evidence: MERGED same-repository pull requests that reference it, and/or (for bug reports) uncited BUG FIXES changelog bullets — fixes on the issue's resources that shipped after the report, where no pull request ever cited the issue, so nobody connected them. You are given the issue's title, body, the version it reported against when known, its thread, and every piece of evidence: each referenced PR with whether the reference carried a closing keyword ("fixes #N"), the release that shipped it, and its title and body; each bullet with the release it shipped in, ranked by how much substance it shares with the report (MATCHED means the best bullet names the report's own property or symptom; RESOURCE-ONLY means later fixes merely touched the resource).

For each issue judge how likely a shipped change actually fixed THAT issue — that it could reasonably be closed as completed today, citing the best PR or bullet. Weigh all the evidence together and name what convinced you:

- Compare substance, not proximity. A PR that mentions the issue in passing, references it as related context, or touches the same resource for a different problem is NOT a fix. Likewise most bullets on a busy resource have nothing to do with any given report — the property names, the direction of the failure, and the operation involved (create, update, import, delete) must line up. RESOURCE-ONLY candidates should rarely score high.
- The thread is evidence. A "still happening" claim, a re-reproduction, or fresh error output dated AFTER the fix shipped means the fix did not cover this case; "this was fixed by #X" or "works since vX" supports the close.
- When an issue was closed by a PR and then REOPENED (noted below the evidence), treat that as strong evidence the fix was incomplete or regressed, and score low unless a later PR or bullet clearly finished the job.
- Use the reported version. A plausible bullet that shipped at or before the version the issue reported against cannot be the answer — the reporter hit the bug with that fix already in. When the reported version is unknown and the plausible fix is old, weigh the issue's open date against the release timeline.
- A fix for a narrower or adjacent case is not this fix: the report is about all subnets, the bullet fixes one property's validation; the report is a perpetual diff, the PR fixes a crash on the same field.
- Watch for wrong references: a PR body citing the issue number may be linking a different repository's item or an unrelated change.
- When several PRs or bullets are listed, score the best one and name it in the reason.

Be conservative: a wrong "this was fixed" close tells a reporter with a live bug that nobody read their report. When the connection is unclear, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming the PR or bullet that fixes it (or why none do)>"}]

Issues:

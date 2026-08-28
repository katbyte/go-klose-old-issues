You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below is open and has one or more same-repository pull requests referencing it. You are given the issue's title and body, and for every referenced PR: its state (merged — with the release that shipped it when known — open, or closed without merging), whether the reference carried a closing keyword ("fixes #N"), and the PR's title and body where available.

For each issue judge how likely the referenced PR(s) actually address THAT issue's problem or feature request — compare the substance of what the issue asks against what the PR changes. The PR's state does not change the score: a merged PR that fixes the issue and an open PR that would fix it both score high; a PR that merely mentions the issue in passing, references it as related context, or touches the same resource for a different problem scores low. When several PRs are listed, score the best match and name it in the reason. Watch for wrong references: a PR body citing the issue number may be linking a different repository's item or an unrelated change. Be conservative: when the connection is unclear, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming the PR>"}]

Issues:

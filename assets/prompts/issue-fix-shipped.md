You are checking OPEN GitHub issues for the terraform-provider-azurerm provider that show evidence a change addressing them ALREADY SHIPPED. Each numbered issue below is still open, with its title and body, and for every piece of evidence: the changelog bullet from the release that shipped it, plus the referenced PR's title and body where available.

For each issue judge how likely the shipped change fully resolves it — that is, the issue could reasonably be closed as completed today. Compare the substance of the issue's problem or request against what the PR/bullet actually shipped. Keep in mind these issues are OPEN for a reason: some were reopened because the fix regressed or was partial, some ask for more than the shipped slice delivered, and some are only tangentially mentioned by the PR — read the issue body for scope the change doesn't cover. Watch for number collisions: a bullet "citing" the issue number may really point at a different repository or an unrelated change sharing the number — the link URL inside the bullet is the tell. Be conservative: an issue should only score high when the shipped change clearly covers what was asked.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence>"}]

Issues:

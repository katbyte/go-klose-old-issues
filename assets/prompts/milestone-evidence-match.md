You are auditing milestone assignments for the terraform-provider-azurerm provider. Each numbered issue below is a CLOSED GitHub issue paired with the changelog evidence used to determine the release that supposedly resolved it. For each issue you are given its title and body, and for every piece of evidence: the changelog bullet from the citing release, plus the referenced PR's title and body where available.

For each issue judge how likely the evidence actually describes a change that resolves or implements THAT issue, comparing the substance of what each describes — the problem or request in the issue against the change the PR/bullet ships. Watch for number collisions: a bullet "citing" the issue number may really point at a different repository's issue or PR (e.g. hashicorp/terraform#123) or an unrelated change that merely shares the number — the link URL inside the bullet is the tell. A PR that merely mentions the issue, or touches the same resource for a different problem, is NOT a match. An issue asking for a feature matched with a PR shipping exactly that feature IS a match, and a PR whose body says "fixes/closes" this issue and whose change matches the issue's substance is a strong match. Be conservative: when the connection is unclear, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence>"}]

Issues:

You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below is a bug or crash report whose resource(s) received BUG FIXES changelog bullets AFTER the issue was reported — and no pull request ever cited the issue, so nobody connected the fix to it. You are given the issue's title, body, the provider version it reported against, the candidate bullets with the release each shipped in (ranked by how much substance they share with the report, best first), and a thread digest. MATCHED means the best bullet names the same property or symptom the report does; RESOURCE-ONLY means later fixes touched the resource but nothing lines up textually.

For each issue judge how likely one of the bullets describes a fix for THIS bug — the same property, the same symptom, the same failure — shipped in a release after the version the issue reported against, so closing as completed citing that bullet is the right call.

Score HIGH when a bullet's description and the report line up on substance: the report says import misses attributes and a bullet fixes importing; the report says changing a property forces recreation and a bullet stops that property forcing replacement; the report quotes a crash and a bullet fixes that panic. The match must survive reading both carefully — property names, the direction of the failure, the operation involved (create, update, import, delete).

Score LOW when:

- The bullet touches the same resource but a different property, operation, or behaviour — most later fixes to a busy resource have nothing to do with any given report. RESOURCE-ONLY candidates should rarely score high.
- The thread contains a "still happening" claim, a re-reproduction, or fresh error output dated AFTER the bullet's release — the fix did not cover this case.
- The bullet fixes a narrower or adjacent case: the report is about all subnets, the bullet fixes one property's validation; the report is a perpetual diff, the bullet fixes a crash on the same field.
- The reported version is unknown or ambiguous and the plausible bullet shipped long ago — the reporter may have hit the bug with the fix already in; weigh the issue's open date against the release timeline.
- The report's substance is broader than any fix: multiple failure modes, a design problem, or behaviour the bullets do not speak to.

Be conservative: a wrong "this was fixed" close tells a reporter with a live bug that nobody read their report. When the substance is not clearly the same, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence: which bullet, and whether its substance really is this report's bug>"}]

Issues:

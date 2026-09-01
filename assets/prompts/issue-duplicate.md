You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below has one or more SIBLING issues in the same repository that may cover it — cross-referenced by somebody, or (when marked "NOT linked") paired purely on near-identical titles. A sibling may be CLOSED (this issue may duplicate something already dealt with) or STILL OPEN (this issue may duplicate a live discussion that should stay in one place). Each sibling's block says which it is and carries the evidence: closed siblings come with how and when they were closed, whether a fixing pull request and release are recorded, their body, and their comments split around the close; open siblings come with their body, comments, and engagement — the survivor carrying more of the discussion, weighted towards the older issue. A similarity-found pair has NO human vouching for the connection: judge it entirely on substance and hold it to a higher bar than a linked pair.

For each issue judge how likely closing it in favour of a sibling is the right call: the sibling's outcome already covers its substance (closed), or the sibling is the same ask and the discussion belongs in one place (open).

First, the substance must match — for either kind of sibling:

- Same substance, not same subject. Two issues about the same resource, or two titles both saying "not supported", are not duplicates unless they ask for the same thing or report the same failure.
- A reference is not a duplicate claim. "Related to #123", "blocked by #123", "similar to #123 but for the data source" link DIFFERENT issues.
- The bug report and the feature request that would prevent it are different issues; so are the same missing feature on two different resources.
- A narrower issue inside a broader one is a duplicate only when the broader one clearly covers it and is tracked that way. When the issue being closed is the specific, actionable one and the survivor is a vague catch-all, score LOW: closing the specific one loses information.

For CLOSED siblings, weigh the resolution evidence hard:

- A recorded fixing PR and release is genuine resolution: the same underlying problem scores HIGH when that fix clearly covers this report too.
- "Closed as completed" is only a label. No fixing PR or release recorded may mean a manual or bulk close where nothing shipped; do NOT treat it as resolved unless the bodies make the delivery clear.
- Use versions and dates together. The classic duplicate was filed AFTER the sibling closed, from a provider version OLDER than the fix release: HIGH when the substance matches. A report from a version AT OR PAST the fix release means the fix missed their case; a report filed after a fix-less close gained nothing from it; both LOW.
- The comments BEFORE the close reveal why it was really closed ("fixed in vX" is real; "closing as stale" is not); the comments AFTER are where people dispute it — "still happening" or a reopen request means the closure resolved nothing, score LOW. A "(N earlier comments not shown)" line means truncated, not silent.
- The open issue's own thread counts too: "fixed by #X" supports the duplicate, "still happening on vY" at or past the fix release refutes it.

For OPEN siblings, direction and thread health matter:

- A maintainer saying "let's track this in #123" is strong; someone on the sibling saying "that is a different problem" refutes it, as does the sibling having drifted to another topic.
- Say so in your reason if the wrong one is being closed: the issue kept must be the one with the fuller picture, the clearer reproduction, or the maintainer discussion — whichever is older.
- Reported against different provider versions is not by itself a difference, but a bug that was fixed and came back is not the same issue as the original.

Be conservative: a wrong duplicate close sends a reporter somewhere that does not answer them. When the connection, the substance, or the resolution is unclear, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming the sibling and what makes this the same ask (and, for closed ones, whether the resolution is real)>"}]

Issues:

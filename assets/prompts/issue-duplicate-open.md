You are triaging OPEN issues for the terraform-provider-azurerm provider. Each numbered issue below appears to duplicate an OLDER issue that is also still open: either this issue references that one, or nobody linked them and the two titles say nearly the same thing. You are given both issues in full: title, body, dates, engagement and recent comments.

For each issue judge how likely it is the SAME underlying request or bug as the other issue, so that closing it and pointing people at that one is the right call. The issue named as the survivor is the one carrying more of the discussion (reactions and comments, weighted towards the older issue), which is usually but not always the older one.

- Same substance, not same subject. Two issues about `azurerm_kubernetes_cluster`, or two issues whose titles both say "not supported", are not duplicates unless they ask for the same thing or report the same failure. Score LOW when they merely share an area.
- A reference is not a duplicate claim. "Related to #123", "see also #123", "this is blocked by #123" and "similar to #123 but for the data source" are all links between DIFFERENT issues. Only score HIGH when the substance matches.
- The bug report and the feature request that would prevent it are different issues, as are a bug and its workaround discussion. So are the same missing feature on two different resources.
- A narrower issue inside a broader one is a duplicate only when the broader issue clearly covers it and is being tracked that way. If the older issue is a vague catch-all and this one is specific and actionable, score LOW: closing the specific one loses information.
- Read the comments on both. A maintainer saying "let's track this in #123" is strong. Someone on the older issue saying "that is a different problem" refutes it, as does the older issue having drifted to a different topic.
- Direction matters. Say so in your reason if the wrong one is being closed: the issue kept must be the one with the fuller picture, the clearer reproduction, or the maintainer discussion. Score LOW when the issue about to be closed is the specific, actionable one and the survivor is the vague catch-all, whichever is older.
- Reported against different provider versions is not by itself a difference, but a bug that was fixed and came back is not the same issue as the original.

Be conservative: closing a live issue as a duplicate sends its reporter somewhere else, so when the two are only plausibly the same, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming the older issue and what makes them the same or different>"}]

Issues:

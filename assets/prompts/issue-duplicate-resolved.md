You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below is open but cross-references one or more CLOSED issues in the same repository, so it may be a duplicate of something already dealt with. You are given the open issue's title, body, dates, and recent comments, and for every closed linked issue: how and when it was closed, whether a fixing pull request and release are recorded, its title and body, and its comments split around the close. The comments BEFORE the close reveal why it was really closed: "fixed in vX" or a maintainer pointing at the shipped change is real resolution; "closing as stale" or silence is not, whatever the close label says. The comments AFTER the close are where people dispute it: "this is not fixed", "still happening", or a request to reopen means the closure resolved nothing, and anything leaning on that closure must score LOW. A "(N earlier comments not shown)" line means the discussion was truncated, not silent — judge only from what you can see.

For each open issue judge how likely it is a duplicate whose substance the linked issue's outcome already covers, so that closing it and pointing at the linked issue is the right call. Compare what each issue actually describes, and weigh the resolution evidence hard:

- A linked issue with a recorded fixing PR and release is genuinely resolved: the same underlying problem or request scores HIGH when that fix clearly covers this report too.
- "Closed as completed" is only a label. A linked issue closed with NO fixing PR or release recorded may have been closed manually or in a bulk cleanup without anything shipping; do NOT treat it as resolved unless the issue bodies themselves make it clear the thing was actually delivered. Score LOW otherwise.
- Use the versions and the dates together. The classic true duplicate was filed AFTER the linked issue was closed, by someone running a provider version OLDER than the release that shipped the fix (issue bodies usually state the reporter's version): that scores HIGH when the substance matches. But a report from a version AT OR PAST the fix release means the fix did not cover their case, and a report filed after a close with NO fix recorded gained nothing from that closure; both score LOW.
- Read the open issue's own comments. "This was fixed by #X" or "works since vX" supports the duplicate; "still happening on vY" at or past the fix release refutes it.
- Score LOW when the issues are merely related (same resource, different problem), when the open issue asks for more than the linked one delivered, when the linked issue was closed as stale or not planned, or when the reference is incidental.

Cross-references alone are not evidence of a duplicate; the substance must match and the resolution must be real. Be conservative: when the connection or the resolution is unclear, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming the linked issue and the evidence>"}]

Issues:

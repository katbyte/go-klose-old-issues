You are re-reviewing GitHub issues for the terraform-provider-azurerm provider that were closed — most by an automated triage tool with a human supervising, some by hand by the maintainer. Each numbered issue below was closed with a comment citing evidence — a shipped fix, a resolving sibling issue, a claim in the thread, a removed/deprecated resource, a legacy-only bug — and has since received one or more NEW comments. You are given the issue's title and body, why it was closed (the reason code, recorded evidence, and reasoning at close time for tool closes; the maintainer's own closing comment for by-hand closes), and every comment left after the close with its author's standing (MEMBER/COLLABORATOR are maintainers) and date.

For each issue judge how likely the new comments genuinely dispute the close, so that REOPENING the issue is the right call. Weigh them like a maintainer would:

- The issue's own AUTHOR saying the close was wrong — "this is not fixed", "still hitting this on the latest version", "the linked issue is a different problem" — is strong; a fresh reproduction with versions, config, or output is stronger still.
- Anyone reporting the problem still occurs on a version AT OR ABOVE the one the fix shipped in disputes a close-as-fixed; "still broken" on a version older than the fix does not.
- A maintainer saying the close was premature or wrong is decisive; score HIGH.
- Thanks, agreement, "makes sense", confirmations the fix works, and +1 reactions in prose support the close; score LOW.
- A release-bot note ("This functionality has been released in vX.Y.Z...") or lock notice is not a dispute; score LOW.
- A NEW or different problem raised in the comments belongs in a new issue, not a reopen; score LOW — the dispute must be about the closed issue's own substance.
- Questions are not disputes: "which version has the fix?" or "what should I use instead?" scores LOW, unless the exchange reveals the close reasoning was wrong.
- For an issue closed as resolved by a sibling or duplicate, a comment showing the sibling does NOT cover this case disputes the close.
- For an issue closed as deprecated/removed or legacy-only, a comment showing it also affects current, supported functionality disputes the close.
- Disappointment about a not-planned close, or re-asking for the feature, is not a dispute unless it brings substance the close reasoning did not consider.

Be conservative both ways: score HIGH only when a maintainer reading the thread would actually reopen, and LOW when the comments are noise, gratitude, or a different problem.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming who disputed what and whether it warrants reopening>"}]

Issues:

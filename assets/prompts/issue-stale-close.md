You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below is a thread that ended on a MAINTAINER'S comment, unanswered by anyone for over a year, classed by the shape of that last word. ASKED: the maintainer requested something back — more information, a reproduction, confirmation, an up-to-date config — and it never came. SAID: the maintainer stated a position — this is by design, an Azure API limitation, out of scope, belongs upstream in Terraform Core or another repository, superseded by something else — and nobody disputed it since. You are given the issue's title, body, dates, the maintainer's last word in full, and a thread digest.

For each issue judge how likely the conversation is genuinely over — the maintainer's last word settled it or left the ball with a reporter who never returned — so closing as not planned citing that comment is the right call.

Score HIGH when:

- ASKED: the request was directed at the reporter (or anyone able to move the issue forward), what was asked for was genuinely needed to proceed, and it never arrived — nothing actionable remains.
- SAID: the last word concluded the issue — by design, cannot fix (API limitation), out of scope, belongs upstream, superseded — and the silence since reads as acceptance.

Score LOW when:

- The last word is a COMMITMENT or status update: "I'll look into this", "we plan to support this", "adding to the roadmap", "a fix is in progress". The ball is with the maintainers, not the reporter — that is the opposite of closeable.
- The last word says the issue is FIXED, implemented, or resolved — that close belongs to a different pass (and should close as completed, not stale); score low here.
- ASKED: the question was rhetorical, aimed at other maintainers, or the requested information had actually already been provided earlier in the thread or the issue body.
- The last word is administrative noise (labelling notes, milestone shuffling, "linking related issues") that neither asked nor concluded anything.
- The issue's substance clearly stands regardless of the exchange — e.g. a well-reproduced bug where the maintainer asked a side question.

Be conservative: closing a thread the maintainers still owe an action on tells the reporter they were ignored. When the last word's intent is unclear, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence: what the maintainer's last word was and why the thread is or is not over>"}]

Issues:

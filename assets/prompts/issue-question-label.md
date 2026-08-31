You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below carries NO `question` label, but its title or body reads as a question, and koi proposes adding the `question` label. You are given the issue's title, body, dates, existing labels, and the question-shaped quotes the sweep found.

Quotes marked "weak" were swept loosely — a bare interrogative title opening or a stray "how to" — and are leads, not asks: weigh them only by what the full body shows.

For each issue judge how likely it genuinely IS a question: the author wants to know how to do something with what the provider already offers, whether something is possible or supported today, which configuration or approach is correct, or why their working setup behaves the way it does. Score HIGH when the body reads as an ask — "how do I reference the subnet from another module?", "is it possible to use a single key vault across subscriptions?", "what is the correct way to rotate these credentials?" — especially when there is no error output, no expected-vs-actual behaviour, and no request to change the provider.

Score LOW when the question shape is only phrasing:

- A bug report phrased interrogatively — "why does this crash?", "is this a bug?", "am I doing something wrong or is this broken?" backed by error output, panic traces, or expected-vs-actual behaviour. That issue is a report, not a question.
- A feature or enhancement request phrased as an ask — "is it possible to add support for X?", "could this resource expose Y?", "any way to get this new Azure feature?". Asking FOR something new is a request; asking how to use what exists is a question.
- The issue already carries a kind label (bug, crash, enhancement, documentation) that its content supports — the existing label is right, and question would sit beside it as noise. Only score high here when the content plainly contradicts that label and the issue is a pure usage question wearing the wrong one.
- The quote is incidental — a "how to" inside a pasted doc link, a template heading, or quoted text rather than the author's own ask.
- A documentation gap report — "the docs don't explain how to configure X" is asking for the docs to be fixed, not for an answer.

The label routes the issue into the question triage wave, where answered and abandoned questions get closed — a mislabelled bug report could be closed as an answered question. Be conservative: when the issue is as much report as ask, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence: what the author actually wants and whether that makes this a question>"}]

Issues:

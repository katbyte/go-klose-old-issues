You are triaging OPEN enhancement requests for the terraform-provider-azurerm provider. Each numbered request below asked for something that now appears to exist in the provider: either the requested resource or data source is in the provider today and arrived AFTER the request was filed (with the changelog bullet announcing it and a documentation link), or a property the request's prose names shipped for one of its resources in a later release. You are given the request's title, body, dates, and recent comments.

For each request judge how likely what shipped actually DELIVERS the specific ask, so that closing it as completed with the good news is the right call.

- The requested resource or data source now existing is strong: if the request was "support X" and `azurerm_x` arrived later, score HIGH. Read the request carefully though — if it asked for a specific capability and the shipped resource is only adjacent (a different service, a narrower scope), score LOW.
- Some evidence is docs-only: the thing is listed in the provider documentation today with no dated arrival, and may even have existed when the request was filed. Existence is what matters — a request for something that turns out to already exist scores HIGH once the docs listing clearly covers the ask.
- For properties, the shipped bullet must cover what was asked, not merely mention the same word: "support for `zone_redundant`" delivered means the ask for zone redundancy is done; a bullet that only fixes or validates that property is not delivery.
- Read the comments. "This doesn't cover my case", "still waiting for Y", or the thread narrowing the ask to something the shipped change lacks all score LOW. A commenter confirming "this exists now" scores HIGH.
- An ask that is broader than what shipped scores LOW: delivering one of three requested things is not delivery.
- Matching words are not matching substance; judge from what the request actually wants.

- Some issues are marked `KIND UNKNOWN`: nothing labels them and the rules could not tell what they are. Judge that first. Score 0 unless the issue is genuinely asking for something to be added or supported — a bug report about a broken resource, a support question or a discussion is not a request, however much its wording matches what shipped.

Be conservative: when it is unclear whether the shipped change covers the ask, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming what shipped and whether it delivers the ask>"}]

Issues:

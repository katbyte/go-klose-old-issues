You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below is a bug or crash report quoting error or panic output, and the distinctive fragments of that output NO LONGER EXIST anywhere in the provider's current source code (vendored SDKs included) — the code that produced them has been rewritten or removed since the report. Each searched fragment is listed with its verification status: VERIFIED means the fragment DID exist in the provider source at the version the issue reported against, so it was provider output then and is gone now; PANIC FUNCTION means a provider function named in the panic stack no longer exists; UNVERIFIED means the fragment is absent today but the reported version could not be checked, so it may never have been provider output at all. You are given the issue's title, body, dates, reported version, and recent comments.

For each issue judge how likely the report is obsolete as written — the failing code path has been rewritten or removed — so that closing it as not planned with an invitation to re-test on the current provider is the right call. Score HIGH when the issue's substance is the quoted error or crash itself, the fragments read like provider-generated wording (resource operations, expand/flatten steps, validation messages) or are a gone panic function, and nothing in the thread claims the problem still occurs on a recent major (v{{RECENT_MAJORS}}).

Score LOW when:

- The quoted text reads like it came from somewhere other than the provider: Azure API response bodies (Code=..., status codes, request IDs, service-side messages), Terraform core output, or wrapper tooling. Its absence from the provider source means nothing about the bug.
- A comment credibly claims the problem still occurs on a recent major — the message may simply have been reworded while the behaviour survived.
- The issue's substance is broader than the quoted error: the error is one symptom of a design problem, missing feature, or wrong behaviour that a rewrite does not settle.
- The fragment is generic phrasing that any rewrite would reword ("waiting for the operation", "an unexpected error occurred") rather than text tied to the failing code path.
- Every fragment is UNVERIFIED and short.

Be conservative: rewritten code does not guarantee fixed behaviour — the close is only right when the report is unactionable as written and nobody says it is still alive. When it is unclear whether the quoted output was the provider's, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming the gone fragment and why the report is or is not obsolete>"}]

Issues:

You are triaging OPEN bug and crash reports for the terraform-provider-azurerm provider (current major: {{CURRENT_MAJOR}}). Each numbered issue below was reported against a legacy major version (v{{LEGACY_MAX}}.x or older) and is a candidate for closing as stale: the provider has moved on and old-major bug reports are usually no longer actionable. For each issue you are given its title and body, the parsed version evidence, and a digest of its comments with author, role, and date.

For each issue judge how likely closing it as a stale legacy bug is the right call. The deciding question is what the comments say about recent majors ({{RECENT_MAJORS}}):

- A comment credibly claiming the problem STILL OCCURS on {{RECENT_MAJORS}} (an actual report of the behaviour, not a question or a "+1") means the issue must stay open: score LOW.
- A comment saying the problem NO LONGER occurs on {{RECENT_MAJORS}}, or that it works after upgrading, is strong support for closing: score HIGH.
- No comment mentioning a recent major at all: score HIGH when the report clearly targets the old major, discussion died out long ago, and the affected behaviour has likely been reworked since.

Also score LOW when the bug is clearly version-independent and likely still present in current code, when a fix is actively in flight, or when the report is really an enhancement or question mislabelled as a bug. Recent substantive discussion is a reason to be cautious. Base the judgement only on the text given, and quote-worthy evidence should be reflected in the reason.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence>"}]

Issues:

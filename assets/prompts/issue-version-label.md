You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below has evidence naming provider major versions its v/N.x labels do not yet record, and koi proposes adding those labels. The evidence per proposed label is shown: the version the issue itself reports (parsed from the template block or body) and comment quotes mentioning a version. You are given the issue's title, body, dates, existing version labels, and every quote.

Quotes marked "bare mention" were swept with NO context requirement — the sweep finds every version-shaped token and leaves the analysis entirely to you. A bare mention is a lead, not a claim: count it only when its surrounding text shows the commenter runs, reproduces, or observes THIS issue on that AzureRM provider version. A module version from a pasted configuration, a Kubernetes or API version, a dependency list, or a number that merely looks like a version is nothing.

For each issue judge how likely ALL of its proposed labels genuinely record affected versions — versions the problem was reported or confirmed to occur on. Score HIGH when each proposed major is backed by a quote that reports or confirms the issue on that AzureRM provider version: the template's version block, "still happening on 3.117", "confirmed with azurerm 2.41", "reproduced on v4.2".

Score LOW when any proposed label's evidence is not an affected-version claim:

- The version is merely quoted in a fix discussion — "fixed in 3.71", "this shipped in v4.0", "will be addressed in the next major". A fix version is not an affected version.
- The version is Terraform Core's, the plugin SDK's, an Azure API version, or another provider's — only AzureRM provider versions belong on v/N.x labels.
- The quote asks for something rather than reporting something: "please support this in v4", "any plans for 5.0?", "will this work on 3.x?".
- The parse grabbed the wrong number: a version inside a resource ID, a date, a Kubernetes or API version string quoted in configuration or error output.
- The quote's claim is negated or second-hand: "does NOT happen on 4.2", "someone said it broke in v2".

One bad label poisons the set — score the issue by its weakest proposed label. Be conservative: labels guide future triage waves, and a wrong affected-version label sends an issue to the wrong wave.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence: which labels and whether their evidence really claims those versions are affected>"}]

Issues:

You are helping triage very old GitHub issues on the terraform-provider-azurerm repository. The provider is currently on major version {{CURRENT_MAJOR}}; majors 1 through {{LEGACY_MAX}} are unsupported legacy versions. For each numbered issue below, determine:

1. "kind": one of "bug", "enhancement", "question", "crash", "documentation", or "unknown".
2. "version_major": the AzureRM *provider* major version the issue was reported against (integer; 0 when it cannot be determined). CRITICAL: do not confuse Terraform core versions (0.x, 1.x — e.g. "Terraform v1.5.7") with AzureRM provider versions; only report the provider's version. A version string merely being quoted or referenced (in fix discussions, upgrade instructions, or changelogs) is NOT evidence the issue was reported against that version.
3. "still_relevant": true only when a comment credibly claims the problem still occurs on provider major {{RECENT_MAJORS}} — someone actually reporting the behaviour, not asking about it, quoting a version, or speculating.
4. "recommendation": exactly one of:
   - "close-legacy" — a bug or crash clearly reported against provider major 1-{{LEGACY_MAX}}, with no credible claim it persists on {{RECENT_MAJORS}}
   - "keep" — credible evidence it is still relevant on a recent major, or a clearly still-active discussion
   - "unknown" — cannot decide with reasonable confidence
5. "confidence": 0.0-1.0. If the evidence is thin, lower the confidence instead of guessing.
6. "quote": the single most decisive short quote (under 200 characters) from the issue or its comments supporting the recommendation, or "" when there is none. Only quote text that actually appears in the input.

Respond with ONLY a JSON array containing one object per issue, in this exact shape:
[{"number": 123, "kind": "bug", "version_major": 2, "still_relevant": false, "recommendation": "close-legacy", "confidence": 0.9, "quote": "..."}]

Issues:

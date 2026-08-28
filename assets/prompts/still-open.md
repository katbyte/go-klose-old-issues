You are double-checking GitHub issues that are candidates for closure as "reported against a legacy version" of the terraform-provider-azurerm provider (current major: {{CURRENT_MAJOR}}). For each numbered issue below you are given its recent comments. Answer ONE question per issue: does any comment credibly claim the problem STILL occurs on provider major {{RECENT_MAJORS}}?

Credible claims are users actually reporting the behaviour: "still happening on 4.20", "just hit this with azurerm 5.1", "confirmed on v4.8". NOT credible: questions ("does this affect 4.x?"), version strings quoted in fix discussions or upgrade guides, mentions of Terraform core versions (e.g. "Terraform v1.5.7" is a core version, not a provider version), or "+1" / "any update?" comments that name no version.

If the evidence is thin, lower the confidence instead of guessing. Only quote text that actually appears in the input.

Respond with ONLY a JSON array containing one object per issue, in this exact shape:
[{"number": 123, "still_claim": false, "claimed_major": 0, "confidence": 0.9, "quote": ""}]

Issues:

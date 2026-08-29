Thank you for the report, and apologies for the slow follow up here.

This issue is linked to #{{ .PR }}{{ if .PRTitle }} ({{ .PRTitle }}){{ end }}, which merged{{ if .Version }} and shipped in v{{ .Version }}{{ end }} and appears to have fixed it, so we are closing this one out as resolved.

If you are still running into this on the current (v{{ .CurrentMajor }}.x) version of the provider, please let us know with a comment or open a new issue referencing this one and we will take another look.

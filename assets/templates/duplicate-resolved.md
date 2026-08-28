Thank you for the report. This issue covers the same ground as #{{ .Linked }}{{ if .LinkedTitle }} ({{ .LinkedTitle }}){{ end }}, which has since been {{ if .Resolved }}resolved{{ if .Version }} in v{{ .Version }}{{ end }}{{ else }}closed{{ end }}, so we are closing this one as a duplicate of it.

If you are still running into this on the current (v{{ .CurrentMajor }}.x) version of the provider, please let us know with a comment or open a new issue referencing this one and we will take another look.

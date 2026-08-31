Thank you for taking the time to report this issue, and apologies that it has sat without a resolution for so long.

{{ if .IsPanic }}The crash reported here occurred in provider code (`{{ .Fragment }}`) that no longer exists in the current provider{{ else }}The error reported here (`{{ .Fragment }}`{{ if .Tag }}, which was produced by the provider source as of {{ .Tag }}{{ end }}) no longer exists in the current provider source{{ end }} — the relevant code has since been rewritten, so this report is no longer actionable as written.

As part of a cleanup of historical issues we are closing reports whose failing code paths have been rewritten. If you still run into this behaviour on the current major version (v{{ .CurrentMajor }}.x), please open a new issue with up-to-date reproduction details (feel free to reference this one) so it can be triaged with current context.

Thank you again for the report, and for your patience.

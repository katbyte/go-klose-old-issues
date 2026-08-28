Thank you for taking the time to report this issue, and apologies that it has sat without a resolution for so long.

This bug was reported against {{ if .VersionFull }}v{{ .VersionFull }}{{ else }}an early major version{{ end }} of the AzureRM provider. The provider has since moved on to v{{ .CurrentMajor }}.x, and the affected functionality has often been significantly reworked across those major releases, so bug reports from legacy major versions prior to v{{ .PreviousMajor }}.0 are generally no longer actionable as written.

As part of a cleanup of historical issues we are closing bug reports that target legacy provider versions. If you are still running into this behaviour on the current major version, please open a new issue with up-to-date reproduction details (feel free to reference this one) so it can be triaged with current context.

Thank you again for the report, and for your patience.

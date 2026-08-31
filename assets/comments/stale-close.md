{{ if .Asked }}Additional information was requested [by @{{ .Author }}]({{ .URL }}) to move this issue forward, but we haven't heard back in quite some time, so as part of a cleanup of historical issues we are closing it due to inactivity.

If this is still relevant on the current version of the provider (v{{ .CurrentMajor }}.x), please open a new issue including the requested details (feel free to reference this one) and we will take another look. Thank you!{{ else }}This thread was left [with @{{ .Author }}'s conclusion]({{ .URL }}) and there has been no further activity in quite some time, so as part of a cleanup of historical issues we are closing it out.

If circumstances have changed and this affects you on the current version of the provider (v{{ .CurrentMajor }}.x), please open a new issue with fresh details referencing this one. Thank you!{{ end }}

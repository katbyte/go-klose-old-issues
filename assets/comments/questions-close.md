Thank you for asking this question!

{{ if .Answered }}It looks like this was answered by @{{ .Author }} [in the thread]({{ .URL }}), so as part of a cleanup of historical issues we are going to close this out as answered.{{ else }}As part of a cleanup of historical issues we are closing this question due to a long period of inactivity. Apologies that it never received the answer it deserved.{{ end }}

For provider usage questions, the [HashiCorp Discuss forum](https://discuss.hashicorp.com/c/terraform-providers/31) tends to be the best place to get help. If this has turned out to be a bug or a missing feature on the current version of the provider (v{{ .CurrentMajor }}.x), please open a new issue with fresh details referencing this one. Thank you!

// Package assets embeds the close-comment templates, AI prompt templates, and
// the HTML report templates so the binary is self-contained. Templates are plain
// files so the community manager can review and edit them before the first wave.
package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed comments/*.md prompts/*.md reports/*.tmpl
var files embed.FS

// CommentTemplate returns the close-comment template for a reason code.
func CommentTemplate(name string) (string, error) {
	b, err := files.ReadFile("comments/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("no comment template for reason %q: %w", name, err)
	}
	return string(b), nil
}

// CommentTemplateNames lists available close-comment template names (reason codes).
func CommentTemplateNames() []string {
	entries, err := fs.ReadDir(files, "comments")
	if err != nil {
		return nil // unreachable: embedded dir always exists
	}
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}

// Prompt returns an AI prompt template by name.
func Prompt(name string) (string, error) {
	b, err := files.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("no prompt template %q: %w", name, err)
	}
	return string(b), nil
}

// ReportHTML returns the close-candidates HTML report template.
func ReportHTML() string { return report("report.html.tmpl") }

// ActionsHTML returns the actions-taken HTML report template.
func ActionsHTML() string { return report("actions.html.tmpl") }

// Styles returns the css partials both HTML reports include.
func Styles() string { return report("styles.html.tmpl") }

func report(name string) string {
	b, err := files.ReadFile("reports/" + name)
	if err != nil {
		return "" // unreachable: embedded file always exists
	}
	return string(b)
}

// Package assets embeds the close-comment templates, AI prompt templates, and
// the HTML report template so the binary is self-contained. Templates are plain
// files so the community manager can review and edit them before the first wave.
package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed templates/*.md prompts/*.md report.html.tmpl
var files embed.FS

// CommentTemplate returns the close-comment template for a reason code.
func CommentTemplate(name string) (string, error) {
	b, err := files.ReadFile("templates/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("no comment template for reason %q: %w", name, err)
	}
	return string(b), nil
}

// CommentTemplateNames lists available close-comment template names (reason codes).
func CommentTemplateNames() []string {
	entries, err := fs.ReadDir(files, "templates")
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

// ReportHTML returns the HTML report template.
func ReportHTML() string {
	b, err := files.ReadFile("report.html.tmpl")
	if err != nil {
		return "" // unreachable: embedded file always exists
	}
	return string(b)
}

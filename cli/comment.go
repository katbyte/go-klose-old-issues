package cli

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/katbyte/go-klose-old-issues/assets"
	"github.com/katbyte/go-klose-old-issues/lib/db"
)

// commentData is what close-comment templates render with.
type commentData struct {
	Number        int
	Title         string
	CurrentMajor  int
	VersionMajor  int
	VersionFull   string
	MergedPR      int
	MergedPRTitle string
	Evidence      map[string]string
}

// renderCloseComment renders the close comment for an action.
func renderCloseComment(f *FlagData, i *db.Issue, s *db.Signals, a *db.Action) (string, error) {
	text, err := assets.CommentTemplate(a.Template)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New(a.Template).Parse(text)
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", a.Template, err)
	}

	data := commentData{
		Number:       i.Number,
		Title:        i.Title,
		CurrentMajor: f.CurrentMajor,
		Evidence:     a.Evidence,
	}
	if s != nil {
		data.VersionMajor = s.VersionMajor
		data.VersionFull = s.VersionFull
		data.MergedPR = s.MergedPRNumber
		data.MergedPRTitle = s.MergedPRTitle
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("rendering template %s: %w", a.Template, err)
	}
	return strings.TrimSpace(b.String()), nil
}

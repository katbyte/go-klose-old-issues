package db

import "fmt"

// Removal actions.
const (
	RemovalRemoved    = "removed"
	RemovalDeprecated = "deprecated"

	// Removal kinds: what the removed/deprecated thing is.
	RemovalKindResource   = "resource"
	RemovalKindDataSource = "data-source"
	RemovalKindProperty   = "property"
)

// Removal is one removed or deprecated resource, data source, or property,
// parsed from a major-version upgrade guide or a changelog DEPRECATIONS
// bullet. koi deprecated scans open issues against this inventory.
type Removal struct {
	Kind      string // resource | data-source | property
	Resource  string // the azurerm_* name it belongs to
	Property  string // "" for resource-level rows
	Action    string // removed | deprecated
	Major     int    // major that removed it (deprecations: major it was announced in)
	Successor string // what to use instead ("" = unknown)
	Note      string // the guide/changelog sentence, truncated
	Source    string // e.g. "4.0 upgrade guide" | "changelog v3.109.0"
}

// ReplaceRemovals replaces the whole removals table.
func (d *DB) ReplaceRemovals(rs []Removal) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("beginning removals replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM removals"); err != nil {
		return fmt.Errorf("clearing removals: %w", err)
	}
	for _, r := range rs {
		if _, err := tx.Exec(`
			INSERT INTO removals (kind, resource, property, action, major, successor, note, source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Kind, r.Resource, r.Property, r.Action, r.Major, r.Successor, r.Note, r.Source); err != nil {
			return fmt.Errorf("inserting removal %s/%s: %w", r.Resource, r.Property, err)
		}
	}
	return tx.Commit()
}

// Removals returns the whole removal inventory.
func (d *DB) Removals() ([]Removal, error) {
	rows, err := d.Query("SELECT kind, resource, property, action, major, successor, note, source FROM removals")
	if err != nil {
		return nil, fmt.Errorf("querying removals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Removal
	for rows.Next() {
		var r Removal
		if err := rows.Scan(&r.Kind, &r.Resource, &r.Property, &r.Action, &r.Major, &r.Successor, &r.Note, &r.Source); err != nil {
			return nil, fmt.Errorf("scanning removal: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChangelogSection returns every changelog entry in one section.
func (d *DB) ChangelogSection(section string) ([]ChangelogEntry, error) {
	rows, err := d.Query("SELECT version, major, section, resource, text, pr_number FROM changelog WHERE section = ?", section)
	if err != nil {
		return nil, fmt.Errorf("querying changelog section %s: %w", section, err)
	}
	defer func() { _ = rows.Close() }()

	var entries []ChangelogEntry
	for rows.Next() {
		var e ChangelogEntry
		if err := rows.Scan(&e.Version, &e.Major, &e.Section, &e.Resource, &e.Text, &e.PRNumber); err != nil {
			return nil, fmt.Errorf("scanning changelog entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

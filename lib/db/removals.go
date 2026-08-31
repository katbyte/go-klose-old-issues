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
// bullet. koi close deprecated scans open issues against this inventory.
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

// Doc kinds for provider_docs rows.
const (
	DocKindResource   = "resource"
	DocKindDataSource = "data-source"
)

// ReplaceProviderDocs replaces the what-exists-now inventory.
func (d *DB) ReplaceProviderDocs(byKind map[string][]string) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("beginning provider docs replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM provider_docs"); err != nil {
		return fmt.Errorf("clearing provider docs: %w", err)
	}
	for kind, names := range byKind {
		for _, n := range names {
			if _, err := tx.Exec("INSERT OR IGNORE INTO provider_docs (kind, name) VALUES (?, ?)", kind, n); err != nil {
				return fmt.Errorf("inserting provider doc %s/%s: %w", kind, n, err)
			}
		}
	}
	return tx.Commit()
}

// ProviderDocs returns the what-exists-now set, keyed "kind|name" — many
// names exist as both a resource and a data source, so the kinds must not
// collapse into one another.
func (d *DB) ProviderDocs() (map[string]bool, error) {
	rows, err := d.Query("SELECT kind, name FROM provider_docs")
	if err != nil {
		return nil, fmt.Errorf("querying provider docs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	docs := map[string]bool{}
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return nil, fmt.Errorf("scanning provider doc: %w", err)
		}
		docs[kind+"|"+name] = true
	}
	return docs, rows.Err()
}

// ChangelogLike returns every changelog entry whose text matches the LIKE
// pattern — how koi close exists finds the "New Resource"/"New Data Source" bullets.
func (d *DB) ChangelogLike(pattern string) ([]ChangelogEntry, error) {
	rows, err := d.Query("SELECT version, major, section, resource, text, pr_number FROM changelog WHERE text LIKE ?", pattern)
	if err != nil {
		return nil, fmt.Errorf("querying changelog like %q: %w", pattern, err)
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

// DocArg is one argument/attribute listed in a resource's documentation.
type DocArg struct {
	Kind string // resource | data-source
	Name string // azurerm_*
	Arg  string
}

// ReplaceDocArgs replaces the per-resource documented argument inventory.
func (d *DB) ReplaceDocArgs(args []DocArg) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("beginning doc args replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM provider_doc_args"); err != nil {
		return fmt.Errorf("clearing doc args: %w", err)
	}
	for _, a := range args {
		if _, err := tx.Exec("INSERT OR IGNORE INTO provider_doc_args (kind, name, arg) VALUES (?, ?, ?)", a.Kind, a.Name, a.Arg); err != nil {
			return fmt.Errorf("inserting doc arg %s/%s: %w", a.Name, a.Arg, err)
		}
	}
	return tx.Commit()
}

// DocArgs returns the documented arguments per "kind|name".
func (d *DB) DocArgs() (map[string]map[string]bool, error) {
	rows, err := d.Query("SELECT kind, name, arg FROM provider_doc_args")
	if err != nil {
		return nil, fmt.Errorf("querying doc args: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]bool{}
	for rows.Next() {
		var kind, name, arg string
		if err := rows.Scan(&kind, &name, &arg); err != nil {
			return nil, fmt.Errorf("scanning doc arg: %w", err)
		}
		key := kind + "|" + name
		if out[key] == nil {
			out[key] = map[string]bool{}
		}
		out[key][arg] = true
	}
	return out, rows.Err()
}

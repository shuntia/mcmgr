package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LockVersion is the schema version of mcmgr.lock.json.
const LockVersion = 1

// LockedMod is one add-on as actually installed on disk.
type LockedMod struct {
	// ID is the slug the user refers to the mod by.
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`

	ProjectID     string `json:"project_id"`
	VersionID     string `json:"version_id"`
	VersionNumber string `json:"version_number"`

	Filename string `json:"filename"`
	SHA1     string `json:"sha1,omitempty"`
	Size     int64  `json:"size,omitempty"`
	URL      string `json:"url,omitempty"`

	// Explicit marks mods the user asked for directly, as opposed to ones
	// pulled in to satisfy a dependency.
	Explicit bool `json:"explicit"`
	// RequiredBy lists the slugs of explicit mods that depend on this one.
	RequiredBy []string `json:"required_by,omitempty"`

	// ServerSide mirrors the provider's server compatibility flag, so an
	// add-on that cannot run on a server stays identifiable after install.
	ServerSide string `json:"server_side,omitempty"`

	GameVersions []string `json:"game_versions,omitempty"`
	InstalledAt  int64    `json:"installed_at,omitempty"`
}

// Lock is the contents of mcmgr.lock.json.
type Lock struct {
	LockVersion int         `json:"lock_version"`
	Minecraft   string      `json:"minecraft,omitempty"`
	Loader      Loader      `json:"loader,omitempty"`
	Mods        []LockedMod `json:"mods"`
}

// LockPath returns the lockfile path for a server.
func (c *Config) LockPath() string { return filepath.Join(c.dir, LockName) }

// LoadLock reads the lockfile, returning an empty lock if absent.
func (c *Config) LoadLock() (*Lock, error) {
	l := &Lock{LockVersion: LockVersion, Mods: []LockedMod{}}
	data, err := os.ReadFile(c.LockPath())
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", c.LockPath(), err)
	}
	if l.Mods == nil {
		l.Mods = []LockedMod{}
	}
	return l, nil
}

// SaveLock writes the lockfile, keeping entries sorted by id so diffs stay
// readable.
func (c *Config) SaveLock(l *Lock) error {
	l.LockVersion = LockVersion
	l.Minecraft = c.Minecraft
	l.Loader = c.Loader
	if l.Mods == nil {
		l.Mods = []LockedMod{}
	}
	sort.Slice(l.Mods, func(i, j int) bool { return l.Mods[i].ID < l.Mods[j].ID })
	return WriteJSON(c.LockPath(), l)
}

// Find returns a pointer to the locked mod with the given id or name, or nil.
func (l *Lock) Find(id string) *LockedMod {
	id = strings.ToLower(id)
	for i := range l.Mods {
		if strings.ToLower(l.Mods[i].ID) == id ||
			strings.ToLower(l.Mods[i].Name) == id ||
			strings.ToLower(l.Mods[i].ProjectID) == id {
			return &l.Mods[i]
		}
	}
	return nil
}

// Upsert replaces an existing entry with the same id, or appends a new one.
func (l *Lock) Upsert(m LockedMod) {
	for i := range l.Mods {
		if l.Mods[i].ID == m.ID {
			l.Mods[i] = m
			return
		}
	}
	l.Mods = append(l.Mods, m)
}

// Delete removes an entry by id and reports whether it was present.
func (l *Lock) Delete(id string) bool {
	for i := range l.Mods {
		if strings.EqualFold(l.Mods[i].ID, id) {
			l.Mods = append(l.Mods[:i], l.Mods[i+1:]...)
			return true
		}
	}
	return false
}

// Orphans returns dependency-only mods that nothing explicit still requires,
// after keep has been applied as the set of surviving explicit slugs.
func (l *Lock) Orphans(keep map[string]bool) []LockedMod {
	var out []LockedMod
	for _, m := range l.Mods {
		if m.Explicit {
			continue
		}
		needed := false
		for _, r := range m.RequiredBy {
			if keep[strings.ToLower(r)] {
				needed = true
				break
			}
		}
		if !needed {
			out = append(out, m)
		}
	}
	return out
}

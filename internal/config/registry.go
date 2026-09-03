package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mcmgr/internal/paths"
)

// RegistryName is the global index of known servers, so commands can be run
// from anywhere with --server <name>.
const RegistryName = "servers.json"

// Registry maps server names to their directories.
type Registry struct {
	Default string            `json:"default,omitempty"`
	Servers map[string]string `json:"servers"`
}

// RegistryPath returns the location of the registry file.
func RegistryPath() (string, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, RegistryName), nil
}

// LoadRegistry reads the registry, returning an empty one if absent.
func LoadRegistry() (*Registry, error) {
	p, err := RegistryPath()
	if err != nil {
		return nil, err
	}
	reg := &Registry{Servers: map[string]string{}}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return reg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, reg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	if reg.Servers == nil {
		reg.Servers = map[string]string{}
	}
	return reg, nil
}

// Save writes the registry back to disk.
func (r *Registry) Save() error {
	if _, err := paths.EnsureConfigDir(); err != nil {
		return err
	}
	p, err := RegistryPath()
	if err != nil {
		return err
	}
	return WriteJSON(p, r)
}

// Add registers a server directory under name, becoming the default if it is
// the first one known.
func (r *Registry) Add(name, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if existing, ok := r.Servers[name]; ok && existing != abs {
		return fmt.Errorf("a different server named %q is already registered at %s", name, existing)
	}
	r.Servers[name] = abs
	if r.Default == "" {
		r.Default = name
	}
	return r.Save()
}

// Remove drops a server from the registry, clearing the default if needed. It
// never touches the server's files.
func (r *Registry) Remove(name string) error {
	if _, ok := r.Servers[name]; !ok {
		return fmt.Errorf("no server named %q is registered", name)
	}
	delete(r.Servers, name)
	if r.Default == name {
		r.Default = ""
		for _, n := range r.Names() {
			r.Default = n
			break
		}
	}
	return r.Save()
}

// SetDefault marks a registered server as the one used when no other selection
// applies.
func (r *Registry) SetDefault(name string) error {
	if _, ok := r.Servers[name]; !ok {
		return fmt.Errorf("no server named %q is registered", name)
	}
	r.Default = name
	return r.Save()
}

// Names returns registered server names in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.Servers))
	for n := range r.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Prune drops entries whose directories no longer contain a manifest, and
// reports the names removed.
func (r *Registry) Prune() ([]string, error) {
	var gone []string
	for name, dir := range r.Servers {
		if !Exists(dir) {
			gone = append(gone, name)
			delete(r.Servers, name)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	if _, ok := r.Servers[r.Default]; !ok {
		r.Default = ""
		for _, n := range r.Names() {
			r.Default = n
			break
		}
	}
	sort.Strings(gone)
	return gone, r.Save()
}

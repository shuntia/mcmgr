package config

import (
	"encoding/json"
	"errors"
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

// Save writes the registry back to disk, replacing whatever is there.
//
// Prefer Add, Remove or SetDefault: those re-read the file under the lock, so
// a change made by another mcmgr in the meantime survives. This one is for the
// settings editor, which writes a registry the user has just seen whole.
func (r *Registry) Save() error {
	p, err := registryTarget()
	if err != nil {
		return err
	}
	return WriteJSONLocked(p, r)
}

// errUnchanged is returned by a change function that found nothing to do, so
// update adopts the fresh state without rewriting the file.
var errUnchanged = errors.New("registry unchanged")

// update applies change to a freshly read registry while holding its lock, and
// adopts the result as the receiver's own state.
//
// Re-reading matters because the caller's copy can be seconds old: two mcmgr
// invocations that each loaded the registry, added a server and saved would
// otherwise leave only the second one's server registered.
func (r *Registry) update(change func(*Registry) error) error {
	p, err := registryTarget()
	if err != nil {
		return err
	}
	return WithLock(p, func() error {
		fresh, err := LoadRegistry()
		if err != nil {
			return err
		}
		if err := change(fresh); err != nil {
			if errors.Is(err, errUnchanged) {
				*r = *fresh
				return nil
			}
			return err
		}
		if err := WriteJSON(p, fresh); err != nil {
			return err
		}
		*r = *fresh
		return nil
	})
}

// registryTarget returns the registry path, creating its directory so the lock
// file has somewhere to live.
func registryTarget() (string, error) {
	if _, err := paths.EnsureConfigDir(); err != nil {
		return "", err
	}
	return RegistryPath()
}

// Add registers a server directory under name, becoming the default if it is
// the first one known.
func (r *Registry) Add(name, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return r.update(func(reg *Registry) error {
		if existing, ok := reg.Servers[name]; ok && existing != abs {
			return fmt.Errorf("a different server named %q is already registered at %s", name, existing)
		}
		reg.Servers[name] = abs
		if reg.Default == "" {
			reg.Default = name
		}
		return nil
	})
}

// Remove drops a server from the registry, clearing the default if needed. It
// never touches the server's files.
func (r *Registry) Remove(name string) error {
	return r.update(func(reg *Registry) error {
		if _, ok := reg.Servers[name]; !ok {
			return fmt.Errorf("no server named %q is registered", name)
		}
		delete(reg.Servers, name)
		reg.reseatDefault()
		return nil
	})
}

// SetDefault marks a registered server as the one used when no other selection
// applies.
func (r *Registry) SetDefault(name string) error {
	return r.update(func(reg *Registry) error {
		if _, ok := reg.Servers[name]; !ok {
			return fmt.Errorf("no server named %q is registered", name)
		}
		reg.Default = name
		return nil
	})
}

// reseatDefault picks a new default when the current one is no longer
// registered, so a registry never points at a server that is gone.
func (r *Registry) reseatDefault() {
	if _, ok := r.Servers[r.Default]; ok {
		return
	}
	r.Default = ""
	for _, n := range r.Names() {
		r.Default = n
		break
	}
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
// reports the names removed. A default naming a server that is not registered
// is reseated too. The file is left alone when there is nothing to fix.
func (r *Registry) Prune() ([]string, error) {
	var gone []string
	err := r.update(func(reg *Registry) error {
		gone = nil
		for name, dir := range reg.Servers {
			if !Exists(dir) {
				gone = append(gone, name)
				delete(reg.Servers, name)
			}
		}
		before := reg.Default
		reg.reseatDefault()
		sort.Strings(gone)
		if len(gone) == 0 && reg.Default == before {
			return errUnchanged
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return gone, nil
}

package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"mcmgr/internal/paths"
)

// GlobalName is the machine-wide settings file in the user config directory.
const GlobalName = "config.toml"

// LegacyKeyFile is the plain-text CurseForge key file supported before global
// settings existed. It is still read if present.
const LegacyKeyFile = "curseforge.key"

// Global holds settings that are not tied to one server.
type Global struct {
	// CurseForgeKey authenticates against the CurseForge API.
	CurseForgeKey string `toml:"curseforge_key,omitempty"`

	// DefaultLoader and DefaultMemory pre-fill `mcmgr new`, for people who
	// always reach for the same ones.
	DefaultLoader string `toml:"default_loader,omitempty"`
	DefaultMemory string `toml:"default_memory,omitempty"`

	// JavaPath is the java binary used by servers that do not name their own.
	// `mcmgr venv install --global` sets it.
	JavaPath string `toml:"java_path,omitempty"`

	// JavaDist is the distribution downloaded when one is needed and none is
	// named: "temurin" (the default when empty) or "graalvm".
	JavaDist string `toml:"java_distribution,omitempty"`
}

// refreshGlobalCache makes CachedGlobal reflect settings just written, so a
// repair in the same process is seen by later JavaBin calls.
func refreshGlobalCache(g *Global) {
	CachedGlobal()
	*globalValue = *g
}

// globalCache avoids re-reading the settings file for every JavaBin call.
var (
	globalOnce  sync.Once
	globalValue *Global
)

// CachedGlobal reads the global settings once per process. Callers that change
// the settings should use LoadGlobal and Save instead.
func CachedGlobal() *Global {
	globalOnce.Do(func() {
		g, err := LoadGlobal()
		if err != nil || g == nil {
			g = &Global{}
		}
		globalValue = g
	})
	return globalValue
}

// GlobalPath returns the location of the global settings file.
func GlobalPath() (string, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, GlobalName), nil
}

// LoadGlobal reads global settings, returning defaults when absent.
func LoadGlobal() (*Global, error) {
	g := &Global{}
	p, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err == nil {
		if _, derr := toml.Decode(string(data), g); derr != nil {
			return nil, derr
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Fall back to the older key file so existing setups keep working.
	if g.CurseForgeKey == "" {
		dir, derr := paths.ConfigDir()
		if derr == nil {
			if b, rerr := os.ReadFile(filepath.Join(dir, LegacyKeyFile)); rerr == nil {
				g.CurseForgeKey = strings.TrimSpace(string(b))
			}
		}
	}
	return g, nil
}

const globalHeader = `# mcmgr machine-wide settings.
# Server-specific settings live in each server's mcmgr.toml.
`

// Save writes global settings with owner-only permissions, since the file can
// hold an API key.
func (g *Global) Save() error {
	if _, err := paths.EnsureConfigDir(); err != nil {
		return err
	}
	p, err := GlobalPath()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString(globalHeader)
	buf.WriteByte('\n')
	if err := toml.NewEncoder(&buf).Encode(g); err != nil {
		return err
	}
	if err := WriteFileLocked(p, buf.Bytes()); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

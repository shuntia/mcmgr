// Package config reads and writes the per-server manifest (mcmgr.toml), the
// resolved mod lockfile (mcmgr.lock.json), and locates which server a command
// should act on.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// ManifestName is the per-server config file; its presence marks a directory
// as an mcmgr-managed server. It is TOML because it is meant to be read and
// edited by hand.
const ManifestName = "mcmgr.toml"

// LegacyManifestName is the JSON manifest used before TOML. It is read once
// and migrated, so existing servers keep working.
const LegacyManifestName = "mcmgr.json"

// LockName records the exact mod versions currently installed. It stays JSON:
// it is generated, never hand-edited, and exists to be diffed and committed.
const LockName = "mcmgr.lock.json"

// StateDirName holds runtime state (pid/port/token) and logs inside a server dir.
const StateDirName = ".mcmgr"

// Loader identifies the server platform.
type Loader string

// Supported loaders.
const (
	Vanilla  Loader = "vanilla"
	Fabric   Loader = "fabric"
	Quilt    Loader = "quilt"
	Paper    Loader = "paper"
	NeoForge Loader = "neoforge"
)

// AllLoaders lists every loader mcmgr can install.
var AllLoaders = []Loader{Vanilla, Fabric, Quilt, Paper, NeoForge}

// ParseLoader validates a loader name.
func ParseLoader(s string) (Loader, error) {
	l := Loader(strings.ToLower(strings.TrimSpace(s)))
	for _, k := range AllLoaders {
		if l == k {
			return l, nil
		}
	}
	return "", fmt.Errorf("unknown loader %q (supported: %s)", s, JoinLoaders())
}

// JoinLoaders renders the supported loader list for help text.
func JoinLoaders() string {
	parts := make([]string, len(AllLoaders))
	for i, l := range AllLoaders {
		parts[i] = string(l)
	}
	return strings.Join(parts, ", ")
}

// SupportsMods reports whether add-ons can be installed for this loader.
// Vanilla has nowhere to put them.
func (l Loader) SupportsMods() bool { return l != Vanilla }

// ContentDir is the subdirectory add-ons are installed into: "plugins" for
// server software like Paper, "mods" for mod loaders.
func (l Loader) ContentDir() string {
	if l == Paper {
		return "plugins"
	}
	return "mods"
}

// ContentNoun is the singular word for what this loader loads, so Paper users
// read "plugin" rather than "mod".
func (l Loader) ContentNoun() string {
	if l == Paper {
		return "plugin"
	}
	return "mod"
}

// ModrinthFacets are the loader values Modrinth indexes this platform under.
// Paper plugins are commonly tagged for several Bukkit-family loaders, so a
// Paper server matches any of them.
func (l Loader) ModrinthFacets() []string {
	switch l {
	case Paper:
		return []string{"paper", "spigot", "bukkit", "purpur", "folia"}
	case Quilt:
		// Quilt loads Fabric mods, so accept both.
		return []string{"quilt", "fabric"}
	case NeoForge:
		return []string{"neoforge"}
	case Fabric:
		return []string{"fabric"}
	}
	return nil
}

// Java holds the JVM used to run the server.
type Java struct {
	// Path to the java binary. Empty falls back to the machine-wide
	// java_path, and then to "java" from PATH.
	Path string `json:"path,omitempty" toml:"path,omitempty"`
	// Args are JVM arguments placed before -jar.
	Args []string `json:"args,omitempty" toml:"args,omitempty"`
	// RequiredMajor is the Java feature release this Minecraft version needs,
	// as published by Mojang. It is recorded when the server is installed or
	// upgraded so the check before each launch needs no network.
	RequiredMajor int `json:"required_major,omitempty" toml:"required_major,omitempty"`
}

// ModSpec is a wanted add-on as declared by the user. Version is "*" to track
// the newest compatible release, or a provider version id to pin.
type ModSpec struct {
	ID       string `json:"id" toml:"id"`
	Provider string `json:"provider" toml:"provider"`
	Version  string `json:"version,omitempty" toml:"version,omitempty"`
	Name     string `json:"name,omitempty" toml:"name,omitempty"`
}

// Pinned reports whether this spec is locked to one exact version.
func (m ModSpec) Pinned() bool { return m.Version != "" && m.Version != "*" }

// Defaults are per-server preferences that stand in for command-line options.
// An option given on the command line always wins over the value here.
type Defaults struct {
	// Provider is the mod source used when none is named: modrinth or
	// curseforge.
	Provider string `json:"provider,omitempty" toml:"provider,omitempty"`

	// AllowBeta and AllowAlpha widen which release channels may be installed.
	// Full releases are always eligible; these add the pre-release ones, and
	// they are independent so a server can accept betas without alphas.
	AllowBeta  bool `json:"allow_beta,omitempty" toml:"allow_beta,omitempty"`
	AllowAlpha bool `json:"allow_alpha,omitempty" toml:"allow_alpha,omitempty"`

	// AllowClientOnly stops mcmgr refusing add-ons the provider marks as
	// unsupported on servers. Off by default, because installing one is
	// usually a mistake.
	AllowClientOnly bool `json:"allow_client_only,omitempty" toml:"allow_client_only,omitempty"`

	// PruneUnmanaged makes `mod sync` delete add-on files mcmgr did not
	// install, rather than reporting them and leaving them alone.
	PruneUnmanaged bool `json:"prune_unmanaged,omitempty" toml:"prune_unmanaged,omitempty"`
}

// Config is the contents of mcmgr.toml.
//
// Field order matters for the encoder: TOML puts every bare key before the
// first table header, so the scalar settings are declared first and the [java]
// table and [[mods]] array of tables last.
type Config struct {
	Name       string   `json:"name" toml:"name"`
	Minecraft  string   `json:"minecraft" toml:"minecraft"`
	Loader     Loader   `json:"loader" toml:"loader"`
	LoaderVer  string   `json:"loader_version,omitempty" toml:"loader_version,omitempty"`
	Jar        string   `json:"jar,omitempty" toml:"jar,omitempty"`
	ServerArgs []string `json:"server_args,omitempty" toml:"server_args,omitempty"`

	// LaunchArgs replaces "-jar <jar>" for loaders that boot through a JVM
	// argument file instead, such as NeoForge. The token {platform} expands to
	// "win" or "unix" at launch time, so one manifest works on every OS.
	LaunchArgs []string `json:"launch_args,omitempty" toml:"launch_args,omitempty"`

	StopTimeout int `json:"stop_timeout_secs,omitempty" toml:"stop_timeout_secs,omitempty"`

	Java Java `json:"java" toml:"java"`
	// Defaults are the command options this server always wants, so they do
	// not have to be typed on every invocation.
	Defaults Defaults  `json:"defaults" toml:"defaults"`
	Mods     []ModSpec `json:"mods" toml:"mods"`

	dir string // directory containing the manifest; not serialised
}

// Dir returns the server's root directory.
func (c *Config) Dir() string { return c.dir }

// SetDir overrides the resolved directory, for tests and for freshly built
// configs that have not been saved yet.
func (c *Config) SetDir(d string) { c.dir = d }

// Path returns the manifest path.
func (c *Config) Path() string { return filepath.Join(c.dir, ManifestName) }

// ContentPath is the absolute mods/ or plugins/ directory.
func (c *Config) ContentPath() string { return filepath.Join(c.dir, c.Loader.ContentDir()) }

// StatePath is the directory holding runtime state and logs.
func (c *Config) StatePath() string { return filepath.Join(c.dir, StateDirName) }

// ExpandedLaunchArgs returns LaunchArgs with {platform} substituted for the
// current operating system.
func (c *Config) ExpandedLaunchArgs() []string {
	platform := "unix"
	if runtime.GOOS == "windows" {
		platform = "win"
	}
	out := make([]string, len(c.LaunchArgs))
	for i, a := range c.LaunchArgs {
		out[i] = strings.ReplaceAll(a, "{platform}", platform)
	}
	return out
}

// JavaBin is the java executable to invoke, in order of preference: this
// server's own setting, the machine-wide one, then whatever is on PATH.
func (c *Config) JavaBin() string {
	if c.Java.Path != "" {
		return c.Java.Path
	}
	if g := CachedGlobal(); g.JavaPath != "" {
		return g.JavaPath
	}
	return "java"
}

// StopTimeoutSecs is the grace period allowed for a clean shutdown.
func (c *Config) StopTimeoutSecs() int {
	if c.StopTimeout > 0 {
		return c.StopTimeout
	}
	return 90
}

// FindMod returns the index of a spec by id or name, or -1.
func (c *Config) FindMod(id string) int {
	id = strings.ToLower(id)
	for i, m := range c.Mods {
		if strings.ToLower(m.ID) == id || strings.ToLower(m.Name) == id {
			return i
		}
	}
	return -1
}

// ErrNoServer is returned when no server directory could be resolved.
var ErrNoServer = errors.New("no server selected")

// Exists reports whether dir holds a server manifest in either format.
func Exists(dir string) bool {
	for _, name := range []string{ManifestName, LegacyManifestName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// Load reads the manifest in dir. A directory still holding the old JSON
// manifest is migrated to TOML in place, so nobody has to convert by hand.
func Load(dir string) (*Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	p := filepath.Join(abs, ManifestName)
	data, err := os.ReadFile(p)
	if err == nil {
		var c Config
		if _, err := toml.Decode(string(data), &c); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", p, err)
		}
		c.normalise(abs)
		return &c, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	legacy := filepath.Join(abs, LegacyManifestName)
	data, lerr := os.ReadFile(legacy)
	if lerr != nil {
		// Report the missing TOML file, which is the one that should exist.
		return nil, err
	}
	var c Config
	if jerr := json.Unmarshal(data, &c); jerr != nil {
		return nil, fmt.Errorf("parsing %s: %w", legacy, jerr)
	}
	c.normalise(abs)
	if serr := c.Save(); serr != nil {
		return nil, fmt.Errorf("migrating %s to %s: %w", LegacyManifestName, ManifestName, serr)
	}
	// Keep the original as a backup rather than deleting the user's file.
	_ = os.Rename(legacy, legacy+".bak")
	return &c, nil
}

// normalise fills in defaults and records the directory.
func (c *Config) normalise(abs string) {
	if c.Loader == "" {
		c.Loader = Vanilla
	}
	if c.Mods == nil {
		c.Mods = []ModSpec{}
	}
	c.dir = abs
}

// manifestHeader is written above the encoded TOML so the file explains itself
// to anyone who opens it.
const manifestHeader = `# mcmgr server manifest. Safe to edit by hand.
# After changing the mods list, run: mcmgr mod sync
# Exact installed versions are recorded in ` + LockName + `
`

// Save writes the manifest back, atomically.
func (c *Config) Save() error {
	if c.Mods == nil {
		c.Mods = []ModSpec{}
	}
	if c.StopTimeout == 0 {
		// Write the value actually in force rather than an empty 0, so the
		// file shows what the server will do.
		c.StopTimeout = c.StopTimeoutSecs()
	}
	var buf bytes.Buffer
	buf.WriteString(manifestHeader)
	buf.WriteByte('\n')
	enc := toml.NewEncoder(&buf)
	enc.Indent = "" // keep sub-tables flush left, as TOML is usually written
	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("encoding %s: %w", ManifestName, err)
	}
	return WriteFileAtomic(c.Path(), buf.Bytes())
}

// Init creates a manifest in dir. It fails if one already exists.
func Init(dir string, c *Config) (*Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	c.dir = abs
	if Exists(abs) {
		return nil, fmt.Errorf("%s is already an mcmgr server\n"+
			"       use 'mcmgr config' to change its settings, or delete its manifest to start over", abs)
	}
	if c.Name == "" {
		c.Name = filepath.Base(abs)
	}
	return c, c.Save()
}

// Resolve determines which server directory to operate on, in order:
//
//  1. --server naming a registered server, or a path to one
//  2. the nearest mcmgr.json at or above the working directory
//  3. the registry default server
func Resolve(sel string) (*Config, error) {
	if sel != "" {
		// A path wins if it actually holds a manifest.
		if Exists(sel) {
			return Load(sel)
		}
		reg, err := LoadRegistry()
		if err != nil {
			return nil, err
		}
		if dir, ok := reg.Servers[sel]; ok {
			c, err := Load(dir)
			if err != nil {
				return nil, fmt.Errorf("server %q is registered at %s but could not be loaded: %w", sel, dir, err)
			}
			return c, nil
		}
		return nil, fmt.Errorf("no server named %q; run 'mcmgr server list' to see registered servers", sel)
	}

	if dir, ok := findUp(); ok {
		return Load(dir)
	}

	reg, err := LoadRegistry()
	if err == nil && reg.Default != "" {
		if dir, ok := reg.Servers[reg.Default]; ok {
			return Load(dir)
		}
	}
	return nil, fmt.Errorf("%w: run this from a server directory, pass --server <name>, or create one with 'mcmgr server create'", ErrNoServer)
}

// findUp walks from the working directory towards the filesystem root looking
// for a manifest.
func findUp() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if Exists(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// WriteJSON marshals v to path atomically. Used for generated files: the
// lockfile and the server registry.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'))
}

// WriteFileAtomic writes data to path via a temporary file and a rename, so a
// crash mid-write cannot corrupt the existing file.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcmgr-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	// os.Rename replaces an existing file on every platform, so the old
	// contents survive if anything above failed.
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

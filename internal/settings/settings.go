// Package settings presents mcmgr.toml, server.properties and the machine-wide
// settings as one keyspace, so `mcmgr config` can read and write all three
// without the user having to know which file a setting lives in.
package settings

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/props"
	"mcmgr/internal/warn"
)

// Scope says which file a key belongs to.
type Scope int

const (
	// Manifest is mcmgr.toml, mcmgr's own per-server settings.
	Manifest Scope = iota
	// Properties is Minecraft's server.properties.
	Properties
	// Global is the machine-wide settings file.
	Global
)

// String names a scope for display.
func (s Scope) String() string {
	switch s {
	case Manifest:
		return config.ManifestName
	case Properties:
		return props.FileName
	default:
		return "global"
	}
}

// Kind drives validation and which prompt widget the interactive editor uses.
const (
	KindString = "string"
	KindBool   = "bool"
	KindInt    = "int"
	KindList   = "list"
	KindSecret = "secret"
)

// Key is one addressable setting.
type Key struct {
	// Name is the canonical, fully qualified name, e.g. "properties.white-list".
	Name    string
	Scope   Scope
	Kind    string
	Help    string
	Choices []string
	// ReadOnly keys are shown by `config list` but cannot be set directly.
	ReadOnly bool
}

// manifestKeys are the settings held in mcmgr.toml.
var manifestKeys = []Key{
	{Name: "name", Scope: Manifest, Kind: KindString, Help: "Name this server is registered under"},
	{Name: "minecraft", Scope: Manifest, Kind: KindString, Help: "Minecraft version"},
	{Name: "loader", Scope: Manifest, Kind: KindString, Help: "Server platform",
		Choices: loaderChoices()},
	{Name: "loader_version", Scope: Manifest, Kind: KindString, Help: "Pinned loader build"},
	{Name: "jar", Scope: Manifest, Kind: KindString, Help: "Server jar, relative to the server directory"},
	{Name: "memory", Scope: Manifest, Kind: KindString, Help: "JVM heap size, e.g. 4G (maps to -Xmx)"},
	{Name: "java.path", Scope: Manifest, Kind: KindString, Help: "Java binary; empty means java from PATH"},
	{Name: "java.args", Scope: Manifest, Kind: KindList, Help: "Extra JVM arguments"},
	{Name: "java.required_major", Scope: Manifest, Kind: KindInt,
		Help: "Java feature release this Minecraft version needs"},
	{Name: "server_args", Scope: Manifest, Kind: KindList, Help: "Arguments passed after the jar"},
	{Name: "stop_timeout_secs", Scope: Manifest, Kind: KindInt, Help: "Seconds to wait for a clean shutdown"},

	// Defaults for command options, so the flags you always pass can be set
	// once. Each is still overridable per command.
	{Name: "defaults.provider", Scope: Manifest, Kind: KindString,
		Help: "Mod source used when none is named", Choices: []string{"modrinth", "curseforge"}},
	{Name: "defaults.allow_beta", Scope: Manifest, Kind: KindBool,
		Help: "Accept beta mod builds as well as full releases"},
	{Name: "defaults.allow_alpha", Scope: Manifest, Kind: KindBool,
		Help: "Accept alpha mod builds as well as full releases"},
	{Name: "defaults.allow_client_only", Scope: Manifest, Kind: KindBool,
		Help: "Stop refusing add-ons the provider says do not run on servers"},
	{Name: "defaults.prune_unmanaged", Scope: Manifest, Kind: KindBool,
		Help: "Make 'mod sync' delete add-on files mcmgr did not install"},
}

// globalKeys are the machine-wide settings.
var globalKeys = []Key{
	{Name: "global.default_server", Scope: Global, Kind: KindString,
		Help: "Server used when none is selected"},
	{Name: "global.curseforge_key", Scope: Global, Kind: KindSecret,
		Help: "CurseForge API key, from https://console.curseforge.com/"},
	{Name: "global.default_loader", Scope: Global, Kind: KindString,
		Help: "Loader 'mcmgr new' offers first", Choices: loaderChoices()},
	{Name: "global.default_memory", Scope: Global, Kind: KindString,
		Help: "Heap size 'mcmgr new' offers first, e.g. 4G"},
	{Name: "global.java_path", Scope: Global, Kind: KindString,
		Help: "Java used by servers that do not set java.path"},
}

func loaderChoices() []string {
	out := make([]string, len(config.AllLoaders))
	for i, l := range config.AllLoaders {
		out[i] = string(l)
	}
	return out
}

// aliases map convenient spellings onto canonical names. Minecraft's own key
// is "white-list", but nobody types the hyphen.
var aliases = map[string]string{
	"whitelist":    "properties.white-list",
	"white-list":   "properties.white-list",
	"port":         "properties.server-port",
	"online_mode":  "properties.online-mode",
	"onlinemode":   "properties.online-mode",
	"maxplayers":   "properties.max-players",
	"java_args":    "java.args",
	"xmx":          "memory",
	"default":      "global.default_server",
	"curseforge":   "global.curseforge_key",
	"cf_key":       "global.curseforge_key",
	"stop_timeout": "stop_timeout_secs",

	// The [defaults] table is what people reach for most, so its keys answer
	// to their bare names too.
	"beta":              "defaults.allow_beta",
	"allow_beta":        "defaults.allow_beta",
	"alpha":             "defaults.allow_alpha",
	"allow_alpha":       "defaults.allow_alpha",
	"provider":          "defaults.provider",
	"allow_client_only": "defaults.allow_client_only",
	"client_only":       "defaults.allow_client_only",
	"prune":             "defaults.prune_unmanaged",
	"prune_unmanaged":   "defaults.prune_unmanaged",
}

// Store holds every settings file for one server, loaded together so a single
// `config set` can touch whichever one owns the key.
type Store struct {
	Cfg    *config.Config
	Props  *props.File
	Glob   *config.Global
	Reg    *config.Registry
	Ctx    warn.Context
	dirty  map[Scope]bool
	loaded bool
}

// Open loads all settings files for a server. cfg may be nil for global-only
// use, such as setting the CurseForge key before any server exists.
func Open(cfg *config.Config) (*Store, error) {
	s := &Store{Cfg: cfg, dirty: map[Scope]bool{}}

	g, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	s.Glob = g

	reg, err := config.LoadRegistry()
	if err != nil {
		return nil, err
	}
	s.Reg = reg

	if cfg != nil {
		p, perr := props.Load(cfg.Dir())
		if perr != nil {
			return nil, perr
		}
		s.Props = p
		s.Ctx = warn.Context{
			Dir:       cfg.Dir(),
			LevelName: p.GetOr("level-name", "world"),
		}
		if lock, lerr := cfg.LoadLock(); lerr == nil {
			s.Ctx.ModCount = len(lock.Mods)
		}
	}
	s.loaded = true
	return s, nil
}

// SetRunning records whether the server is up, which some warnings depend on.
func (s *Store) SetRunning(running bool) { s.Ctx.Running = running }

// Keys returns every addressable key, manifest first, then the documented
// server.properties settings, then global ones.
func (s *Store) Keys() []Key {
	var out []Key
	if s.Cfg != nil {
		out = append(out, manifestKeys...)
		for _, p := range props.Known {
			out = append(out, Key{
				Name:    "properties." + p.Key,
				Scope:   Properties,
				Kind:    kindOf(p.Kind),
				Help:    p.Help,
				Choices: p.Choices,
			})
		}
		// Keys present in the file that mcmgr does not document are still
		// addressable, so nothing becomes uneditable.
		if s.Props != nil {
			for _, k := range s.Props.Keys() {
				if !props.IsKnown(k) {
					out = append(out, Key{
						Name: "properties." + k, Scope: Properties, Kind: KindString,
						Help: "(not documented by mcmgr)",
					})
				}
			}
		}
	}
	out = append(out, globalKeys...)
	return out
}

func kindOf(propKind string) string {
	switch propKind {
	case "bool":
		return KindBool
	case "int":
		return KindInt
	}
	return KindString
}

// Resolve turns a user-supplied name into a canonical key.
func (s *Store) Resolve(name string) (Key, error) {
	name = strings.TrimSpace(name)
	if canonical, ok := aliases[strings.ToLower(name)]; ok {
		name = canonical
	}

	for _, k := range s.Keys() {
		if strings.EqualFold(k.Name, name) {
			return k, nil
		}
	}

	// A bare name that matches a server.properties key is accepted, so
	// "mcmgr config set motd ..." works without the prefix.
	if !strings.Contains(name, ".") && s.Cfg != nil {
		if p, ok := props.Lookup(name); ok {
			return Key{Name: "properties." + p.Key, Scope: Properties,
				Kind: kindOf(p.Kind), Help: p.Help, Choices: p.Choices}, nil
		}
		if s.Props != nil {
			if _, ok := s.Props.Get(name); ok {
				return Key{Name: "properties." + name, Scope: Properties, Kind: KindString}, nil
			}
		}
	}

	// An undocumented properties key can still be written explicitly.
	if rest, ok := strings.CutPrefix(strings.ToLower(name), "properties."); ok && s.Cfg != nil {
		return Key{Name: "properties." + rest, Scope: Properties, Kind: KindString,
			Help: "(not documented by mcmgr)"}, nil
	}

	return Key{}, fmt.Errorf("unknown setting %q\n       run 'mcmgr config list' to see what can be set", name)
}

// xmxPattern matches the JVM maximum-heap flag.
var xmxPattern = regexp.MustCompile(`^-Xmx(.+)$`)

// Get returns a key's current value. Unset server.properties keys report the
// value Minecraft would use.
func (s *Store) Get(k Key) string {
	switch k.Scope {
	case Manifest:
		return s.getManifest(k.Name)
	case Properties:
		name := strings.TrimPrefix(k.Name, "properties.")
		if s.Props != nil {
			if v, ok := s.Props.Get(name); ok {
				return v
			}
		}
		if p, ok := props.Lookup(name); ok {
			return p.Default
		}
		return ""
	default:
		switch k.Name {
		case "global.default_server":
			return s.Reg.Default
		case "global.curseforge_key":
			if s.Glob.CurseForgeKey == "" {
				return ""
			}
			return "(set)"
		case "global.default_loader":
			return s.Glob.DefaultLoader
		case "global.default_memory":
			return s.Glob.DefaultMemory
		case "global.java_path":
			return s.Glob.JavaPath
		}
	}
	return ""
}

func (s *Store) getManifest(name string) string {
	c := s.Cfg
	switch name {
	case "name":
		return c.Name
	case "minecraft":
		return c.Minecraft
	case "loader":
		return string(c.Loader)
	case "loader_version":
		return c.LoaderVer
	case "jar":
		return c.Jar
	case "memory":
		for _, a := range c.Java.Args {
			if m := xmxPattern.FindStringSubmatch(a); m != nil {
				return m[1]
			}
		}
		return ""
	case "java.path":
		return c.Java.Path
	case "java.args":
		return strings.Join(c.Java.Args, " ")
	case "java.required_major":
		if c.Java.RequiredMajor == 0 {
			return ""
		}
		return strconv.Itoa(c.Java.RequiredMajor)
	case "server_args":
		return strings.Join(c.ServerArgs, " ")
	case "stop_timeout_secs":
		if c.StopTimeout == 0 {
			return ""
		}
		return strconv.Itoa(c.StopTimeout)
	case "defaults.provider":
		return c.Defaults.Provider
	case "defaults.allow_beta":
		return strconv.FormatBool(c.Defaults.AllowBeta)
	case "defaults.allow_alpha":
		return strconv.FormatBool(c.Defaults.AllowAlpha)
	case "defaults.allow_client_only":
		return strconv.FormatBool(c.Defaults.AllowClientOnly)
	case "defaults.prune_unmanaged":
		return strconv.FormatBool(c.Defaults.PruneUnmanaged)
	}
	return ""
}

// memoryPattern matches JVM heap sizes such as 4G, 512M or 2048.
var memoryPattern = regexp.MustCompile(`^\d+[kKmMgG]?$`)

// Validate checks a value for a key without applying it, so a bad value is
// reported where it is typed rather than failing later at save time.
func (s *Store) Validate(k Key, value string) error {
	// Kind checks apply everywhere. An empty value is allowed for manifest
	// and global keys, where it means "unset".
	switch k.Kind {
	case KindBool:
		if _, err := strconv.ParseBool(strings.TrimSpace(value)); err != nil && !(value == "" && k.Scope != Properties) {
			return fmt.Errorf("%s must be true or false, got %q", k.Name, value)
		}
	case KindInt:
		if value != "" || k.Scope == Properties {
			if _, err := strconv.Atoi(strings.TrimSpace(value)); err != nil {
				return fmt.Errorf("%s must be a whole number, got %q", k.Name, value)
			}
		}
	}

	switch k.Scope {
	case Properties:
		name := strings.TrimPrefix(k.Name, "properties.")
		if p, ok := props.Lookup(name); ok {
			return p.Validate(value)
		}
		return nil
	case Manifest:
		switch k.Name {
		case "loader":
			_, err := config.ParseLoader(value)
			return err
		case "memory":
			if value != "" && !memoryPattern.MatchString(value) {
				return fmt.Errorf("memory should look like 4G, 512M or 2048, got %q", value)
			}
		case "stop_timeout_secs":
			if value == "" {
				return nil
			}
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return fmt.Errorf("stop_timeout_secs must be a positive whole number, got %q", value)
			}
		case "java.required_major":
			if value == "" {
				return nil
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 8 {
				return fmt.Errorf("java.required_major must be a Java feature release such as 21, got %q", value)
			}
		case "name":
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("name cannot be empty")
			}
		case "minecraft":
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("minecraft version cannot be empty")
			}
		}
	case Global:
		if k.Name == "global.default_server" && value != "" {
			if _, ok := s.Reg.Servers[value]; !ok {
				return fmt.Errorf("no server named %q is registered", value)
			}
		}
	}
	if len(k.Choices) > 0 && value != "" {
		for _, c := range k.Choices {
			if strings.EqualFold(c, value) {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %s, got %q", k.Name, strings.Join(k.Choices, ", "), value)
	}
	return nil
}

// Warnings returns the warnings that setting k to value would trigger.
func (s *Store) Warnings(k Key, value string) []warn.Warning {
	ctx := s.Ctx
	ctx.Old = s.Get(k)
	if ctx.Old == value {
		return nil // nothing is actually changing
	}
	switch k.Scope {
	case Properties:
		return warn.ForProperty(strings.TrimPrefix(k.Name, "properties."), value, ctx)
	case Manifest:
		return warn.ForManifest(k.Name, value, ctx)
	}
	return nil
}

// Set applies a value in memory. Call Save to persist it.
func (s *Store) Set(k Key, value string) error {
	if err := s.Validate(k, value); err != nil {
		return err
	}
	switch k.Scope {
	case Properties:
		if s.Props == nil {
			return fmt.Errorf("no server selected, so %s cannot be set", k.Name)
		}
		s.Props.Set(strings.TrimPrefix(k.Name, "properties."), value)
	case Manifest:
		if err := s.setManifest(k.Name, value); err != nil {
			return err
		}
	case Global:
		switch k.Name {
		case "global.default_server":
			s.Reg.Default = value
		case "global.curseforge_key":
			s.Glob.CurseForgeKey = value
		case "global.default_loader":
			s.Glob.DefaultLoader = value
		case "global.default_memory":
			s.Glob.DefaultMemory = value
		case "global.java_path":
			s.Glob.JavaPath = value
		}
	}
	s.dirty[k.Scope] = true
	return nil
}

func (s *Store) setManifest(name, value string) error {
	c := s.Cfg
	if c == nil {
		return fmt.Errorf("no server selected, so %s cannot be set", name)
	}
	switch name {
	case "name":
		c.Name = value
	case "minecraft":
		c.Minecraft = value
	case "loader":
		l, err := config.ParseLoader(value)
		if err != nil {
			return err
		}
		c.Loader = l
	case "loader_version":
		c.LoaderVer = value
	case "jar":
		c.Jar = value
	case "memory":
		c.Java.Args = setXmx(c.Java.Args, value)
	case "java.path":
		c.Java.Path = value
	case "java.args":
		c.Java.Args = strings.Fields(value)
	case "java.required_major":
		if value == "" {
			c.Java.RequiredMajor = 0
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		c.Java.RequiredMajor = n
	case "server_args":
		c.ServerArgs = strings.Fields(value)
	case "stop_timeout_secs":
		if value == "" {
			c.StopTimeout = 0
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		c.StopTimeout = n
	case "defaults.provider":
		c.Defaults.Provider = value
	case "defaults.allow_beta":
		return assignBool(value, &c.Defaults.AllowBeta)
	case "defaults.allow_alpha":
		return assignBool(value, &c.Defaults.AllowAlpha)
	case "defaults.allow_client_only":
		return assignBool(value, &c.Defaults.AllowClientOnly)
	case "defaults.prune_unmanaged":
		return assignBool(value, &c.Defaults.PruneUnmanaged)
	default:
		return fmt.Errorf("unknown manifest setting %q", name)
	}
	return nil
}

// setXmx replaces the -Xmx flag in args, adding or removing it as needed so
// "memory" behaves like a normal setting.
func setXmx(args []string, value string) []string {
	out := make([]string, 0, len(args)+1)
	for _, a := range args {
		if !xmxPattern.MatchString(a) {
			out = append(out, a)
		}
	}
	if value == "" {
		return out
	}
	return append([]string{"-Xmx" + value}, out...)
}

// Save writes back only the files that changed.
func (s *Store) Save() error {
	if s.dirty[Manifest] && s.Cfg != nil {
		if err := s.Cfg.Save(); err != nil {
			return err
		}
	}
	if s.dirty[Properties] && s.Props != nil {
		if err := s.Props.Save(); err != nil {
			return err
		}
	}
	if s.dirty[Global] {
		if err := s.Glob.Save(); err != nil {
			return err
		}
		if err := s.Reg.Save(); err != nil {
			return err
		}
	}
	return nil
}

// Changed reports whether anything has been set since loading.
func (s *Store) Changed() bool {
	for _, v := range s.dirty {
		if v {
			return true
		}
	}
	return false
}

// SortKeys orders keys for display: by scope, then name.
func SortKeys(keys []Key) {
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].Scope != keys[j].Scope {
			return keys[i].Scope < keys[j].Scope
		}
		return keys[i].Name < keys[j].Name
	})
}

// assignBool parses a boolean setting value into a field.
func assignBool(value string, dst *bool) error {
	b, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("expected true or false, got %q", value)
	}
	*dst = b
	return nil
}

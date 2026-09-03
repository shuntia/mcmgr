// Package props reads and writes Minecraft's server.properties.
//
// The file is a Java properties file that the server itself rewrites on every
// start, so this package preserves the comments, blank lines, key order and
// unknown keys it finds. Editing one value leaves the rest of the file byte
// for byte as it was.
package props

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mcmgr/internal/config"
)

// FileName is Minecraft's own settings file, in the server directory.
const FileName = "server.properties"

// line is one physical line: either a comment/blank kept verbatim, or a
// key/value pair.
type line struct {
	raw   string // comment or blank line, when key is empty
	key   string
	value string
}

// File is a parsed server.properties.
type File struct {
	path  string
	lines []line
	index map[string]int // key -> position in lines
}

// Path returns the file's location.
func (f *File) Path() string { return f.path }

// PathFor returns the server.properties path for a server directory.
func PathFor(dir string) string { return filepath.Join(dir, FileName) }

// Load reads server.properties from a server directory. A missing file yields
// an empty File, so settings can be written before the server's first run.
func Load(dir string) (*File, error) {
	path := PathFor(dir)
	f := &File{path: path, index: map[string]int{}}

	fh, err := os.Open(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		text := sc.Text()
		trimmed := strings.TrimSpace(text)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			f.lines = append(f.lines, line{raw: text})
			continue
		}
		key, value, found := strings.Cut(text, "=")
		if !found {
			// Not a key/value line; keep it rather than lose it.
			f.lines = append(f.lines, line{raw: text})
			continue
		}
		k := strings.TrimSpace(key)
		f.index[k] = len(f.lines)
		// Java properties skip whitespace after the separator but keep
		// trailing whitespace, so a motd ending in spaces must survive a
		// round trip.
		f.lines = append(f.lines, line{key: k, value: unescape(strings.TrimLeft(value, " \t"))})
	}
	return f, sc.Err()
}

// Exists reports whether the file is present on disk.
func (f *File) Exists() bool {
	_, err := os.Stat(f.path)
	return err == nil
}

// Get returns a value and whether the key was present.
func (f *File) Get(key string) (string, bool) {
	i, ok := f.index[key]
	if !ok {
		return "", false
	}
	return f.lines[i].value, true
}

// GetOr returns a value, or fallback when the key is absent.
func (f *File) GetOr(key, fallback string) string {
	if v, ok := f.Get(key); ok {
		return v
	}
	return fallback
}

// Bool interprets a key as a boolean, returning fallback when absent or
// unparseable.
func (f *File) Bool(key string, fallback bool) bool {
	v, ok := f.Get(key)
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return b
}

// Set writes a key, appending it if it is new.
func (f *File) Set(key, value string) {
	if i, ok := f.index[key]; ok {
		f.lines[i].value = value
		return
	}
	f.index[key] = len(f.lines)
	f.lines = append(f.lines, line{key: key, value: value})
}

// SetBool writes a boolean key in the form Minecraft uses.
func (f *File) SetBool(key string, v bool) {
	f.Set(key, strconv.FormatBool(v))
}

// Keys returns every key present, sorted.
func (f *File) Keys() []string {
	keys := make([]string, 0, len(f.index))
	for k := range f.index {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Save writes the file back, preserving comments, ordering and unknown keys.
func (f *File) Save() error {
	var b strings.Builder
	if len(f.lines) == 0 {
		b.WriteString("# Minecraft server properties\n# Written by mcmgr; the server rewrites this file when it starts.\n")
	}
	for _, l := range f.lines {
		if l.key == "" {
			b.WriteString(l.raw)
		} else {
			b.WriteString(l.key)
			b.WriteByte('=')
			b.WriteString(escape(l.value))
		}
		b.WriteByte('\n')
	}
	return config.WriteFileLocked(f.path, []byte(b.String()))
}

// unescape decodes the subset of Java properties escapes Minecraft actually
// writes. Anything else is left alone rather than mangled.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'u':
			if i+4 < len(s) {
				if n, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(n))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		default:
			// \\, \:, \= and friends stand for the character itself.
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// escape re-encodes the characters that would otherwise change the meaning of
// a properties line.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '=', ':':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			if r > 126 {
				// Minecraft reads server.properties as UTF-8, so non-ASCII is
				// written literally rather than as \\u escapes.
				b.WriteRune(r)
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Setting describes a server.properties key mcmgr knows about, so it can be
// listed, validated and explained without the user consulting the wiki.
type Setting struct {
	Key     string
	Default string
	// Kind is "bool", "int" or "string"; it drives validation and the prompt
	// widget used for the key.
	Kind string
	Help string
	// Choices, when set, restricts a string setting to these values.
	Choices []string
	// Min and Max bound an int setting when Max is non-zero. They follow what
	// the server itself accepts, so a value outside them is rejected here
	// rather than silently clamped or refused at startup.
	Min, Max int
}

// Known lists the settings mcmgr surfaces in prompts and `config list`. Any
// other key in server.properties can still be read and written by name.
var Known = []Setting{
	{Key: "server-port", Default: "25565", Kind: "int", Help: "TCP port the server listens on", Min: 1, Max: 65535},
	{Key: "motd", Default: "A Minecraft Server", Kind: "string", Help: "Message shown in the server list"},
	{Key: "max-players", Default: "20", Kind: "int", Help: "Maximum simultaneous players", Min: 1, Max: 1<<31 - 1},
	{Key: "white-list", Default: "false", Kind: "bool", Help: "Only allow players on the whitelist"},
	{Key: "enforce-whitelist", Default: "false", Kind: "bool", Help: "Kick players who are removed from the whitelist"},
	{Key: "online-mode", Default: "true", Kind: "bool", Help: "Verify players against Mojang's authentication servers"},
	{Key: "pvp", Default: "true", Kind: "bool", Help: "Allow players to damage each other"},
	{Key: "difficulty", Default: "easy", Kind: "string", Help: "World difficulty",
		Choices: []string{"peaceful", "easy", "normal", "hard"}},
	{Key: "gamemode", Default: "survival", Kind: "string", Help: "Default game mode for new players",
		Choices: []string{"survival", "creative", "adventure", "spectator"}},
	{Key: "hardcore", Default: "false", Kind: "bool", Help: "Death is permanent and the world locks to spectator"},
	{Key: "level-name", Default: "world", Kind: "string", Help: "World directory name"},
	{Key: "level-seed", Default: "", Kind: "string", Help: "Seed used when generating a new world"},
	{Key: "view-distance", Default: "10", Kind: "int", Help: "Chunk render distance sent to clients", Min: 3, Max: 32},
	{Key: "simulation-distance", Default: "10", Kind: "int", Help: "Chunk radius that stays fully ticking", Min: 3, Max: 32},
	{Key: "spawn-protection", Default: "16", Kind: "int", Help: "Radius around spawn only operators may build in", Min: 0, Max: 1<<31 - 1},
	{Key: "enable-command-block", Default: "false", Kind: "bool", Help: "Allow command blocks to run"},
	{Key: "allow-flight", Default: "false", Kind: "bool", Help: "Permit flight in survival mode"},
	{Key: "allow-nether", Default: "true", Kind: "bool", Help: "Enable the Nether"},
	{Key: "enable-rcon", Default: "false", Kind: "bool", Help: "Expose the remote console protocol"},
	{Key: "enable-status", Default: "true", Kind: "bool", Help: "Reply to server-list pings"},
}

// Lookup finds a known setting by key.
func Lookup(key string) (Setting, bool) {
	for _, s := range Known {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// IsKnown reports whether a key is one mcmgr documents.
func IsKnown(key string) bool {
	_, ok := Lookup(key)
	return ok
}

// Validate checks a value against a known setting's kind and choices.
func (s Setting) Validate(value string) error {
	switch s.Kind {
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be true or false, got %q", s.Key, value)
		}
	case "int":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%s must be a whole number, got %q", s.Key, value)
		}
		if s.Max != 0 && (n < s.Min || n > s.Max) {
			if s.Max == 1<<31-1 {
				return fmt.Errorf("%s must be at least %d, got %d", s.Key, s.Min, n)
			}
			return fmt.Errorf("%s must be between %d and %d, got %d", s.Key, s.Min, s.Max, n)
		}
	}
	if len(s.Choices) > 0 {
		for _, c := range s.Choices {
			if strings.EqualFold(c, value) {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %s, got %q", s.Key, strings.Join(s.Choices, ", "), value)
	}
	return nil
}

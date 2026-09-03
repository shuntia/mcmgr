package server

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/props"
)

// Detected is what inspecting a server directory turned up. Every value is a
// best guess; the fields ending in "From" record where the guess came from, so
// the user can judge it rather than being asked to trust it.
type Detected struct {
	Minecraft     string
	MinecraftFrom string

	Loader     config.Loader
	LoaderFrom string
	LoaderVer  string

	Jar        string
	LaunchArgs []string
	// Jars lists every .jar in the directory, as fallback candidates.
	Jars []string

	// AddOns counts add-on files already present in the content directory.
	AddOns int
	// World is the level directory, when one exists.
	World string
}

// maxJarEntry bounds how much of a jar entry is read; the metadata files this
// looks at are a few hundred bytes.
const maxJarEntry = 1 << 20

// Inspect works out what kind of server a directory holds, without changing
// anything in it.
//
// Detection is layered, most authoritative first, because the cheap signals
// are also the least trustworthy: a jar can be renamed, but the version
// recorded inside it cannot be wrong.
func Inspect(dir string) Detected {
	var d Detected

	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
				d.Jars = append(d.Jars, e.Name())
			}
		}
		sort.Strings(d.Jars)
	}

	// NeoForge leaves an argument file rather than a launchable jar, so it is
	// recognised from the directory layout instead.
	if args, ver, ok := findNeoForgeArgs(dir); ok {
		d.Loader, d.LaunchArgs, d.LoaderVer = config.NeoForge, args, ver
		d.LoaderFrom = "libraries/net/neoforged/neoforge/" + ver
		if mc, ok := minecraftFromNeoForge(ver); ok {
			d.Minecraft, d.MinecraftFrom = mc, "NeoForge "+ver
		}
	}

	d.detectFromJars(dir)

	// Directory-level fallbacks, in decreasing order of authority.
	if d.Minecraft == "" {
		if v := versionFromHistory(dir); v != "" {
			d.Minecraft, d.MinecraftFrom = v, "version_history.json"
		}
	}
	if d.Minecraft == "" {
		if v, src := versionFromLogs(dir); v != "" {
			d.Minecraft, d.MinecraftFrom = v, src
		}
	}
	if d.Minecraft == "" {
		// Last resort: the name of a jar nobody has renamed.
		for _, name := range d.Jars {
			if m := vanillaJarPattern.FindStringSubmatch(name); m != nil {
				d.Minecraft, d.MinecraftFrom = m[1], name+" (file name)"
				break
			}
		}
	}

	if d.Loader == "" && d.Jar != "" {
		// A jar that names no loader is most likely plain Minecraft, but this
		// is a guess and says so.
		d.Loader, d.LoaderFrom = config.Vanilla, "assumed; nothing identified a loader"
	}

	if d.Loader != "" {
		if entries, err := os.ReadDir(filepath.Join(dir, d.Loader.ContentDir())); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
					d.AddOns++
				}
			}
		}
	}

	if p, err := props.Load(dir); err == nil {
		level := p.GetOr("level-name", "world")
		if fi, serr := os.Stat(filepath.Join(dir, level)); serr == nil && fi.IsDir() {
			d.World = level
		}
	}
	return d
}

// detectFromJars reads each candidate jar's own metadata. This is the reliable
// layer: the loader and game version are recorded inside the jar, so renaming
// it changes nothing.
func (d *Detected) detectFromJars(dir string) {
	for _, name := range d.Jars {
		path := filepath.Join(dir, name)

		// Fabric and Quilt server launchers declare both versions outright.
		if game, loaderName, loaderVer, ok := readInstallProperties(path); ok {
			if d.Minecraft == "" && game != "" {
				d.Minecraft, d.MinecraftFrom = game, name+" (install.properties)"
			}
			if d.Loader == "" && loaderName != "" {
				d.Loader, d.LoaderVer = loaderName, loaderVer
				d.LoaderFrom = name + " (install.properties)"
			}
			if d.Jar == "" {
				d.Jar = name
			}
			continue
		}

		// Vanilla, Paper and anything else built on the bundler or Paperclip
		// carry the game's own version.json.
		if v, ok := readJarVersionJSON(path); ok && d.Minecraft == "" {
			d.Minecraft, d.MinecraftFrom = v, name+" (version.json)"
			if d.Jar == "" {
				d.Jar = name
			}
		}

		// The launcher class names the platform outright, and survives the jar
		// being renamed. This is checked for every jar, not just ones that
		// carried a version, so a jar with no version metadata is still
		// identified.
		if d.Loader == "" {
			if l, ok := readJarLoader(path); ok {
				d.Loader, d.LoaderFrom = l, name+" (manifest)"
				if d.Jar == "" {
					d.Jar = name
				}
			} else if l := loaderFromJarName(name); l != "" {
				d.Loader, d.LoaderFrom = l, name+" (file name)"
				if d.Jar == "" {
					d.Jar = name
				}
			}
		}
	}

	// Nothing identified itself: fall back to naming conventions.
	if d.Jar == "" {
		for _, name := range d.Jars {
			if l := loaderFromJarName(name); l != "" || strings.Contains(strings.ToLower(name), "server") {
				d.Jar = name
				if d.Loader == "" && l != "" {
					d.Loader, d.LoaderFrom = l, name+" (file name)"
				}
				break
			}
		}
	}
	// Still nothing, but there is exactly one jar here: it is the only thing
	// this server could be launching, whatever it is called.
	if d.Jar == "" && len(d.Jars) == 1 {
		d.Jar = d.Jars[0]
	}
}

// readJarLoader identifies the platform from the jar's manifest, which names
// the class that boots the server:
//
//	io.papermc.paperclip.Main          Paper and its forks
//	net.minecraft.bundler.Main         vanilla
//	net.fabricmc.installer.*           Fabric
//
// Unlike the file name, this survives someone renaming the jar.
func readJarLoader(jarPath string) (config.Loader, bool) {
	data, err := jarEntry(jarPath, "META-INF/MANIFEST.MF")
	if err != nil {
		return "", false
	}
	manifest := strings.ToLower(string(data))
	switch {
	case strings.Contains(manifest, "papermc.paperclip"), strings.Contains(manifest, "paperclip.main"):
		return config.Paper, true
	case strings.Contains(manifest, "quiltmc"):
		return config.Quilt, true
	case strings.Contains(manifest, "fabricmc"):
		return config.Fabric, true
	case strings.Contains(manifest, "neoforge"):
		return config.NeoForge, true
	case strings.Contains(manifest, "net.minecraft.bundler"), strings.Contains(manifest, "net.minecraft.server"):
		return config.Vanilla, true
	}
	return "", false
}

// loaderFromJarName guesses a loader from a conventional file name.
func loaderFromJarName(name string) config.Loader {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "fabric"):
		return config.Fabric
	case strings.Contains(lower, "quilt"):
		return config.Quilt
	case strings.HasPrefix(lower, "paper"), strings.HasPrefix(lower, "purpur"),
		strings.HasPrefix(lower, "folia"):
		return config.Paper
	case vanillaJarPattern.MatchString(name), lower == "server.jar":
		return config.Vanilla
	}
	return ""
}

// readInstallProperties reads the install.properties that Fabric and Quilt
// server launchers embed, which records the exact game and loader versions.
//
//	fabric-loader-version=0.16.14
//	game-version=1.21.4
func readInstallProperties(jarPath string) (game string, loader config.Loader, loaderVer string, ok bool) {
	data, err := jarEntry(jarPath, "install.properties")
	if err != nil {
		return "", "", "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "game-version":
			game = value
		case "fabric-loader-version":
			loader, loaderVer = config.Fabric, value
		case "quilt-loader-version":
			loader, loaderVer = config.Quilt, value
		}
	}
	return game, loader, loaderVer, game != "" || loader != ""
}

// readJarVersionJSON reads the version.json that Mojang's bundler and Paper's
// Paperclip both place at the root of a server jar.
func readJarVersionJSON(jarPath string) (string, bool) {
	data, err := jarEntry(jarPath, "version.json")
	if err != nil {
		return "", false
	}
	var doc struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", false
	}
	if doc.ID != "" {
		return doc.ID, true
	}
	return doc.Name, doc.Name != ""
}

// jarEntry returns one file from inside a jar. Opening a zip reads only the
// central directory, so this stays cheap even on a 50 MB server jar.
func jarEntry(jarPath, name string) ([]byte, error) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, maxJarEntry))
	}
	return nil, fs.ErrNotExist
}

// startupVersionPattern matches the line every server prints on boot, e.g.
// "Starting minecraft server version 1.21.4".
var startupVersionPattern = regexp.MustCompile(`(?i)starting minecraft server version ([0-9][\w.\-]*)`)

// runningVersionPattern matches Paper's own banner, "(MC: 1.21.4)".
var runningVersionPattern = regexp.MustCompile(`\(MC:\s*([0-9][\w.\-]*)\)`)

// logScanLimit bounds how much of a log is searched; the version appears in
// the first few lines.
const logScanLimit = 400

// versionFromLogs reads what the server itself reported the last time it ran.
// mcmgr's own console capture is checked first, then Minecraft's log.
func versionFromLogs(dir string) (string, string) {
	candidates := []string{
		filepath.Join(dir, config.StateDirName, ConsoleLogName),
		filepath.Join(dir, "logs", "latest.log"),
	}
	for _, path := range candidates {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		version := scanForVersion(f)
		f.Close()
		if version != "" {
			rel, rerr := filepath.Rel(dir, path)
			if rerr != nil {
				rel = path
			}
			return version, filepath.ToSlash(rel)
		}
	}
	return "", ""
}

func scanForVersion(r io.Reader) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < logScanLimit && sc.Scan(); i++ {
		line := sc.Text()
		if m := startupVersionPattern.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		if m := runningVersionPattern.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// versionHistoryPattern pulls the Minecraft version out of Paper's
// version_history.json, e.g. "git-Paper-232 (MC: 1.21.4)".
var versionHistoryPattern = regexp.MustCompile(`MC:\s*([0-9][\w.\-]*)`)

func versionFromHistory(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "version_history.json"))
	if err != nil {
		return ""
	}
	if m := versionHistoryPattern.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// vanillaJarPattern matches the name Mojang's downloads use, e.g.
// minecraft_server.1.21.4.jar.
var vanillaJarPattern = regexp.MustCompile(`^minecraft_server\.([0-9][0-9.]*)\.jar$`)

// minecraftFromNeoForge inverts the NeoForge version scheme: NeoForge drops
// the leading "1." and uses 0 for a missing patch, so 21.4.157 targets 1.21.4
// and 21.0.x targets 1.21.
func minecraftFromNeoForge(version string) (string, bool) {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return "", false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", false
	}
	if minor == 0 {
		return fmt.Sprintf("1.%d", major), true
	}
	return fmt.Sprintf("1.%d.%d", major, minor), true
}

// findNeoForgeArgs looks for the argument file NeoForge's installer writes.
func findNeoForgeArgs(dir string) ([]string, string, bool) {
	base := filepath.Join(dir, "libraries", "net", "neoforged", "neoforge")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, f := range []string{"win_args.txt", "unix_args.txt"} {
			if _, err := os.Stat(filepath.Join(base, e.Name(), f)); err == nil {
				return []string{fmt.Sprintf(
					"@libraries/net/neoforged/neoforge/%s/{platform}_args.txt", e.Name())}, e.Name(), true
			}
		}
	}
	return nil, "", false
}

// VersionMismatch reports a configured Minecraft version that disagrees with
// what the server directory actually contains, which usually means the
// manifest was edited without reinstalling, or a jar was swapped by hand.
// It returns an empty string when they agree or nothing could be detected.
func VersionMismatch(cfg *config.Config) string {
	d := Inspect(cfg.Dir())
	if d.Minecraft == "" || cfg.Minecraft == "" || d.Minecraft == cfg.Minecraft {
		return ""
	}
	return fmt.Sprintf("%s says %s, but %s reports %s",
		config.ManifestName, cfg.Minecraft, d.MinecraftFrom, d.Minecraft)
}

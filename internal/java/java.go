// Package java finds out which Java a server needs, which one it is actually
// using, and installs a standalone runtime when those disagree.
//
// Minecraft raises its Java requirement every few releases, and the failure
// mode is an UnsupportedClassVersionError deep in a stack trace that says
// nothing about Minecraft or Java versions in terms anyone recognises. This
// package turns that into a plain statement of which version is needed.
package java

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"mcmgr/internal/httpx"
)

// classFileBase is the offset between a class file format version and the Java
// release that produces it: class file 65 is Java 21, 69 is Java 25.
const classFileBase = 44

// Info describes one Java installation.
type Info struct {
	// Path is the java binary, as invoked.
	Path string
	// Major is the feature release: 8, 17, 21, 25.
	Major int
	// Version is the full version string the runtime reported.
	Version string
	// Runtime names the build, e.g. "Temurin-21.0.12+7".
	Runtime string
}

// String renders the version for display.
func (i Info) String() string {
	if i.Version == "" {
		return "unknown"
	}
	if i.Runtime != "" {
		return fmt.Sprintf("%s (%s)", i.Version, i.Runtime)
	}
	return i.Version
}

// versionPattern matches the first line of `java -version`, which is quoted:
//
//	openjdk version "21.0.12" 2024-07-16
//	java version "1.8.0_402"
var versionPattern = regexp.MustCompile(`version "([^"]+)"`)

// runtimePattern matches the build name on the second line.
var runtimePattern = regexp.MustCompile(`Runtime Environment\s+\(?([^\s)(]+)`)

// Detect asks a Java binary what version it is.
//
// `java -version` writes to stderr, not stdout, which is why both are captured.
func Detect(bin string) (Info, error) {
	if bin == "" {
		bin = "java"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return Info{}, fmt.Errorf("java not found (looked for %q)", bin)
	}

	out, err := exec.Command(resolved, "-version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return Info{}, fmt.Errorf("running %s -version: %w", resolved, err)
	}

	info := Info{Path: resolved}
	text := string(out)
	m := versionPattern.FindStringSubmatch(text)
	if m == nil {
		return info, fmt.Errorf("could not read a version from %s -version:\n%s", resolved, strings.TrimSpace(text))
	}
	info.Version = m[1]
	info.Major = majorFromVersion(m[1])
	if r := runtimePattern.FindStringSubmatch(text); r != nil {
		info.Runtime = strings.TrimSuffix(r[1], ",")
	}
	if info.Major == 0 {
		return info, fmt.Errorf("could not read a major version from %q", m[1])
	}
	return info, nil
}

// majorFromVersion turns a Java version string into its feature release.
// Java 8 and earlier report "1.8.0_402", where the major is the second field.
func majorFromVersion(v string) int {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == '+' })
	if len(parts) == 0 {
		return 0
	}
	first, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	if first == 1 && len(parts) > 1 {
		if second, err := strconv.Atoi(parts[1]); err == nil {
			return second
		}
	}
	return first
}

// mojangManifest is the index of every Minecraft version.
const mojangManifest = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type manifestDoc struct {
	Versions []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"versions"`
}

type versionDoc struct {
	JavaVersion struct {
		Component    string `json:"component"`
		MajorVersion int    `json:"majorVersion"`
	} `json:"javaVersion"`
}

var (
	requiredMu    sync.Mutex
	requiredCache = map[string]int{}
)

// Required reports the Java feature release Mojang says a Minecraft version
// needs. Mojang publishes this per version, so it never has to be guessed.
func Required(mcVersion string) (int, error) {
	if mcVersion == "" {
		return 0, fmt.Errorf("no Minecraft version given")
	}

	requiredMu.Lock()
	if v, ok := requiredCache[mcVersion]; ok {
		requiredMu.Unlock()
		return v, nil
	}
	requiredMu.Unlock()

	var manifest manifestDoc
	if err := httpx.GetJSON(mojangManifest, &manifest, nil); err != nil {
		return 0, fmt.Errorf("fetching the Minecraft version manifest: %w", err)
	}
	var url string
	for _, v := range manifest.Versions {
		if v.ID == mcVersion {
			url = v.URL
			break
		}
	}
	if url == "" {
		return 0, fmt.Errorf("Minecraft version %q is not in Mojang's manifest", mcVersion)
	}

	var doc versionDoc
	if err := httpx.GetJSON(url, &doc, nil); err != nil {
		return 0, err
	}
	if doc.JavaVersion.MajorVersion == 0 {
		return 0, fmt.Errorf("Mojang does not state a Java version for Minecraft %s", mcVersion)
	}

	requiredMu.Lock()
	requiredCache[mcVersion] = doc.JavaVersion.MajorVersion
	requiredMu.Unlock()
	return doc.JavaVersion.MajorVersion, nil
}

// classVersionPattern matches the error a JVM raises when asked to load classes
// built for a newer release:
//
//	has been compiled by a more recent version of the Java Runtime (class file
//	version 69.0), this version of the Java Runtime only recognizes class file
//	versions up to 65.0
var classVersionPattern = regexp.MustCompile(
	`class file version (\d+)\.\d+\)?,\s*this version of the Java Runtime only recognizes class file versions up to (\d+)\.`)

// Mismatch is a Java version that is too old for what it was asked to run.
type Mismatch struct {
	// Need and Have are Java feature releases, already converted from the
	// class file versions in the error.
	Need int
	Have int
}

func (m Mismatch) Error() string {
	return fmt.Sprintf("this server needs Java %d, but Java %d is being used", m.Need, m.Have)
}

// ParseClassVersionError extracts a version mismatch from server output.
// It reports ok=false when the output contains no such error.
func ParseClassVersionError(output string) (Mismatch, bool) {
	m := classVersionPattern.FindStringSubmatch(output)
	if m == nil {
		return Mismatch{}, false
	}
	need, err1 := strconv.Atoi(m[1])
	have, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return Mismatch{}, false
	}
	return Mismatch{Need: need - classFileBase, Have: have - classFileBase}, true
}

package java

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// Distribution is which build of Java mcmgr downloads.
//
// Temurin is the default: free, no account, and the usual choice for a
// standalone JVM. GraalVM is Oracle's build with the Graal JIT compiler in
// place of C2, which some servers run measurably faster on. It is free to use
// under the GraalVM Free Terms and Conditions, and only published for Java 17
// and newer.
type Distribution string

const (
	Temurin Distribution = "temurin"
	GraalVM Distribution = "graalvm"
)

// Distributions lists every distribution mcmgr can download, default first.
var Distributions = []Distribution{Temurin, GraalVM}

// graalMinMajor is the first release Oracle publishes GraalVM builds for.
const graalMinMajor = 17

// ParseDistribution accepts the names people use for each distribution. An
// empty string is the default, Temurin.
func ParseDistribution(s string) (Distribution, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "temurin", "adoptium", "eclipse":
		return Temurin, nil
	case "graalvm", "graal", "oracle":
		return GraalVM, nil
	}
	return "", fmt.Errorf("unknown Java distribution %q; expected temurin or graalvm", s)
}

// Label is the distribution's name for display.
func (d Distribution) Label() string {
	switch d {
	case GraalVM:
		return "Oracle GraalVM"
	case Temurin:
		return "Eclipse Temurin"
	}
	return string(d)
}

// Supports reports whether the distribution publishes builds of a release at
// all, before any network is touched.
func (d Distribution) Supports(major int) bool {
	if d == GraalVM {
		return major >= graalMinMajor
	}
	return true
}

// Runtime names one downloadable runtime: a distribution at a feature release.
type Runtime struct {
	Dist  Distribution
	Major int
}

// String is the spec form `mcmgr venv remove` accepts, e.g. "graalvm-21". A
// Temurin runtime is just its number, as it was before there was a choice.
func (r Runtime) String() string {
	if r.Dist == Temurin || r.Dist == "" {
		return strconv.Itoa(r.Major)
	}
	return fmt.Sprintf("%s-%d", r.Dist, r.Major)
}

// ParseRuntime reads a spec such as "21", "graalvm-21" or "temurin-21".
func ParseRuntime(spec string) (Runtime, error) {
	name, num, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		num, name = name, ""
	}
	major, err := strconv.Atoi(num)
	if err != nil || major < 8 {
		return Runtime{}, fmt.Errorf("expected a Java feature release such as 21 or graalvm-21, got %q", spec)
	}
	dist, err := ParseDistribution(name)
	if err != nil {
		return Runtime{}, err
	}
	return Runtime{Dist: dist, Major: major}, nil
}

// dirName is the directory a runtime is installed under in the store. Temurin
// keeps the bare number it always had, so runtimes downloaded before GraalVM
// was an option are still found.
func (r Runtime) dirName() string { return r.String() }

// runtimeFromDir is the inverse of dirName, reporting ok=false for anything
// else that ended up in the store.
func runtimeFromDir(name string) (Runtime, bool) {
	r, err := ParseRuntime(name)
	if err != nil {
		return Runtime{}, false
	}
	// ParseRuntime tolerates spellings dirName never produces.
	if r.dirName() != name {
		return Runtime{}, false
	}
	return r, true
}

// graalDownload is where Oracle publishes the current build of a GraalVM
// release for this platform, with the archive's file name. Oracle keeps a
// versionless "latest" path per release, and a .sha256 beside every archive,
// so no index has to be consulted.
func graalDownload(major int) (url, name string, err error) {
	var osName, ext string
	switch runtime.GOOS {
	case "linux":
		osName, ext = "linux", "tar.gz"
	case "darwin":
		osName, ext = "macos", "tar.gz"
	case "windows":
		osName, ext = "windows", "zip"
	default:
		return "", "", fmt.Errorf("no GraalVM builds for %s", runtime.GOOS)
	}
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", "", fmt.Errorf("no GraalVM builds for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	name = fmt.Sprintf("graalvm-jdk-%d_%s-%s_bin.%s", major, osName, arch, ext)
	return fmt.Sprintf("%s/%d/latest/%s", graalDownloads, major, name), name, nil
}

// graalDownloads is Oracle's download host for GraalVM.
const graalDownloads = "https://download.oracle.com/graalvm"

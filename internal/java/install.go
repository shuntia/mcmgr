package java

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"mcmgr/internal/httpx"
	"mcmgr/internal/paths"
	"mcmgr/internal/ui"
)

// adoptiumAPI serves Eclipse Temurin builds: free, no account, and the usual
// choice for a standalone JVM.
const adoptiumAPI = "https://api.adoptium.net/v3"

// asset is one Adoptium download.
type asset struct {
	Binary struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		ImageType    string `json:"image_type"`
		Package      struct {
			Name     string `json:"name"`
			Link     string `json:"link"`
			Checksum string `json:"checksum"`
			Size     int64  `json:"size"`
		} `json:"package"`
	} `json:"binary"`
	ReleaseName string `json:"release_name"`
}

// platform maps Go's names onto Adoptium's.
func platform() (osName, arch string, err error) {
	switch runtime.GOOS {
	case "windows":
		osName = "windows"
	case "linux":
		osName = "linux"
	case "darwin":
		osName = "mac"
	default:
		return "", "", fmt.Errorf("no Temurin builds for %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", "", fmt.Errorf("no Temurin builds for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return osName, arch, nil
}

// StoreDir is where downloaded runtimes live. They are shared between servers:
// a JRE is a hundred megabytes and identical for every server that needs the
// same release, so one copy per distribution and major version is kept rather
// than one per server.
func StoreDir() (string, error) {
	base, err := paths.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "java"), nil
}

// dirFor is where a given runtime is installed.
func dirFor(r Runtime) (string, error) {
	store, err := StoreDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(store, r.dirName()), nil
}

// BinaryName is the java executable's file name on this platform.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "java.exe"
	}
	return "java"
}

// Find returns the java binary of an already-installed managed runtime.
func Find(r Runtime) (string, bool) {
	dir, err := dirFor(r)
	if err != nil {
		return "", false
	}
	bin := filepath.Join(dir, "bin", BinaryName())
	if fi, err := os.Stat(bin); err == nil && !fi.IsDir() {
		return bin, true
	}
	return "", false
}

// FindAny returns a managed runtime of the given release, trying the
// distributions in the order given, so a caller can prefer one without
// refusing another that is already downloaded.
func FindAny(major int, prefer ...Distribution) (string, Runtime, bool) {
	order := append([]Distribution{}, prefer...)
	for _, d := range Distributions {
		order = append(order, d)
	}
	for _, d := range order {
		r := Runtime{Dist: d, Major: major}
		if bin, ok := Find(r); ok {
			return bin, r, true
		}
	}
	return "", Runtime{}, false
}

// Installed lists the managed runtimes, newest major first, Temurin before
// GraalVM within a release.
func Installed() []Info {
	store, err := StoreDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(store)
	if err != nil {
		return nil
	}

	var out []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, ok := runtimeFromDir(e.Name())
		if !ok {
			continue
		}
		bin, ok := Find(r)
		if !ok {
			continue
		}
		info, derr := Detect(bin)
		if derr != nil {
			info = Info{Path: bin, Major: r.Major}
		}
		// The directory says what was downloaded; -version output is only a
		// guess at it.
		info.Dist = r.Dist
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Major != out[j].Major {
			return out[i].Major > out[j].Major
		}
		return distRank(out[i].Dist) < distRank(out[j].Dist)
	})
	return out
}

func distRank(d Distribution) int {
	for i, known := range Distributions {
		if known == d {
			return i
		}
	}
	return len(Distributions)
}

// Remove deletes a managed runtime.
func Remove(r Runtime) error {
	dir, err := dirFor(r)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no managed %s %d is installed", r.Dist.Label(), r.Major)
	}
	return os.RemoveAll(dir)
}

// download is one archive to fetch: where from, what to call it, its digest
// when the publisher states one, and its size when known (0 otherwise).
type download struct {
	url, name, sum string
	size           int64
	release        string
}

// Install downloads a runtime and returns the path to its java binary. An
// already-installed runtime is reused.
func Install(r Runtime) (string, error) {
	if r.Dist == "" {
		r.Dist = Temurin
	}
	if bin, ok := Find(r); ok {
		ui.Info("  %s %d is already installed", r.Dist.Label(), r.Major)
		return bin, nil
	}
	if !r.Dist.Supports(r.Major) {
		return "", fmt.Errorf("%s is only published for Java %d and newer, and this needs Java %d\n"+
			"       use Temurin instead: mcmgr venv install %d", r.Dist.Label(), graalMinMajor, r.Major, r.Major)
	}

	var dl download
	var err error
	switch r.Dist {
	case GraalVM:
		dl, err = graalArchive(r.Major)
	default:
		dl, err = temurinArchive(r.Major)
	}
	if err != nil {
		return "", err
	}

	dir, err := dirFor(r)
	if err != nil {
		return "", err
	}
	store, err := StoreDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(store, 0o755); err != nil {
		return "", err
	}

	archive := filepath.Join(store, dl.name)
	if dl.size > 0 {
		ui.Step("downloading %s (%s)", dl.release, ui.Bytes(dl.size))
	} else {
		ui.Step("downloading %s", dl.release)
	}
	if err := httpx.Download(dl.url, archive, dl.sum); err != nil {
		return "", fmt.Errorf("downloading %s %d: %w", r.Dist.Label(), r.Major, err)
	}
	defer os.Remove(archive)

	ui.Step("unpacking %s %d", r.Dist.Label(), r.Major)
	staging := filepath.Join(store, ".unpack-"+r.dirName())
	_ = os.RemoveAll(staging)
	defer os.RemoveAll(staging)

	if err := extract(archive, staging); err != nil {
		return "", fmt.Errorf("unpacking %s %d: %w", r.Dist.Label(), r.Major, err)
	}

	// The archives contain a single top-level directory; move that into place
	// so the layout is <store>/<runtime>/bin/java.
	root, err := findJavaHome(staging)
	if err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(root, dir); err != nil {
		return "", fmt.Errorf("installing %s %d: %w", r.Dist.Label(), r.Major, err)
	}

	bin := filepath.Join(dir, "bin", BinaryName())
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("unpacked %s %d but found no %s", r.Dist.Label(), r.Major, bin)
	}
	return bin, nil
}

// temurinArchive asks Adoptium for the current Temurin build of a release.
func temurinArchive(major int) (download, error) {
	osName, arch, err := platform()
	if err != nil {
		return download{}, err
	}

	// A JRE is enough to run a server and is roughly half the size; fall back
	// to the full JDK for releases that ship no JRE.
	for _, image := range []string{"jre", "jdk"} {
		u := fmt.Sprintf("%s/assets/latest/%d/hotspot?architecture=%s&image_type=%s&os=%s&vendor=eclipse",
			adoptiumAPI, major, arch, image, osName)

		var assets []asset
		if err := httpx.GetJSON(u, &assets, nil); err != nil {
			return download{}, fmt.Errorf("asking Adoptium for a Java %d build: %w", major, err)
		}
		for _, a := range assets {
			// Take the archive, never the .msi or .pkg installer: mcmgr
			// unpacks a runtime beside its own files rather than installing
			// anything system-wide.
			if isArchive(a.Binary.Package.Name) {
				pkg := a.Binary.Package
				return download{url: pkg.Link, name: pkg.Name, sum: pkg.Checksum,
					size: pkg.Size, release: a.ReleaseName}, nil
			}
		}
	}
	return download{}, fmt.Errorf("Adoptium has no Java %d build for %s/%s", major, osName, arch)
}

// graalArchive locates Oracle's current GraalVM build of a release, and its
// published sha256.
func graalArchive(major int) (download, error) {
	u, name, err := graalDownload(major)
	if err != nil {
		return download{}, err
	}
	sum, err := httpx.GetText(u + ".sha256")
	if err != nil {
		if httpx.NotFound(err) {
			return download{}, fmt.Errorf("Oracle publishes no GraalVM %d build for %s/%s", major, runtime.GOOS, runtime.GOARCH)
		}
		return download{}, fmt.Errorf("asking Oracle for the GraalVM %d checksum: %w", major, err)
	}
	// The file is the bare hex digest, occasionally followed by the file
	// name in the coreutils style.
	sum = strings.Fields(sum)[0]
	if len(sum) != 64 {
		return download{}, fmt.Errorf("unexpected GraalVM %d checksum %q", major, sum)
	}
	return download{url: u, name: name, sum: sum,
		release: fmt.Sprintf("Oracle GraalVM for JDK %d", major)}, nil
}

func isArchive(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".tar.gz")
}

// findJavaHome locates the directory holding bin/java inside a freshly
// unpacked archive, whatever the release named its top-level folder.
func findJavaHome(root string) (string, error) {
	// The common case: exactly one directory, containing bin/java.
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(candidate, "bin", BinaryName())); err == nil {
			return candidate, nil
		}
		// macOS bundles nest it under Contents/Home.
		nested := filepath.Join(candidate, "Contents", "Home")
		if _, err := os.Stat(filepath.Join(nested, "bin", BinaryName())); err == nil {
			return nested, nil
		}
	}
	// Or the archive had no wrapper directory at all.
	if _, err := os.Stat(filepath.Join(root, "bin", BinaryName())); err == nil {
		return root, nil
	}
	return "", fmt.Errorf("no bin/%s found in the downloaded archive", BinaryName())
}

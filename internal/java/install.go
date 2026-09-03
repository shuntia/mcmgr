package java

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
// same release, so one copy per major version is kept rather than one per
// server.
func StoreDir() (string, error) {
	base, err := paths.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "java"), nil
}

// dirFor is where a given major version is installed.
func dirFor(major int) (string, error) {
	store, err := StoreDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(store, strconv.Itoa(major)), nil
}

// BinaryName is the java executable's file name on this platform.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "java.exe"
	}
	return "java"
}

// Find returns the java binary of an already-installed managed runtime.
func Find(major int) (string, bool) {
	dir, err := dirFor(major)
	if err != nil {
		return "", false
	}
	bin := filepath.Join(dir, "bin", BinaryName())
	if fi, err := os.Stat(bin); err == nil && !fi.IsDir() {
		return bin, true
	}
	return "", false
}

// Installed lists the managed runtimes, newest major first.
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
		major, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		bin, ok := Find(major)
		if !ok {
			continue
		}
		info, derr := Detect(bin)
		if derr != nil {
			info = Info{Path: bin, Major: major}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Major > out[j].Major })
	return out
}

// Remove deletes a managed runtime.
func Remove(major int) error {
	dir, err := dirFor(major)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no managed Java %d is installed", major)
	}
	return os.RemoveAll(dir)
}

// Install downloads a Temurin runtime for the given feature release and returns
// the path to its java binary. An already-installed runtime is reused.
func Install(major int) (string, error) {
	if bin, ok := Find(major); ok {
		ui.Info("  Java %d is already installed", major)
		return bin, nil
	}

	osName, arch, err := platform()
	if err != nil {
		return "", err
	}

	// A JRE is enough to run a server and is roughly half the size; fall back
	// to the full JDK for releases that ship no JRE.
	var chosen *asset
	for _, image := range []string{"jre", "jdk"} {
		u := fmt.Sprintf("%s/assets/latest/%d/hotspot?architecture=%s&image_type=%s&os=%s&vendor=eclipse",
			adoptiumAPI, major, arch, image, osName)

		var assets []asset
		if err := httpx.GetJSON(u, &assets, nil); err != nil {
			return "", fmt.Errorf("asking Adoptium for a Java %d build: %w", major, err)
		}
		for i := range assets {
			// Take the archive, never the .msi or .pkg installer: mcmgr
			// unpacks a runtime beside its own files rather than installing
			// anything system-wide.
			if isArchive(assets[i].Binary.Package.Name) {
				chosen = &assets[i]
				break
			}
		}
		if chosen != nil {
			break
		}
	}
	if chosen == nil {
		return "", fmt.Errorf("Adoptium has no Java %d build for %s/%s", major, osName, arch)
	}

	dir, err := dirFor(major)
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

	pkg := chosen.Binary.Package
	archive := filepath.Join(store, pkg.Name)
	ui.Step("downloading %s (%s)", chosen.ReleaseName, ui.Bytes(pkg.Size))
	if err := httpx.Download(pkg.Link, archive, pkg.Checksum); err != nil {
		return "", fmt.Errorf("downloading Java %d: %w", major, err)
	}
	defer os.Remove(archive)

	ui.Step("unpacking Java %d", major)
	staging := filepath.Join(store, ".unpack-"+strconv.Itoa(major))
	_ = os.RemoveAll(staging)
	defer os.RemoveAll(staging)

	if err := extract(archive, staging); err != nil {
		return "", fmt.Errorf("unpacking Java %d: %w", major, err)
	}

	// Temurin archives contain a single top-level directory; move that into
	// place so the layout is <store>/<major>/bin/java.
	root, err := findJavaHome(staging)
	if err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(root, dir); err != nil {
		return "", fmt.Errorf("installing Java %d: %w", major, err)
	}

	bin := filepath.Join(dir, "bin", BinaryName())
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("unpacked Java %d but found no %s", major, bin)
	}
	return bin, nil
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

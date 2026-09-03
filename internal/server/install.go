package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/httpx"
	"mcmgr/internal/ui"
)

// Upstream metadata endpoints.
const (
	mojangManifest = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"
	fabricMeta     = "https://meta.fabricmc.net/v2"
	paperAPI       = "https://fill.papermc.io/v3"
	neoforgeMaven  = "https://maven.neoforged.net"
)

// InstallOptions controls a server installation.
type InstallOptions struct {
	// LoaderVersion pins the loader build; empty means latest stable.
	LoaderVersion string
	// Force re-downloads even when the jar is already present.
	Force bool
}

// Install downloads and configures the server binary for cfg's Minecraft
// version and loader, updating cfg in place. The caller saves the manifest.
func Install(cfg *config.Config, opts InstallOptions) error {
	if cfg.Minecraft == "" {
		return fmt.Errorf("no Minecraft version set; add \"minecraft\" to %s", config.ManifestName)
	}

	switch cfg.Loader {
	case config.Vanilla:
		return installVanilla(cfg, opts)
	case config.Fabric:
		return installFabric(cfg, opts)
	case config.Paper:
		return installPaper(cfg, opts)
	case config.NeoForge:
		return installNeoForge(cfg, opts)
	case config.Quilt:
		return fmt.Errorf("automatic Quilt installation is not supported yet\n" +
			"       install it with the Quilt installer, then point mcmgr at the result:\n" +
			"       mcmgr server set-jar <quilt-server-launch.jar>")
	}
	return fmt.Errorf("unknown loader %q", cfg.Loader)
}

// --- Vanilla -----------------------------------------------------------

type mojangManifestDoc struct {
	Latest struct {
		Release  string `json:"release"`
		Snapshot string `json:"snapshot"`
	} `json:"latest"`
	Versions []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"versions"`
}

type mojangVersionDoc struct {
	Downloads struct {
		Server struct {
			URL  string `json:"url"`
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
		} `json:"server"`
	} `json:"downloads"`
}

// LatestRelease returns the newest released Minecraft version, used when
// `server create` is run without an explicit version.
func LatestRelease() (string, error) {
	var doc mojangManifestDoc
	if err := httpx.GetJSON(mojangManifest, &doc, nil); err != nil {
		return "", fmt.Errorf("fetching the Minecraft version manifest: %w", err)
	}
	if doc.Latest.Release == "" {
		return "", fmt.Errorf("the Minecraft version manifest contained no releases")
	}
	return doc.Latest.Release, nil
}

// VersionExists reports whether a Minecraft version id is known to Mojang.
func VersionExists(version string) (bool, error) {
	var doc mojangManifestDoc
	if err := httpx.GetJSON(mojangManifest, &doc, nil); err != nil {
		return false, err
	}
	for _, v := range doc.Versions {
		if v.ID == version {
			return true, nil
		}
	}
	return false, nil
}

func installVanilla(cfg *config.Config, opts InstallOptions) error {
	var doc mojangManifestDoc
	if err := httpx.GetJSON(mojangManifest, &doc, nil); err != nil {
		return fmt.Errorf("fetching the Minecraft version manifest: %w", err)
	}
	var metaURL string
	for _, v := range doc.Versions {
		if v.ID == cfg.Minecraft {
			metaURL = v.URL
			break
		}
	}
	if metaURL == "" {
		return fmt.Errorf("Minecraft version %q not found in Mojang's manifest", cfg.Minecraft)
	}

	var ver mojangVersionDoc
	if err := httpx.GetJSON(metaURL, &ver, nil); err != nil {
		return err
	}
	if ver.Downloads.Server.URL == "" {
		return fmt.Errorf("Minecraft %s does not publish a server jar", cfg.Minecraft)
	}

	jar := fmt.Sprintf("minecraft_server.%s.jar", cfg.Minecraft)
	dst := filepath.Join(cfg.Dir(), jar)
	if !opts.Force && upToDate(dst, ver.Downloads.Server.SHA1) {
		ui.Info("  server jar already present (%s)", jar)
	} else {
		ui.Step("downloading Minecraft %s server (%s)", cfg.Minecraft, ui.Bytes(ver.Downloads.Server.Size))
		if err := httpx.Download(ver.Downloads.Server.URL, dst, ver.Downloads.Server.SHA1); err != nil {
			return err
		}
	}
	cfg.Jar = jar
	cfg.LaunchArgs = nil
	return nil
}

// --- Fabric ------------------------------------------------------------

type fabricLoaderEntry struct {
	Loader struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	} `json:"loader"`
}

type fabricInstallerEntry struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

func installFabric(cfg *config.Config, opts InstallOptions) error {
	loaderVer := opts.LoaderVersion
	if loaderVer == "" {
		loaderVer = cfg.LoaderVer
	}
	if loaderVer == "" {
		var loaders []fabricLoaderEntry
		u := fmt.Sprintf("%s/versions/loader/%s", fabricMeta, cfg.Minecraft)
		if err := httpx.GetJSON(u, &loaders, nil); err != nil {
			return fmt.Errorf("fetching Fabric loader versions: %w", err)
		}
		if len(loaders) == 0 {
			return fmt.Errorf("Fabric has no loader builds for Minecraft %s", cfg.Minecraft)
		}
		// The list is newest-first; prefer the newest stable build.
		loaderVer = loaders[0].Loader.Version
		for _, l := range loaders {
			if l.Loader.Stable {
				loaderVer = l.Loader.Version
				break
			}
		}
	}

	var installers []fabricInstallerEntry
	if err := httpx.GetJSON(fabricMeta+"/versions/installer", &installers, nil); err != nil {
		return fmt.Errorf("fetching Fabric installer versions: %w", err)
	}
	if len(installers) == 0 {
		return fmt.Errorf("no Fabric installer versions available")
	}
	installerVer := installers[0].Version
	for _, i := range installers {
		if i.Stable {
			installerVer = i.Version
			break
		}
	}

	// This endpoint serves a ready-made launcher jar; it resolves the rest of
	// the loader at first start, so no installer needs to be run.
	u := fmt.Sprintf("%s/versions/loader/%s/%s/%s/server/jar", fabricMeta, cfg.Minecraft, loaderVer, installerVer)
	jar := "fabric-server-launch.jar"
	dst := filepath.Join(cfg.Dir(), jar)

	if !opts.Force && cfg.LoaderVer == loaderVer && fileExists(dst) {
		ui.Info("  Fabric %s launcher already present", loaderVer)
	} else {
		ui.Step("downloading Fabric loader %s for Minecraft %s", loaderVer, cfg.Minecraft)
		if err := httpx.Download(u, dst, ""); err != nil {
			return fmt.Errorf("downloading the Fabric server launcher: %w", err)
		}
	}
	cfg.Jar = jar
	cfg.LoaderVer = loaderVer
	cfg.LaunchArgs = nil
	return nil
}

// --- Paper -------------------------------------------------------------

// paperBuild is a build as returned by the Fill (v3) API, which supplies the
// download URL and checksum directly instead of needing a second request.
type paperBuild struct {
	ID      int    `json:"id"`
	Channel string `json:"channel"`
	// Downloads is keyed by artifact name; the server jar is "server:default".
	Downloads map[string]struct {
		Name      string `json:"name"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
	} `json:"downloads"`
}

func installPaper(cfg *config.Config, opts InstallOptions) error {
	// "latest" resolves the newest build; a pin addresses one by number.
	which := "latest"
	if opts.LoaderVersion != "" {
		var n int
		if _, err := fmt.Sscanf(opts.LoaderVersion, "%d", &n); err != nil {
			return fmt.Errorf("--loader-version for Paper must be a build number, got %q", opts.LoaderVersion)
		}
		which = strconv.Itoa(n)
	}

	var build paperBuild
	u := fmt.Sprintf("%s/projects/paper/versions/%s/builds/%s", paperAPI, cfg.Minecraft, which)
	if err := httpx.GetJSON(u, &build, nil); err != nil {
		if httpx.NotFound(err) {
			return fmt.Errorf("Paper has no build %q for Minecraft %s", which, cfg.Minecraft)
		}
		return fmt.Errorf("fetching Paper builds: %w", err)
	}

	dl, ok := build.Downloads["server:default"]
	if !ok || dl.URL == "" {
		return fmt.Errorf("Paper build %d publishes no server jar", build.ID)
	}

	jar := "paper-server.jar"
	dst := filepath.Join(cfg.Dir(), jar)
	loaderVer := strconv.Itoa(build.ID)

	if !opts.Force && cfg.LoaderVer == loaderVer && fileExists(dst) {
		ui.Info("  Paper build %d already present", build.ID)
	} else {
		ui.Step("downloading Paper %s build %d (%s)", cfg.Minecraft, build.ID, ui.Bytes(dl.Size))
		if err := httpx.Download(dl.URL, dst, dl.Checksums.SHA256); err != nil {
			return err
		}
	}
	cfg.Jar = jar
	cfg.LoaderVer = loaderVer
	cfg.LaunchArgs = nil
	return nil
}

// --- NeoForge ----------------------------------------------------------

type neoforgeVersionsDoc struct {
	Versions []string `json:"versions"`
}

// neoforgePrefix maps a Minecraft version to the NeoForge version series that
// targets it: NeoForge drops the leading "1." and uses 0 for the missing patch,
// so 1.21.4 -> "21.4." and 1.21 -> "21.0.".
func neoforgePrefix(mc string) (string, error) {
	rest, ok := strings.CutPrefix(mc, "1.")
	if !ok {
		return "", fmt.Errorf("NeoForge does not target Minecraft %s", mc)
	}
	parts := strings.Split(rest, ".")
	switch len(parts) {
	case 1:
		return parts[0] + ".0.", nil
	default:
		return parts[0] + "." + parts[1] + ".", nil
	}
}

func installNeoForge(cfg *config.Config, opts InstallOptions) error {
	version := opts.LoaderVersion
	if version == "" {
		prefix, err := neoforgePrefix(cfg.Minecraft)
		if err != nil {
			return err
		}
		var doc neoforgeVersionsDoc
		u := neoforgeMaven + "/api/maven/versions/releases/net%2Fneoforged%2Fneoforge"
		if err := httpx.GetJSON(u, &doc, nil); err != nil {
			return fmt.Errorf("fetching NeoForge versions: %w", err)
		}
		var matches, stable []string
		for _, v := range doc.Versions {
			if strings.HasPrefix(v, prefix) {
				matches = append(matches, v)
				if !strings.Contains(v, "-beta") {
					stable = append(stable, v)
				}
			}
		}
		pick := stable
		if len(pick) == 0 {
			pick = matches
		}
		if len(pick) == 0 {
			return fmt.Errorf("NeoForge has no builds for Minecraft %s", cfg.Minecraft)
		}
		sort.Strings(pick)
		version = pick[len(pick)-1]
	}

	argsFile := filepath.Join(cfg.Dir(), "libraries", "net", "neoforged", "neoforge", version, platformArgsFile())
	if !opts.Force && fileExists(argsFile) {
		ui.Info("  NeoForge %s already installed", version)
	} else {
		installerURL := fmt.Sprintf("%s/releases/net/neoforged/neoforge/%s/neoforge-%s-installer.jar",
			neoforgeMaven, version, version)
		installer := filepath.Join(cfg.StatePath(), fmt.Sprintf("neoforge-%s-installer.jar", version))

		ui.Step("downloading NeoForge %s installer", version)
		if err := httpx.Download(installerURL, installer, ""); err != nil {
			return fmt.Errorf("downloading the NeoForge installer: %w", err)
		}
		defer os.Remove(installer)

		ui.Step("running the NeoForge installer (this downloads libraries and takes a minute)")
		cmd := exec.Command(cfg.JavaBin(), "-jar", installer, "--installServer", cfg.Dir())
		cmd.Dir = cfg.Dir()
		hideConsole(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("the NeoForge installer failed: %w\n%s", err, lastLines(string(out), 15))
		}
		if !fileExists(argsFile) {
			return fmt.Errorf("the NeoForge installer completed but %s was not created", argsFile)
		}
	}

	// NeoForge is launched through an argument file rather than -jar. The file
	// name differs per OS, so {platform} is substituted at launch time and the
	// manifest stays portable between machines.
	cfg.Jar = ""
	cfg.LoaderVer = version
	cfg.LaunchArgs = []string{
		fmt.Sprintf("@libraries/net/neoforged/neoforge/%s/{platform}_args.txt", version),
	}
	return nil
}

// platformArgsFile is the NeoForge argument file for the current OS.
func platformArgsFile() string {
	if runtime.GOOS == "windows" {
		return "win_args.txt"
	}
	return "unix_args.txt"
}

// --- EULA --------------------------------------------------------------

// AcceptEULA writes eula.txt marking the Minecraft EULA as accepted. It is
// only ever called from an explicit user action.
func AcceptEULA(dir string) error {
	content := "# Accepted via mcmgr. By changing this to true you agree to the Minecraft EULA:\n" +
		"# https://aka.ms/MinecraftEULA\n" +
		"eula=true\n"
	return os.WriteFile(filepath.Join(dir, "eula.txt"), []byte(content), 0o644)
}

// EULAAccepted reports whether eula.txt exists and records acceptance.
//
// This is a stricter question than checkEULA asks: a missing eula.txt does not
// block a launch, because the server writes one itself on first run, but it
// does mean the EULA has not been accepted yet.
func EULAAccepted(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "eula.txt"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(strings.ToLower(l)) == "eula=true" {
			return true
		}
	}
	return false
}

// --- helpers -----------------------------------------------------------

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// upToDate reports whether a file exists and already matches the expected sha1.
func upToDate(path, sha1 string) bool {
	if !fileExists(path) {
		return false
	}
	if sha1 == "" {
		return true
	}
	got, err := httpx.FileSHA1(path)
	return err == nil && strings.EqualFold(got, sha1)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

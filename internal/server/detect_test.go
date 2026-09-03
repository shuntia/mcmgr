package server

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcmgr/internal/config"
)

// writeJar builds a jar containing the given entries. The layouts mirror real
// server jars: Fabric's launcher carries install.properties, and Mojang's
// bundler and Paper's Paperclip both carry version.json at the root.
func writeJar(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

const realVersionJSON = `{
    "id": "1.21.4",
    "name": "1.21.4",
    "world_version": 4189,
    "protocol_version": 769
}`

func TestDetectFabricFromInstallProperties(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "fabric-server-launch.jar"), map[string]string{
		"install.properties":   "fabric-loader-version=0.16.14\ngame-version=1.21.4",
		"META-INF/MANIFEST.MF": "Main-Class: net.fabricmc.installer.ServerLauncher\n",
	})

	d := Inspect(dir)
	if d.Minecraft != "1.21.4" {
		t.Errorf("Minecraft = %q, want 1.21.4", d.Minecraft)
	}
	if d.Loader != config.Fabric {
		t.Errorf("Loader = %q, want fabric", d.Loader)
	}
	if d.LoaderVer != "0.16.14" {
		t.Errorf("LoaderVer = %q, want 0.16.14", d.LoaderVer)
	}
	if !strings.Contains(d.MinecraftFrom, "install.properties") {
		t.Errorf("MinecraftFrom = %q, want it to name install.properties", d.MinecraftFrom)
	}
}

func TestDetectQuiltFromInstallProperties(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "quilt-server-launch.jar"), map[string]string{
		"install.properties": "quilt-loader-version=0.26.0\ngame-version=1.21.1",
	})

	d := Inspect(dir)
	if d.Loader != config.Quilt || d.LoaderVer != "0.26.0" || d.Minecraft != "1.21.1" {
		t.Errorf("got loader=%q ver=%q mc=%q, want quilt/0.26.0/1.21.1", d.Loader, d.LoaderVer, d.Minecraft)
	}
}

func TestDetectVanillaFromVersionJSON(t *testing.T) {
	dir := t.TempDir()
	// Deliberately a name that carries no version, so only the jar contents
	// can supply it.
	writeJar(t, filepath.Join(dir, "server.jar"), map[string]string{
		"version.json": realVersionJSON,
	})

	d := Inspect(dir)
	if d.Minecraft != "1.21.4" {
		t.Errorf("Minecraft = %q, want 1.21.4", d.Minecraft)
	}
	if d.Loader != config.Vanilla {
		t.Errorf("Loader = %q, want vanilla", d.Loader)
	}
	if d.Jar != "server.jar" {
		t.Errorf("Jar = %q, want server.jar", d.Jar)
	}
}

func TestDetectPaperFromVersionJSON(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "paper-server.jar"), map[string]string{
		"version.json": realVersionJSON,
	})

	d := Inspect(dir)
	if d.Minecraft != "1.21.4" {
		t.Errorf("Minecraft = %q, want 1.21.4", d.Minecraft)
	}
	if d.Loader != config.Paper {
		t.Errorf("Loader = %q, want paper", d.Loader)
	}
	// Paper add-ons live in plugins/, which the loader choice must imply.
	if d.Loader.ContentDir() != "plugins" {
		t.Errorf("ContentDir = %q, want plugins", d.Loader.ContentDir())
	}
}

// TestRenamedJarStillDetected is the point of reading jar contents: a file
// name tells you nothing once someone renames it.
func TestRenamedJarStillDetected(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "my-cool-server.jar"), map[string]string{
		"version.json": realVersionJSON,
	})

	d := Inspect(dir)
	if d.Minecraft != "1.21.4" {
		t.Errorf("a renamed jar should still report its version, got %q", d.Minecraft)
	}
	if d.Jar != "my-cool-server.jar" {
		t.Errorf("Jar = %q, want my-cool-server.jar", d.Jar)
	}
}

func TestDetectNeoForgeFromLayout(t *testing.T) {
	dir := t.TempDir()
	argsDir := filepath.Join(dir, "libraries", "net", "neoforged", "neoforge", "21.4.157")
	if err := os.MkdirAll(argsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"win_args.txt", "unix_args.txt"} {
		if err := os.WriteFile(filepath.Join(argsDir, f), []byte("-cp x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	d := Inspect(dir)
	if d.Loader != config.NeoForge {
		t.Errorf("Loader = %q, want neoforge", d.Loader)
	}
	if d.LoaderVer != "21.4.157" {
		t.Errorf("LoaderVer = %q, want 21.4.157", d.LoaderVer)
	}
	// NeoForge's own version implies the Minecraft version.
	if d.Minecraft != "1.21.4" {
		t.Errorf("Minecraft = %q, want 1.21.4 derived from NeoForge 21.4.157", d.Minecraft)
	}
	if len(d.LaunchArgs) != 1 || !strings.Contains(d.LaunchArgs[0], "{platform}") {
		t.Errorf("LaunchArgs = %v, want a {platform} arg file", d.LaunchArgs)
	}
}

func TestMinecraftFromNeoForge(t *testing.T) {
	cases := map[string]string{
		"21.4.157":    "1.21.4",
		"21.0.42":     "1.21",
		"20.6.119":    "1.20.6",
		"21.4.1-beta": "1.21.4",
	}
	for in, want := range cases {
		got, ok := minecraftFromNeoForge(in)
		if !ok || got != want {
			t.Errorf("minecraftFromNeoForge(%q) = (%q, %v), want %q", in, got, ok, want)
		}
	}
	if _, ok := minecraftFromNeoForge("nonsense"); ok {
		t.Error("a non-numeric NeoForge version should not resolve")
	}
}

func TestDetectFromPaperVersionHistory(t *testing.T) {
	dir := t.TempDir()
	// A jar with no usable metadata, so the directory fallback has to work.
	writeJar(t, filepath.Join(dir, "server.jar"), map[string]string{"a.class": "x"})
	history := `{"currentVersion": "git-Paper-232 (MC: 1.21.4)"}`
	if err := os.WriteFile(filepath.Join(dir, "version_history.json"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}

	d := Inspect(dir)
	if d.Minecraft != "1.21.4" {
		t.Errorf("Minecraft = %q, want 1.21.4 from version_history.json", d.Minecraft)
	}
}

func TestDetectFromLogs(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "server.jar"), map[string]string{"a.class": "x"})
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `[12:00:00] [main/INFO]: Environment: authHost='https://authserver.mojang.com'
[12:00:01] [Server thread/INFO]: Starting minecraft server version 1.20.6
[12:00:01] [Server thread/INFO]: Loading properties
`
	if err := os.WriteFile(filepath.Join(logs, "latest.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	d := Inspect(dir)
	if d.Minecraft != "1.20.6" {
		t.Errorf("Minecraft = %q, want 1.20.6 read from the log", d.Minecraft)
	}
	if !strings.Contains(d.MinecraftFrom, "latest.log") {
		t.Errorf("MinecraftFrom = %q, want it to name the log", d.MinecraftFrom)
	}
}

func TestDetectFromJarNameLastResort(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "minecraft_server.1.19.2.jar"), map[string]string{"a.class": "x"})

	d := Inspect(dir)
	if d.Minecraft != "1.19.2" {
		t.Errorf("Minecraft = %q, want 1.19.2 from the file name", d.Minecraft)
	}
	if !strings.Contains(d.MinecraftFrom, "file name") {
		t.Errorf("MinecraftFrom = %q, should admit it is only a file name", d.MinecraftFrom)
	}
}

// TestJarContentsBeatFileName checks the precedence: a jar whose name says one
// version and whose contents say another must be believed on its contents.
func TestJarContentsBeatFileName(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "minecraft_server.1.19.2.jar"), map[string]string{
		"version.json": realVersionJSON, // says 1.21.4
	})

	d := Inspect(dir)
	if d.Minecraft != "1.21.4" {
		t.Errorf("Minecraft = %q, want 1.21.4 from the jar contents, not the name", d.Minecraft)
	}
}

func TestDetectNothingInEmptyDir(t *testing.T) {
	d := Inspect(t.TempDir())
	if d.Minecraft != "" || d.Loader != "" || d.Jar != "" {
		t.Errorf("an empty directory should detect nothing, got %+v", d)
	}
}

func TestDetectCountsAddOnsAndWorld(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "fabric-server-launch.jar"), map[string]string{
		"install.properties": "fabric-loader-version=0.16.14\ngame-version=1.21.4",
	})
	if err := os.MkdirAll(filepath.Join(dir, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"a.jar", "b.jar", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, "mods", m), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "world"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := Inspect(dir)
	if d.AddOns != 2 {
		t.Errorf("AddOns = %d, want 2 (non-jar files do not count)", d.AddOns)
	}
	if d.World != "world" {
		t.Errorf("World = %q, want world", d.World)
	}
}

func TestVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "server.jar"), map[string]string{
		"version.json": realVersionJSON, // 1.21.4
	})

	cfg := &config.Config{Name: "t", Minecraft: "1.21.4", Loader: config.Vanilla}
	cfg.SetDir(dir)
	if got := VersionMismatch(cfg); got != "" {
		t.Errorf("matching versions should report nothing, got %q", got)
	}

	cfg.Minecraft = "1.20.1"
	got := VersionMismatch(cfg)
	if got == "" {
		t.Fatal("a disagreeing manifest should be reported")
	}
	if !strings.Contains(got, "1.20.1") || !strings.Contains(got, "1.21.4") {
		t.Errorf("the message should name both versions, got %q", got)
	}

	// Nothing detectable means nothing to disagree with.
	empty := &config.Config{Name: "t", Minecraft: "1.20.1"}
	empty.SetDir(t.TempDir())
	if got := VersionMismatch(empty); got != "" {
		t.Errorf("undetectable version should not report a mismatch, got %q", got)
	}
}

// TestLoaderFromManifestBeatsFileName covers identifying the platform from the
// jar's launcher class, which is what makes a renamed Paper or vanilla jar
// still recognisable.
func TestLoaderFromManifestBeatsFileName(t *testing.T) {
	cases := []struct {
		mainClass string
		want      config.Loader
	}{
		{"io.papermc.paperclip.Main", config.Paper},
		{"net.minecraft.bundler.Main", config.Vanilla},
		{"net.fabricmc.installer.ServerLauncher", config.Fabric},
		{"org.quiltmc.installer.ServerLauncher", config.Quilt},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeJar(t, filepath.Join(dir, "totally-unhelpful-name.jar"), map[string]string{
			"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\nMain-Class: " + c.mainClass + "\n",
			"version.json":         realVersionJSON,
		})

		d := Inspect(dir)
		if d.Loader != c.want {
			t.Errorf("Main-Class %s: loader = %q, want %q", c.mainClass, d.Loader, c.want)
		}
		if !strings.Contains(d.LoaderFrom, "manifest") {
			t.Errorf("Main-Class %s: LoaderFrom = %q, want it to credit the manifest", c.mainClass, d.LoaderFrom)
		}
	}
}

// TestUnidentifiableJarDetectsNothingConfidently makes sure an unknown jar is
// reported as undetected rather than guessed at.
func TestUnidentifiableJarDetectsNothingConfidently(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "mystery.jar"), map[string]string{
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\nMain-Class: com.example.Whatever\n",
	})

	d := Inspect(dir)
	if d.Minecraft != "" {
		t.Errorf("Minecraft = %q, want no version invented", d.Minecraft)
	}
}

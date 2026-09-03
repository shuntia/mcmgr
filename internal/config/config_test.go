package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &Config{
		Name:       "survival",
		Minecraft:  "1.21.4",
		Loader:     Fabric,
		LoaderVer:  "0.19.5",
		Jar:        "fabric-server-launch.jar",
		ServerArgs: []string{"nogui"},
		Java:       Java{Args: []string{"-Xmx4G"}},
		Mods: []ModSpec{
			{ID: "fabric-api", Provider: "modrinth", Version: "*", Name: "Fabric API"},
			{ID: "sodium", Provider: "modrinth", Version: "mc1.21.4-0.6.13", Name: "Sodium"},
		},
	}
	original.SetDir(dir)
	if err := original.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Name != "survival" || reloaded.Minecraft != "1.21.4" || reloaded.Loader != Fabric {
		t.Errorf("scalars did not survive: %+v", reloaded)
	}
	if len(reloaded.Java.Args) != 1 || reloaded.Java.Args[0] != "-Xmx4G" {
		t.Errorf("java args did not survive: %v", reloaded.Java.Args)
	}
	if len(reloaded.Mods) != 2 {
		t.Fatalf("expected 2 mods, got %d", len(reloaded.Mods))
	}
	if !reloaded.Mods[1].Pinned() {
		t.Error("the pinned mod should still report as pinned")
	}
	if reloaded.Mods[0].Pinned() {
		t.Error(`a mod tracking "*" should not report as pinned`)
	}
}

// TestHandEditedTOML is the reason the manifest is TOML: people edit it.
func TestHandEditedTOML(t *testing.T) {
	dir := t.TempDir()
	handWritten := `
# my server
name = "hand"
minecraft = "1.21.4"
loader = "paper"
server_args = ["nogui", "--forceUpgrade"]

# an inline table, which is also valid TOML
java = { path = "/opt/jdk/bin/java", args = ["-Xmx8G"] }

[[mods]]
id = "worldedit"
provider = "modrinth"
`
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("hand-written TOML should parse: %v", err)
	}
	if cfg.Loader != Paper {
		t.Errorf("loader = %q, want paper", cfg.Loader)
	}
	if cfg.Java.Path != "/opt/jdk/bin/java" {
		t.Errorf("inline table did not parse: java.path = %q", cfg.Java.Path)
	}
	if len(cfg.ServerArgs) != 2 {
		t.Errorf("server_args = %v, want 2 entries", cfg.ServerArgs)
	}
	if len(cfg.Mods) != 1 || cfg.Mods[0].ID != "worldedit" {
		t.Errorf("mods = %+v", cfg.Mods)
	}
	// Paper keeps add-ons in plugins/, not mods/.
	if filepath.Base(cfg.ContentPath()) != "plugins" {
		t.Errorf("ContentPath = %q, want it to end in plugins", cfg.ContentPath())
	}
}

func TestInvalidTOMLReportsTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("name = \nbroken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("malformed TOML should be an error")
	}
	if !strings.Contains(err.Error(), ManifestName) {
		t.Errorf("the error should name the file, got: %v", err)
	}
}

// TestMigratesLegacyJSON checks that servers created before the TOML manifest
// keep working and are converted in place.
func TestMigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := `{
  "name": "old",
  "minecraft": "1.21.4",
  "loader": "fabric",
  "jar": "fabric-server-launch.jar",
  "java": {"args": ["-Xmx2G"]},
  "mods": [{"id": "sodium", "provider": "modrinth", "version": "*"}]
}`
	if err := os.WriteFile(filepath.Join(dir, LegacyManifestName), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("a legacy JSON manifest should load: %v", err)
	}
	if cfg.Name != "old" || cfg.Loader != Fabric || len(cfg.Mods) != 1 {
		t.Errorf("legacy values did not carry over: %+v", cfg)
	}

	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Error("migration should have written mcmgr.toml")
	}
	if _, err := os.Stat(filepath.Join(dir, LegacyManifestName+".bak")); err != nil {
		t.Error("the original JSON should be kept as a .bak rather than deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, LegacyManifestName)); !os.IsNotExist(err) {
		t.Error("the original JSON should have been renamed away")
	}

	// A second load must come from the TOML and be identical.
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != cfg.Name || len(again.Mods) != len(cfg.Mods) {
		t.Error("reloading after migration produced a different config")
	}
}

func TestExistsAcceptsEitherFormat(t *testing.T) {
	toml := t.TempDir()
	os.WriteFile(filepath.Join(toml, ManifestName), []byte(`name = "a"`), 0o644)
	if !Exists(toml) {
		t.Error("a directory with mcmgr.toml should count as a server")
	}

	json := t.TempDir()
	os.WriteFile(filepath.Join(json, LegacyManifestName), []byte(`{"name":"a"}`), 0o644)
	if !Exists(json) {
		t.Error("a directory with the legacy mcmgr.json should count as a server")
	}

	if Exists(t.TempDir()) {
		t.Error("an empty directory is not a server")
	}
}

func TestLoaderContentDirs(t *testing.T) {
	cases := map[Loader]string{
		Fabric:   "mods",
		NeoForge: "mods",
		Quilt:    "mods",
		Paper:    "plugins",
	}
	for loader, want := range cases {
		if got := loader.ContentDir(); got != want {
			t.Errorf("%s.ContentDir() = %q, want %q", loader, got, want)
		}
	}
	if Vanilla.SupportsMods() {
		t.Error("vanilla cannot load mods")
	}
	if !Fabric.SupportsMods() {
		t.Error("fabric can load mods")
	}
	if Paper.ContentNoun() != "plugin" {
		t.Error("Paper add-ons should be called plugins")
	}
}

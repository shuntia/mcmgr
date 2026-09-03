package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcmgr/internal/config"
	"mcmgr/internal/warn"
)

// newTestStore builds a server with its own config and cache directories so
// tests never touch the real machine-wide settings.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MCMGR_HOME", home)

	dir := t.TempDir()
	cfg := &config.Config{
		Name:      "test",
		Minecraft: "1.21.4",
		Loader:    config.Fabric,
		Java:      config.Java{Args: []string{"-Xmx2G", "-XX:+UseG1GC"}},
	}
	cfg.SetDir(dir)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestResolveAliases(t *testing.T) {
	store := newTestStore(t)

	cases := map[string]string{
		"whitelist":             "properties.white-list",
		"white-list":            "properties.white-list",
		"properties.white-list": "properties.white-list",
		"port":                  "properties.server-port",
		"motd":                  "properties.motd", // bare known properties key
		"online-mode":           "properties.online-mode",
		"memory":                "memory",
		"xmx":                   "memory",
		"minecraft":             "minecraft",
		"curseforge":            "global.curseforge_key",
	}
	for input, want := range cases {
		key, err := store.Resolve(input)
		if err != nil {
			t.Errorf("Resolve(%q): %v", input, err)
			continue
		}
		if key.Name != want {
			t.Errorf("Resolve(%q) = %q, want %q", input, key.Name, want)
		}
	}

	if _, err := store.Resolve("not-a-setting"); err == nil {
		t.Error("an unknown key should be rejected")
	}
}

// TestMemorySugar covers the "memory" key, which is not stored directly but
// projected onto the -Xmx JVM flag.
func TestMemorySugar(t *testing.T) {
	store := newTestStore(t)
	key, err := store.Resolve("memory")
	if err != nil {
		t.Fatal(err)
	}

	if got := store.Get(key); got != "2G" {
		t.Errorf("memory = %q, want 2G", got)
	}
	if err := store.Set(key, "8G"); err != nil {
		t.Fatal(err)
	}
	if got := store.Get(key); got != "8G" {
		t.Errorf("after set, memory = %q, want 8G", got)
	}

	// Other JVM arguments must survive the rewrite.
	args := strings.Join(store.Cfg.Java.Args, " ")
	if !strings.Contains(args, "-XX:+UseG1GC") {
		t.Errorf("unrelated JVM args were lost: %q", args)
	}
	if strings.Count(args, "-Xmx") != 1 {
		t.Errorf("expected exactly one -Xmx flag, got %q", args)
	}

	// Clearing it removes the flag entirely rather than leaving "-Xmx".
	if err := store.Set(key, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(store.Cfg.Java.Args, " "), "-Xmx") {
		t.Error("clearing memory should remove the -Xmx flag")
	}

	if err := store.Validate(key, "banana"); err == nil {
		t.Error("a nonsense heap size should be rejected")
	}
}

func TestSetPersistsToTheRightFile(t *testing.T) {
	store := newTestStore(t)

	mcKey, _ := store.Resolve("minecraft")
	propKey, _ := store.Resolve("whitelist")
	if err := store.Set(mcKey, "1.21.5"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(propKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	dir := store.Cfg.Dir()
	manifest, err := os.ReadFile(filepath.Join(dir, config.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `minecraft = "1.21.5"`) {
		t.Errorf("manifest did not record the version:\n%s", manifest)
	}

	properties, err := os.ReadFile(filepath.Join(dir, "server.properties"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(properties), "white-list=true") {
		t.Errorf("server.properties did not record the whitelist:\n%s", properties)
	}
	// The whitelist must not leak into mcmgr's own manifest.
	if strings.Contains(string(manifest), "white-list") {
		t.Error("a server.properties key was written into mcmgr.toml")
	}
}

// TestWarningsFire checks that the settings that can lock a user out or eat a
// world actually produce a warning.
func TestWarningsFire(t *testing.T) {
	store := newTestStore(t)

	whitelist, _ := store.Resolve("whitelist")
	if got := store.Warnings(whitelist, "true"); len(got) == 0 {
		t.Error("enabling the whitelist should warn about being locked out")
	}
	if got := store.Warnings(whitelist, "false"); len(got) != 0 {
		t.Error("disabling the whitelist should not warn")
	}

	online, _ := store.Resolve("online-mode")
	ws := store.Warnings(online, "false")
	if len(ws) == 0 {
		t.Fatal("disabling online-mode should warn")
	}
	if ws[0].Level != warn.Danger {
		t.Error("disabling online-mode should be a Danger, not a mild caution")
	}

	// A version change only endangers a world that exists.
	mc, _ := store.Resolve("minecraft")
	if got := store.Warnings(mc, "1.20.1"); len(got) != 0 {
		t.Error("changing the version with no world present should not warn about the world")
	}
	if err := os.MkdirAll(filepath.Join(store.Cfg.Dir(), "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	store.Ctx.LevelName = "world"
	got := store.Warnings(mc, "1.20.1")
	if len(got) == 0 || got[0].Level != warn.Danger {
		t.Error("changing the version with an existing world should be a Danger")
	}

	// Setting a value to what it already is changes nothing, so it must not
	// warn.
	if got := store.Warnings(mc, "1.21.4"); len(got) != 0 {
		t.Error("a no-op change should not warn")
	}
}

func TestValidation(t *testing.T) {
	store := newTestStore(t)

	loader, _ := store.Resolve("loader")
	if err := store.Validate(loader, "fabric"); err != nil {
		t.Errorf("fabric should be a valid loader: %v", err)
	}
	if err := store.Validate(loader, "bukkit"); err == nil {
		t.Error("an unsupported loader should be rejected")
	}

	timeout, _ := store.Resolve("stop_timeout_secs")
	if err := store.Validate(timeout, "-5"); err == nil {
		t.Error("a negative stop timeout should be rejected")
	}

	def, _ := store.Resolve("global.default_server")
	if err := store.Validate(def, "nonexistent"); err == nil {
		t.Error("defaulting to an unregistered server should be rejected")
	}
}

// TestDefaultsKeysRoundTrip covers the [defaults] table: the options people
// retype most are settable, addressable by their bare names, and survive a
// reload.
func TestDefaultsKeysRoundTrip(t *testing.T) {
	store := newTestStore(t)

	// Bare names resolve to the [defaults] table.
	for input, want := range map[string]string{
		"beta":                "defaults.allow_beta",
		"allow_beta":          "defaults.allow_beta",
		"alpha":               "defaults.allow_alpha",
		"provider":            "defaults.provider",
		"prune":               "defaults.prune_unmanaged",
		"client_only":         "defaults.allow_client_only",
		"defaults.allow_beta": "defaults.allow_beta",
	} {
		key, err := store.Resolve(input)
		if err != nil {
			t.Errorf("Resolve(%q): %v", input, err)
			continue
		}
		if key.Name != want {
			t.Errorf("Resolve(%q) = %q, want %q", input, key.Name, want)
		}
	}

	beta, _ := store.Resolve("beta")
	alpha, _ := store.Resolve("alpha")
	provider, _ := store.Resolve("provider")

	if got := store.Get(beta); got != "false" {
		t.Errorf("allow_beta defaults to %q, want false", got)
	}
	if err := store.Set(beta, "true"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(provider, "curseforge"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	// Beta and alpha are independent: enabling one must not enable the other.
	if got := store.Get(alpha); got != "false" {
		t.Errorf("enabling beta changed alpha to %q", got)
	}

	reloaded, err := config.Load(store.Cfg.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Defaults.AllowBeta {
		t.Error("allow_beta did not survive a reload")
	}
	if reloaded.Defaults.AllowAlpha {
		t.Error("allow_alpha should still be off")
	}
	if reloaded.Defaults.Provider != "curseforge" {
		t.Errorf("provider = %q, want curseforge", reloaded.Defaults.Provider)
	}
}

func TestDefaultsValidation(t *testing.T) {
	store := newTestStore(t)

	beta, _ := store.Resolve("beta")
	if err := store.Set(beta, "maybe"); err == nil {
		t.Error("a non-boolean should be rejected for allow_beta")
	}

	provider, _ := store.Resolve("provider")
	if err := store.Validate(provider, "nexus"); err == nil {
		t.Error("an unknown provider should be rejected")
	}
	if err := store.Validate(provider, "modrinth"); err != nil {
		t.Errorf("modrinth should be a valid provider: %v", err)
	}
}

// TestGlobalCreationDefaults covers the machine-wide values that pre-fill
// `mcmgr new`.
func TestGlobalCreationDefaults(t *testing.T) {
	store := newTestStore(t)

	loader, err := store.Resolve("global.default_loader")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(loader, "fabric"); err != nil {
		t.Fatal(err)
	}
	memory, _ := store.Resolve("global.default_memory")
	if err := store.Set(memory, "6G"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.DefaultLoader != "fabric" || g.DefaultMemory != "6G" {
		t.Errorf("global defaults = %+v, want fabric/6G", g)
	}

	// An invalid loader must not be accepted.
	if err := store.Validate(loader, "bukkit"); err == nil {
		t.Error("an unsupported loader should be rejected as a global default")
	}
}

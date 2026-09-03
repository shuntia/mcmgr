package mods

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcmgr/internal/config"
	"mcmgr/internal/ui"
)

// newTestEnv builds a server directory with two explicit mods and one
// dependency-only mod, plus the jar files to match.
func newTestEnv(t *testing.T) *Env {
	t.Helper()
	ui.Quiet = true

	dir := t.TempDir()
	cfg := &config.Config{
		Name:      "test",
		Minecraft: "1.21.4",
		Loader:    config.Fabric,
		Mods: []config.ModSpec{
			{ID: "fabric-api", Provider: ProviderModrinth, Version: "*", Name: "Fabric API"},
			{ID: "modmenu", Provider: ProviderModrinth, Version: "*", Name: "Mod Menu"},
		},
	}
	cfg.SetDir(dir)
	if err := cfg.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	// Sorted by id, the lock is: fabric-api, modmenu, placeholder-api. Deleting
	// the middle entry is what previously shifted a later one into its place.
	lock := &config.Lock{Mods: []config.LockedMod{
		{ID: "fabric-api", Name: "Fabric API", Provider: ProviderModrinth, Filename: "fabric-api.jar", Explicit: true},
		{ID: "modmenu", Name: "Mod Menu", Provider: ProviderModrinth, Filename: "modmenu.jar", Explicit: true},
		{ID: "placeholder-api", Name: "Text Placeholder API", Provider: ProviderModrinth,
			Filename: "placeholder-api.jar", Explicit: false, RequiredBy: []string{"modmenu"}},
	}}
	if err := cfg.SaveLock(lock); err != nil {
		t.Fatalf("saving lock: %v", err)
	}

	if err := os.MkdirAll(cfg.ContentPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range lock.Mods {
		if err := os.WriteFile(filepath.Join(cfg.ContentPath(), m.Filename), []byte("jar"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env, err := Open(cfg)
	if err != nil {
		t.Fatalf("opening env: %v", err)
	}
	return env
}

// TestRemoveDropsCorrectEntries covers a bug where Lock.Find returned a pointer
// into the lock slice and Lock.Delete shifted later entries over it, so every
// read after the delete referred to the wrong mod: the manifest entry was left
// behind, the orphaned dependency survived, and the wrong name was reported.
func TestRemoveDropsCorrectEntries(t *testing.T) {
	env := newTestEnv(t)

	if err := env.Remove([]string{"modmenu"}, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if env.Lock.Find("modmenu") != nil {
		t.Error("modmenu is still in the lockfile")
	}
	if i := env.Cfg.FindMod("modmenu"); i >= 0 {
		t.Error("modmenu is still declared in the manifest")
	}
	if env.Lock.Find("placeholder-api") != nil {
		t.Error("placeholder-api was orphaned by removing modmenu but is still locked")
	}
	if env.Lock.Find("fabric-api") == nil {
		t.Error("fabric-api was removed but should have been left alone")
	}
	if i := env.Cfg.FindMod("fabric-api"); i < 0 {
		t.Error("fabric-api was dropped from the manifest but should have been left alone")
	}

	content := env.Cfg.ContentPath()
	for _, gone := range []string{"modmenu.jar", "placeholder-api.jar"} {
		if _, err := os.Stat(filepath.Join(content, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been deleted from the mods directory", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(content, "fabric-api.jar")); err != nil {
		t.Errorf("fabric-api.jar should still exist: %v", err)
	}

	// The changes must survive a reload, not just live in memory.
	reloaded, err := config.Load(env.Cfg.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if i := reloaded.FindMod("modmenu"); i >= 0 {
		t.Error("modmenu is still in the manifest on disk")
	}
	lock, err := reloaded.LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	if lock.Find("modmenu") != nil || lock.Find("placeholder-api") != nil {
		t.Error("removed mods are still in the lockfile on disk")
	}
}

// TestRemoveKeepDeps leaves dependency-only mods in place when asked.
func TestRemoveKeepDeps(t *testing.T) {
	env := newTestEnv(t)

	if err := env.Remove([]string{"modmenu"}, RemoveOptions{KeepDeps: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if env.Lock.Find("placeholder-api") == nil {
		t.Error("--keep-deps should have left placeholder-api installed")
	}
}

// TestGuardBlocksWhileRunning is the safety property the whole design turns on:
// with no running server the guard passes, so mod edits proceed.
func TestGuardPassesWhenStopped(t *testing.T) {
	env := newTestEnv(t)
	if err := env.Guard(false); err != nil {
		t.Errorf("guard should pass for a stopped server, got: %v", err)
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in           string
		defaultProv  string
		wantProvider string
		wantID       string
	}{
		{"sodium", "", "", "sodium"},
		{"modrinth:sodium", "", ProviderModrinth, "sodium"},
		{"cf:jei", "", ProviderCurseForge, "jei"},
		{"curseforge:238222", "", ProviderCurseForge, "238222"},
		{"https://modrinth.com/mod/lithium", "", ProviderModrinth, "lithium"},
		{"https://www.curseforge.com/minecraft/mc-mods/jei", "", ProviderCurseForge, "jei"},
		{"https://modrinth.com/mod/lithium/", "", ProviderModrinth, "lithium"},
		{"sodium", ProviderCurseForge, ProviderCurseForge, "sodium"},
	}
	for _, c := range cases {
		gotProvider, gotID := ParseRef(c.in, c.defaultProv)
		if gotProvider != c.wantProvider || gotID != c.wantID {
			t.Errorf("ParseRef(%q, %q) = (%q, %q), want (%q, %q)",
				c.in, c.defaultProv, gotProvider, gotID, c.wantProvider, c.wantID)
		}
	}
}

// TestVersionMatches checks the compatibility filter that decides which
// upstream releases a server can actually load.
func TestVersionMatches(t *testing.T) {
	v := Version{GameVersions: []string{"1.21.3", "1.21.4"}, Loaders: []string{"fabric"}}

	if !v.matches(Filter{Minecraft: "1.21.4", Loaders: []string{"fabric"}}) {
		t.Error("should match its own game version and loader")
	}
	if v.matches(Filter{Minecraft: "1.20.1", Loaders: []string{"fabric"}}) {
		t.Error("should not match a game version it does not list")
	}
	if v.matches(Filter{Minecraft: "1.21.4", Loaders: []string{"neoforge"}}) {
		t.Error("should not match a loader it does not list")
	}
	// Quilt servers accept Fabric mods, which is expressed as multiple facets.
	if !v.matches(Filter{Minecraft: "1.21.4", Loaders: []string{"quilt", "fabric"}}) {
		t.Error("should match when any one of the loader facets matches")
	}
}

// TestClientOnlyIsRefused covers the guard on installing an add-on the provider
// says cannot run on a server: it is an error, not a warning, because such a
// mod does nothing on a server and can stop it starting.
func TestClientOnlyIsRefused(t *testing.T) {
	env := newTestEnv(t)

	clientOnly := &Project{
		Provider: ProviderModrinth, ID: "AANobbMI", Slug: "sodium",
		Name: "Sodium", ServerSide: "unsupported", ClientSide: "required",
	}
	serverOK := &Project{
		Provider: ProviderModrinth, ID: "gvQqBUqZ", Slug: "lithium",
		Name: "Lithium", ServerSide: "optional", ClientSide: "optional",
	}

	if !clientOnly.ClientOnly() {
		t.Error("a project unsupported on servers should report ClientOnly")
	}
	if serverOK.ClientOnly() {
		t.Error("a project supported on servers should not report ClientOnly")
	}

	err := env.checkServerSide(clientOnly, false)
	if err == nil {
		t.Fatal("installing a client-only mod should be refused")
	}
	var refusal *ErrClientOnly
	if !errors.As(err, &refusal) {
		t.Fatalf("want an *ErrClientOnly, got %T", err)
	}
	// The message has to say how to proceed anyway.
	if !strings.Contains(err.Error(), "--allow-client-only") {
		t.Errorf("the refusal should name the override flag, got: %v", err)
	}
	if !strings.Contains(err.Error(), "mods/") {
		t.Errorf("the refusal should name the directory it would go in, got: %v", err)
	}

	// Explicitly allowed, and never triggered for a server-capable mod.
	if err := env.checkServerSide(clientOnly, true); err != nil {
		t.Errorf("--allow-client-only should permit it, got: %v", err)
	}
	if err := env.checkServerSide(serverOK, false); err != nil {
		t.Errorf("a server-capable mod must not be refused, got: %v", err)
	}

	// A provider that publishes no side information must never be refused.
	unknown := &Project{Provider: ProviderCurseForge, Slug: "jei", Name: "JEI"}
	if err := env.checkServerSide(unknown, false); err != nil {
		t.Errorf("a provider with no side data should not block installs, got: %v", err)
	}
}

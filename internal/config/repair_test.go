package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registerServers writes a manifest in a fresh directory for each name and
// registers it, in order, so the first becomes the default.
func registerServers(t *testing.T, names ...string) map[string]string {
	t.Helper()
	dirs := map[string]string{}
	for _, name := range names {
		dir := t.TempDir()
		cfg := &Config{Name: name, Minecraft: "1.21.4", Loader: Fabric}
		cfg.SetDir(dir)
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		reg, err := LoadRegistry()
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.Add(name, dir); err != nil {
			t.Fatal(err)
		}
		dirs[name] = dir
	}
	return dirs
}

func TestRepairForgetsGoneServersAndReseatsDefault(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())
	dirs := registerServers(t, "old", "survival")

	// The default server's directory disappears, as happens when someone
	// deletes it by hand.
	if err := os.RemoveAll(dirs["old"]); err != nil {
		t.Fatal(err)
	}

	rep, err := Repair()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Empty() {
		t.Fatal("a gone default should be something to repair")
	}
	if rep.Forgotten["old"] != dirs["old"] {
		t.Errorf("Forgotten = %v, want old at %s", rep.Forgotten, dirs["old"])
	}
	if !rep.DefaultChanged || rep.OldDefault != "old" || rep.NewDefault != "survival" {
		t.Errorf("default should move from old to survival, got %+v", rep)
	}

	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Servers["old"]; ok {
		t.Error("old should be gone from the registry on disk")
	}
	if reg.Default != "survival" {
		t.Errorf("default on disk = %q, want survival", reg.Default)
	}

	text := strings.Join(rep.Lines(), "\n")
	if !strings.Contains(text, "forgot old") || !strings.Contains(text, "default server is now survival") {
		t.Errorf("the report should say what happened, got:\n%s", text)
	}
}

func TestRepairLeavesAHealthyRegistryAlone(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())
	registerServers(t, "survival")

	p, _ := RegistryPath()
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Repair()
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Empty() {
		t.Errorf("nothing was wrong, but Repair reported %+v", rep)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a registry with nothing to fix should not be rewritten")
	}

	// Nor should a repair conjure files on a machine with no servers.
	t.Setenv("MCMGR_HOME", t.TempDir())
	if _, err := Repair(); err != nil {
		t.Fatal(err)
	}
	p, _ = RegistryPath()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("an empty registry should not be created by a repair, got %v", err)
	}
}

func TestRepairUnsetsAGlobalJavaPathThatIsGone(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())

	// A runtime that lived under a server directory since deleted.
	javaDir := t.TempDir()
	bin := filepath.Join(javaDir, "bin", "java")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("java"), 0o755); err != nil {
		t.Fatal(err)
	}
	glob, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	glob.JavaPath = bin
	glob.DefaultMemory = "4G"
	if err := glob.Save(); err != nil {
		t.Fatal(err)
	}

	// While the binary exists, the setting is left alone.
	rep, err := Repair()
	if err != nil {
		t.Fatal(err)
	}
	if rep.JavaPathCleared != "" {
		t.Errorf("an existing java_path should be kept, but %q was cleared", rep.JavaPathCleared)
	}

	if err := os.RemoveAll(javaDir); err != nil {
		t.Fatal(err)
	}
	rep, err = Repair()
	if err != nil {
		t.Fatal(err)
	}
	if rep.JavaPathCleared != bin {
		t.Errorf("JavaPathCleared = %q, want %q", rep.JavaPathCleared, bin)
	}
	glob, err = LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if glob.JavaPath != "" {
		t.Errorf("java_path on disk = %q, want unset", glob.JavaPath)
	}
	if glob.DefaultMemory != "4G" {
		t.Error("the other settings should survive the repair")
	}
	if CachedGlobal().JavaPath != "" {
		t.Error("the in-process cache should see the repair too")
	}
}

// TestResolveRepairsAGoneDefault is the case that motivated Repair: with the
// default server's directory deleted, every command run outside a server
// directory used to fail with a file-not-found error, forever.
func TestResolveRepairsAGoneDefault(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	dirs := registerServers(t, "old", "survival")
	if err := os.RemoveAll(dirs["old"]); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve("")
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("Resolve should report no server, with the repair, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{`"old"`, "no longer holds a server", "default server is now survival", "--server"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error should contain %q, got:\n%s", want, msg)
		}
	}

	// The registry is fixed, so the next command lands on the survivor.
	cfg, err := Resolve("")
	if err != nil {
		t.Fatalf("after the repair, Resolve should find survival, got %v", err)
	}
	if cfg.Name != "survival" {
		t.Errorf("resolved %q, want survival", cfg.Name)
	}
}

func TestResolveRepairsAGoneNamedServer(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	dirs := registerServers(t, "survival", "old")
	if err := os.RemoveAll(dirs["old"]); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve("old")
	if err == nil {
		t.Fatal("a gone server should not resolve")
	}
	if !strings.Contains(err.Error(), "forgot old") {
		t.Errorf("the error should say the registration was dropped, got:\n%s", err)
	}
	reg, _ := LoadRegistry()
	if _, ok := reg.Servers["old"]; ok {
		t.Error("old should have been forgotten")
	}
	if reg.Default != "survival" {
		t.Errorf("the default should be untouched, got %q", reg.Default)
	}

	// Named again, it is now simply unknown.
	_, err = Resolve("old")
	if err == nil || !strings.Contains(err.Error(), "no server named") {
		t.Errorf("a forgotten server should be reported as unknown, got %v", err)
	}
}

func TestPruneWritesOnlyWhenSomethingChanged(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())
	registerServers(t, "a")

	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := RegistryPath()
	before, _ := os.ReadFile(p)
	gone, err := reg.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Errorf("nothing should be gone, got %v", gone)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("Prune should not rewrite an unchanged registry")
	}

	// A default naming an unregistered server is repaired too.
	reg.Default = "nobody"
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Prune(); err != nil {
		t.Fatal(err)
	}
	if reg.Default != "a" {
		t.Errorf("default = %q, want a", reg.Default)
	}
}

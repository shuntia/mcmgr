package mods

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mcmgr/internal/config"
	"mcmgr/internal/ui"
)

// newUpgradeEnv builds a server whose lock holds one plain mod, one pinned mod
// and one dependency, with jars on disk to match.
func newUpgradeEnv(t *testing.T) *Env {
	t.Helper()
	ui.Quiet = true

	dir := t.TempDir()
	cfg := &config.Config{
		Name:      "test",
		Minecraft: "1.21.4",
		Loader:    config.Fabric,
		Mods: []config.ModSpec{
			{ID: "lithium", Provider: ProviderModrinth, Version: "*", Name: "Lithium"},
			{ID: "sodium", Provider: ProviderModrinth, Version: "mc1.21.4-0.6.13", Name: "Sodium"},
		},
	}
	cfg.SetDir(dir)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	lock := &config.Lock{Mods: []config.LockedMod{
		{ID: "lithium", Name: "Lithium", Provider: ProviderModrinth, ProjectID: "gvQqBUqZ",
			VersionNumber: "mc1.21.4-0.15.3", Filename: "lithium.jar", Explicit: true},
		{ID: "sodium", Name: "Sodium", Provider: ProviderModrinth, ProjectID: "AANobbMI",
			VersionNumber: "mc1.21.4-0.6.13", Filename: "sodium.jar", Explicit: true,
			ServerSide: "unsupported"},
	}}
	if err := cfg.SaveLock(lock); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	return env
}

// TestUpgradePlanPartitioning checks how a plan sorts add-ons, using entries
// built directly so the test needs no network.
func TestUpgradePlanPartitioning(t *testing.T) {
	plan := &UpgradePlan{
		From: "1.21.4", To: "1.21.5",
		Entries: []UpgradeEntry{
			{ID: "fabric-api", Current: "0.119.4", Target: "0.128.2"},
			{ID: "lithium", Current: "0.15.3", Target: "0.16.3"},
			{ID: "sodium", Current: "0.6.13", Reason: "pre-release only", UnstableOnly: true},
			{ID: "someoldmod", Current: "1.0", Reason: "no build yet"},
			{ID: "pinnedmod", Current: "2.0", Pinned: true, Reason: "pinned to 2.0"},
		},
	}

	if got := len(plan.Ready()); got != 2 {
		t.Errorf("Ready() = %d, want 2", got)
	}
	// Blocked covers everything with no target that is not deliberately pinned.
	if got := len(plan.Blocked()); got != 2 {
		t.Errorf("Blocked() = %d, want 2", got)
	}
	if got := len(plan.PinnedEntries()); got != 1 {
		t.Errorf("PinnedEntries() = %d, want 1", got)
	}

	// A pinned mod is not "blocked": it is intentionally left alone.
	for _, e := range plan.Blocked() {
		if e.Pinned {
			t.Errorf("%s is pinned and should not be reported as blocked", e.ID)
		}
	}

	declared := plan.StillDeclared()
	if len(declared) != 2 || declared[0] != "sodium" || declared[1] != "someoldmod" {
		t.Errorf("StillDeclared() = %v, want [sodium someoldmod] in sorted order", declared)
	}
}

func TestUpgradeEntryAvailable(t *testing.T) {
	if !(UpgradeEntry{Target: "1.0"}).Available() {
		t.Error("an entry with a target is available")
	}
	if (UpgradeEntry{Reason: "no build yet"}).Available() {
		t.Error("an entry with no target is not available")
	}
}

// TestApplyUpgradeRemovesIncompatible checks the destructive half of an
// upgrade: a mod with no build for the new version has its jar deleted and its
// lock entry dropped, because leaving a jar built for the old version usually
// stops the server starting. The manifest entry must survive, so the mod
// returns on a later sync.
func TestApplyUpgradeRemovesIncompatible(t *testing.T) {
	env := newUpgradeEnv(t)

	plan := &UpgradePlan{
		From: "1.21.4", To: "1.21.5",
		Entries: []UpgradeEntry{
			{ID: "sodium", Name: "Sodium", Current: "mc1.21.4-0.6.13", Reason: "pre-release only", UnstableOnly: true},
		},
	}
	if err := env.ApplyUpgrade(plan, Channels{}); err != nil {
		t.Fatalf("ApplyUpgrade: %v", err)
	}

	if env.Lock.Find("sodium") != nil {
		t.Error("an incompatible mod should be dropped from the lock")
	}
	jar := filepath.Join(env.Cfg.ContentPath(), "sodium.jar")
	if _, err := os.Stat(jar); !os.IsNotExist(err) {
		t.Error("an incompatible jar should be deleted so it cannot break startup")
	}
	if i := env.Cfg.FindMod("sodium"); i < 0 {
		t.Error("the manifest entry should survive so the mod returns on a later sync")
	}

	// Untouched mods stay exactly as they were.
	if env.Lock.Find("lithium") == nil {
		t.Error("a mod not mentioned in the plan should be left alone")
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.ContentPath(), "lithium.jar")); err != nil {
		t.Errorf("lithium.jar should still be present: %v", err)
	}

	// And it all has to be on disk, not just in memory.
	reloaded, err := config.Load(env.Cfg.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if i := reloaded.FindMod("sodium"); i < 0 {
		t.Error("the manifest on disk should still declare sodium")
	}
	lock, err := reloaded.LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	if lock.Find("sodium") != nil {
		t.Error("the lockfile on disk should no longer contain sodium")
	}
}

// TestApplyUpgradeLeavesPinnedAlone makes sure an explicit pin is never moved
// by an upgrade.
func TestApplyUpgradeLeavesPinnedAlone(t *testing.T) {
	env := newUpgradeEnv(t)

	plan := &UpgradePlan{
		From: "1.21.4", To: "1.21.5",
		Entries: []UpgradeEntry{
			{ID: "sodium", Name: "Sodium", Current: "mc1.21.4-0.6.13", Pinned: true, Reason: "pinned"},
		},
	}
	if err := env.ApplyUpgrade(plan, Channels{}); err != nil {
		t.Fatalf("ApplyUpgrade: %v", err)
	}

	if env.Lock.Find("sodium") == nil {
		t.Error("a pinned mod must not be removed by an upgrade")
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.ContentPath(), "sodium.jar")); err != nil {
		t.Errorf("a pinned mod's jar must stay in place: %v", err)
	}
}

func TestClientOnlyInstalledReporting(t *testing.T) {
	env := newUpgradeEnv(t)

	got := env.ClientOnlyInstalled()
	if len(got) != 1 || got[0] != "sodium" {
		t.Errorf("ClientOnlyInstalled() = %v, want [sodium]", got)
	}

	if !LockedClientOnly(config.LockedMod{ServerSide: "unsupported"}) {
		t.Error("a locked mod marked unsupported is client-only")
	}
	for _, side := range []string{"", "optional", "required"} {
		if LockedClientOnly(config.LockedMod{ServerSide: side}) {
			t.Errorf("server_side=%q must not count as client-only", side)
		}
	}
}

// fakeProvider serves canned projects and versions so dependency resolution
// can be exercised without the network.
type fakeProvider struct {
	projects  map[string]*Project // keyed by both id and slug
	versions  map[string]*Version // keyed by version id
	byProject map[string][]Version
}

func (f *fakeProvider) Name() string { return ProviderModrinth }

func (f *fakeProvider) Search(string, Filter, int) ([]Project, error) { return nil, nil }

func (f *fakeProvider) Project(idOrSlug string) (*Project, error) {
	if p, ok := f.projects[idOrSlug]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("no project %q", idOrSlug)
}

// Versions honours the filter the way the real providers do, so a version
// tagged for other Minecraft versions is absent from a filtered listing.
func (f *fakeProvider) Versions(projectID string, filter Filter) ([]Version, error) {
	var out []Version
	for _, v := range f.byProject[projectID] {
		if v.matches(filter) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeProvider) Version(id string) (*Version, error) {
	if v, ok := f.versions[id]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("no version %q", id)
}

// useProvider substitutes p for modrinth for the duration of one test.
func useProvider(t *testing.T, p Provider) {
	t.Helper()
	overrides = map[string]Provider{ProviderModrinth: p}
	t.Cleanup(func() { overrides = nil })
}

// TestApplyUpgradeResolvesDepsOfDependencies covers a mod that is itself only
// installed as a dependency and gains a new dependency of its own when it is
// bumped to a build for the new Minecraft version.
//
// The new dependency has to be installed, and it has to be attributed to the
// explicit mod at the root of the chain rather than to the dependency that
// asked for it, or the orphan sweep at the end of the upgrade deletes it again.
func TestApplyUpgradeResolvesDepsOfDependencies(t *testing.T) {
	ui.Quiet = true
	dir := t.TempDir()

	cfg := &config.Config{
		Name: "test", Minecraft: "1.21.4", Loader: config.Fabric,
		Mods: []config.ModSpec{{ID: "create", Provider: ProviderModrinth, Version: "*", Name: "Create"}},
	}
	cfg.SetDir(dir)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	lock := &config.Lock{Mods: []config.LockedMod{
		{ID: "create", Name: "Create", Provider: ProviderModrinth, ProjectID: "P-create",
			VersionNumber: "1.0", Filename: "create-1.0.jar", Explicit: true},
		{ID: "flywheel", Name: "Flywheel", Provider: ProviderModrinth, ProjectID: "P-flywheel",
			VersionNumber: "1.0", Filename: "flywheel-1.0.jar", RequiredBy: []string{"create"}},
	}}
	if err := cfg.SaveLock(lock); err != nil {
		t.Fatal(err)
	}
	// Every jar the upgrade installs is already on disk with no recorded
	// checksum, so install() records it instead of downloading it.
	if err := os.MkdirAll(cfg.ContentPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"create-1.0.jar", "flywheel-1.0.jar",
		"create-2.0.jar", "flywheel-2.0.jar", "ponder-2.0.jar",
	} {
		if err := os.WriteFile(filepath.Join(cfg.ContentPath(), name), []byte("jar"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	create := &Project{Provider: ProviderModrinth, ID: "P-create", Slug: "create", Name: "Create"}
	flywheel := &Project{Provider: ProviderModrinth, ID: "P-flywheel", Slug: "flywheel", Name: "Flywheel"}
	ponder := &Project{Provider: ProviderModrinth, ID: "P-ponder", Slug: "ponder", Name: "Ponder"}
	ponderVer := &Version{Provider: ProviderModrinth, ID: "V-ponder-2", ProjectID: "P-ponder",
		Number: "2.0", Filename: "ponder-2.0.jar"}
	useProvider(t, &fakeProvider{
		projects: map[string]*Project{
			"P-create": create, "create": create,
			"P-flywheel": flywheel, "flywheel": flywheel,
			"P-ponder": ponder, "ponder": ponder,
		},
		versions:  map[string]*Version{"V-ponder-2": ponderVer},
		byProject: map[string][]Version{"P-ponder": {*ponderVer}},
	})

	env, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Create itself gains nothing new; Flywheel, a dependency-only mod, now
	// needs Ponder.
	plan := &UpgradePlan{From: "1.21.4", To: "1.21.5", Entries: []UpgradeEntry{
		{
			ID: "create", Name: "Create", Current: "1.0", Target: "2.0",
			project: &Project{Provider: ProviderModrinth, ID: "P-create", Slug: "create", Name: "Create"},
			version: &Version{Provider: ProviderModrinth, ID: "V-create-2", ProjectID: "P-create",
				Number: "2.0", Filename: "create-2.0.jar",
				Dependencies: []Dependency{{ProjectID: "P-flywheel", Type: "required"}}},
		},
		{
			ID: "flywheel", Name: "Flywheel", Current: "1.0", Target: "2.0",
			project: &Project{Provider: ProviderModrinth, ID: "P-flywheel", Slug: "flywheel", Name: "Flywheel"},
			version: &Version{Provider: ProviderModrinth, ID: "V-flywheel-2", ProjectID: "P-flywheel",
				Number: "2.0", Filename: "flywheel-2.0.jar",
				Dependencies: []Dependency{{ProjectID: "P-ponder", VersionID: "V-ponder-2", Type: "required"}}},
		},
	}}

	if err := env.ApplyUpgrade(plan, Channels{}); err != nil {
		t.Fatalf("ApplyUpgrade: %v", err)
	}

	locked := env.Lock.Find("ponder")
	if locked == nil {
		t.Fatal("a new dependency of an upgraded dependency was never installed")
	}
	if locked.Explicit {
		t.Error("ponder should be recorded as a dependency, not as an explicit mod")
	}
	// Attributed to create: nothing in the manifest requires flywheel by name,
	// so a ponder that only names flywheel would be pruned as an orphan.
	if len(locked.RequiredBy) == 0 || locked.RequiredBy[0] != "create" {
		t.Errorf("ponder.RequiredBy = %v, want the explicit mod [create]", locked.RequiredBy)
	}

	// And it survives on disk, which is what the orphan sweep decides.
	if _, err := os.Stat(filepath.Join(env.Cfg.ContentPath(), "ponder-2.0.jar")); err != nil {
		t.Errorf("ponder's jar should still be installed: %v", err)
	}
	reloaded, err := config.Load(env.Cfg.Dir())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reloaded.LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Find("ponder") == nil {
		t.Error("the lockfile on disk should record ponder")
	}
}

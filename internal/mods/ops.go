package mods

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mcmgr/internal/config"
	"mcmgr/internal/httpx"
	"mcmgr/internal/server"
	"mcmgr/internal/ui"
)

// Env is a mod workspace: one server's manifest plus its lockfile.
type Env struct {
	Cfg  *config.Config
	Lock *config.Lock
}

// Open loads the lockfile for a server.
func Open(cfg *config.Config) (*Env, error) {
	lock, err := cfg.LoadLock()
	if err != nil {
		return nil, err
	}
	return &Env{Cfg: cfg, Lock: lock}, nil
}

// Guard refuses to modify the mods directory while the server is running.
//
// Swapping jars under a live JVM is how worlds get corrupted and how a mod
// list stops matching what is actually loaded, so every mutating operation
// calls this first. Passing force overrides it, loudly, for people who
// knowingly want to stage changes for the next restart.
func (e *Env) Guard(force bool) error {
	err := server.EnsureStopped(e.Cfg.Dir(), e.Cfg.Name)
	if err == nil {
		return nil
	}
	var running *server.ErrRunning
	if !asRunning(err, &running) {
		return err
	}
	if !force {
		msg := fmt.Sprintf("server %q is running", e.Cfg.Name)
		if running.Live != nil && running.Live.State != nil {
			msg += fmt.Sprintf(" (pid %d, up %s)",
				running.Live.State.PID, ui.Duration(running.Live.State.Uptime()))
		}
		return fmt.Errorf("%s\n"+
			"       stop it first:  mcmgr server stop\n"+
			"       or override at your own risk with --force (changes apply on next restart)", msg)
	}
	ui.Warn("server %q is running: changes will not take effect until it restarts, "+
		"and the running server may misbehave if it reads a jar being replaced", e.Cfg.Name)
	return nil
}

func asRunning(err error, target **server.ErrRunning) bool {
	r, ok := err.(*server.ErrRunning)
	if ok {
		*target = r
	}
	return ok
}

// checkLoader rejects mod operations on a loader that cannot load anything.
func (e *Env) checkLoader() error {
	if !e.Cfg.Loader.SupportsMods() {
		return fmt.Errorf("a %s server cannot load mods; create the server with --loader fabric, "+
			"paper or neoforge to manage add-ons", e.Cfg.Loader)
	}
	return nil
}

// AddOptions controls a single add.
type AddOptions struct {
	Provider string
	Version  string
	NoDeps   bool
	Channels Channels
	Force    bool
	// AllowClientOnly installs an add-on the provider marks as unsupported on
	// servers, which is otherwise refused.
	AllowClientOnly bool
}

// ErrClientOnly reports an add-on that its provider says cannot run on a
// server. Installing one is at best a no-op and at worst a crash on start, so
// it is refused rather than warned about.
type ErrClientOnly struct {
	Name string
	Slug string
	Dir  string
}

func (e *ErrClientOnly) Error() string {
	return fmt.Sprintf("%s is a client-only mod\n"+
		"       its author marks it as unsupported on servers, so putting it in %s\n"+
		"       does nothing at best, and can stop the server starting at worst\n"+
		"       install it in your own client's mods folder instead\n"+
		"       or, if you are sure it belongs here: mcmgr mod add %s --allow-client-only",
		e.Name, e.Dir, e.Slug)
}

// checkServerSide refuses client-only add-ons unless explicitly allowed.
func (e *Env) checkServerSide(proj *Project, allow bool) error {
	if !proj.ClientOnly() || allow {
		return nil
	}
	return &ErrClientOnly{Name: proj.Name, Slug: proj.Slug, Dir: e.Cfg.Loader.ContentDir() + "/"}
}

// Add resolves a query to a project, installs its newest compatible version
// along with required dependencies, and records both in the manifest and lock.
func (e *Env) Add(query string, opts AddOptions) error {
	if err := e.checkLoader(); err != nil {
		return err
	}
	if err := e.Guard(opts.Force); err != nil {
		return err
	}

	providerName, ref := ParseRef(query, opts.Provider)
	prov, err := Get(providerName)
	if err != nil {
		return err
	}
	filter := FilterFor(e.Cfg, opts.Channels)

	proj, err := e.resolveProject(prov, ref, filter)
	if err != nil {
		return err
	}
	// Checked before anything is downloaded, so a refused add costs nothing.
	if err := e.checkServerSide(proj, opts.AllowClientOnly || opts.Force); err != nil {
		return err
	}

	version, err := e.selectVersion(prov, proj, opts.Version, filter)
	if err != nil {
		return err
	}

	if existing := e.Lock.Find(proj.Slug); existing != nil && existing.VersionID == version.ID {
		ui.Info("%s %s is already installed", proj.Name, version.Number)
		e.recordSpec(proj, opts.Version)
		return e.save()
	}

	if err := e.install(proj, version, true, nil); err != nil {
		return err
	}
	if !opts.NoDeps {
		if err := e.installDeps(prov, version, proj.Slug, filter, map[string]bool{proj.ID: true}, 0); err != nil {
			return err
		}
	}

	e.recordSpec(proj, opts.Version)
	return e.save()
}

// maxDepDepth bounds dependency recursion; real graphs are shallow, and this
// stops a cycle in provider metadata from looping forever.
const maxDepDepth = 6

// installDeps walks required dependencies breadth-first, installing any that
// are not already present.
func (e *Env) installDeps(prov Provider, v *Version, parentSlug string, f Filter, seen map[string]bool, depth int) error {
	if depth >= maxDepDepth {
		return nil
	}
	for _, dep := range v.Dependencies {
		if dep.Type != "required" || dep.ProjectID == "" || seen[dep.ProjectID] {
			continue
		}
		seen[dep.ProjectID] = true

		depProj, err := prov.Project(dep.ProjectID)
		if err != nil {
			ui.Warn("skipping a dependency of %s: %v", parentSlug, err)
			continue
		}
		// A required dependency is not refused the way a direct add is:
		// declining it would break the mod that asked for it.
		if depProj.ClientOnly() {
			ui.Warn("%s requires %s, which is marked client-only; installing it anyway",
				parentSlug, depProj.Name)
		}

		// Already installed: just record the new dependant.
		if existing := e.Lock.Find(depProj.Slug); existing != nil {
			if !containsFold(existing.RequiredBy, parentSlug) && !existing.Explicit {
				existing.RequiredBy = append(existing.RequiredBy, parentSlug)
			}
			continue
		}

		var depVer *Version
		if dep.VersionID != "" {
			depVer, err = prov.Version(dep.VersionID)
		} else {
			var versions []Version
			versions, err = prov.Versions(depProj.ID, f)
			if err == nil {
				depVer, err = pickBest(versions, f)
			}
		}
		if err != nil {
			ui.Warn("could not resolve dependency %s: %v", depProj.Name, err)
			continue
		}

		if err := e.install(depProj, depVer, false, []string{parentSlug}); err != nil {
			return err
		}
		if err := e.installDeps(prov, depVer, parentSlug, f, seen, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// resolveProject turns a user-supplied reference into a project, treating it
// as a slug or id first and falling back to a search.
func (e *Env) resolveProject(prov Provider, ref string, f Filter) (*Project, error) {
	if p, err := prov.Project(ref); err == nil {
		return p, nil
	}

	results, err := prov.Search(ref, f, 5)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("nothing on %s matches %q for %s %s",
			prov.Name(), ref, e.Cfg.Loader, e.Cfg.Minecraft)
	}
	best := results[0]
	ui.Info("%s%s%s resolved to %s%s%s (%s)",
		ui.Dim, ref, ui.Reset, ui.Bold, best.Name, ui.Reset, best.URL)
	return &best, nil
}

// selectVersion picks the version to install: an explicit pin if given, else
// the newest compatible release.
func (e *Env) selectVersion(prov Provider, proj *Project, pin string, f Filter) (*Version, error) {
	if pin != "" && pin != "*" {
		v, err := prov.Version(pin)
		if err == nil {
			return v, nil
		}
		// Not a version id: try matching a version number instead.
		versions, verr := prov.Versions(proj.ID, f)
		if verr != nil {
			return nil, err
		}
		for i := range versions {
			if strings.EqualFold(versions[i].Number, pin) {
				return &versions[i], nil
			}
		}
		return nil, fmt.Errorf("%s has no version %q compatible with %s %s",
			proj.Name, pin, e.Cfg.Loader, e.Cfg.Minecraft)
	}

	versions, err := prov.Versions(proj.ID, f)
	if err != nil {
		return nil, err
	}
	best, err := pickBest(versions, f)
	if err != nil {
		switch {
		case errors.Is(err, errNoCompatible):
			return nil, fmt.Errorf("%s has no release for %s %s",
				proj.Name, e.Cfg.Loader, e.Cfg.Minecraft)
		case errors.Is(err, errUnstableOnly):
			// Name the exact channel and the setting that enables it, rather
			// than a blanket "unstable".
			var detail *UnstableOnlyError
			if errors.As(err, &detail) {
				return nil, fmt.Errorf("%s: %s for %s %s\n"+
					"       pass %s to install one, or set %s in %s to stop asking",
					proj.Name, detail.Error(), e.Cfg.Loader, e.Cfg.Minecraft,
					detail.Enable(), detail.Setting(), config.ManifestName)
			}
			return nil, fmt.Errorf("%s has only pre-release builds for %s %s\n"+
				"       pass --allow-unstable to install one",
				proj.Name, e.Cfg.Loader, e.Cfg.Minecraft)
		}
		return nil, fmt.Errorf("%s: %w", proj.Name, err)
	}
	return best, nil
}

// install downloads a version into the content directory and records it in the
// lock, replacing any previously installed file for the same project.
func (e *Env) install(proj *Project, v *Version, explicit bool, requiredBy []string) error {
	dir := e.Cfg.ContentPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, v.Filename)

	// Remove the previous jar when a new version renamed the file, so the
	// server does not load two copies of the same mod.
	if old := e.Lock.Find(proj.Slug); old != nil && old.Filename != v.Filename {
		_ = os.Remove(filepath.Join(dir, old.Filename))
	}

	if !fileMatches(dst, v.SHA1) {
		label := "installing"
		if !explicit {
			label = "installing dependency"
		}
		ui.Step("%s %s %s", label, proj.Name, v.Number)
		if err := httpx.Download(v.URL, dst, v.SHA1); err != nil {
			return fmt.Errorf("downloading %s: %w", proj.Name, err)
		}
	}

	entry := config.LockedMod{
		ID:            proj.Slug,
		Name:          proj.Name,
		Provider:      proj.Provider,
		ProjectID:     proj.ID,
		VersionID:     v.ID,
		VersionNumber: v.Number,
		Filename:      v.Filename,
		SHA1:          v.SHA1,
		Size:          v.Size,
		URL:           v.URL,
		Explicit:      explicit,
		RequiredBy:    requiredBy,
		GameVersions:  v.GameVersions,
		ServerSide:    proj.ServerSide,
		InstalledAt:   time.Now().Unix(),
	}
	// An explicit add promotes a mod that was previously only a dependency.
	if prev := e.Lock.Find(proj.Slug); prev != nil && !explicit {
		entry.Explicit = prev.Explicit
		entry.RequiredBy = mergeStrings(prev.RequiredBy, requiredBy)
	}
	e.Lock.Upsert(entry)
	return nil
}

// recordSpec adds or updates the manifest entry for an explicitly wanted mod.
func (e *Env) recordSpec(proj *Project, pin string) {
	if pin == "" {
		pin = "*"
	}
	spec := config.ModSpec{ID: proj.Slug, Provider: proj.Provider, Version: pin, Name: proj.Name}
	if i := e.Cfg.FindMod(proj.Slug); i >= 0 {
		e.Cfg.Mods[i] = spec
		return
	}
	e.Cfg.Mods = append(e.Cfg.Mods, spec)
}

// RemoveOptions controls a removal.
type RemoveOptions struct {
	// KeepDeps leaves dependency-only mods in place even when nothing needs
	// them any more.
	KeepDeps bool
	Force    bool
}

// Remove uninstalls mods by name, deleting their jars and dropping orphaned
// dependencies.
func (e *Env) Remove(names []string, opts RemoveOptions) error {
	if err := e.Guard(opts.Force); err != nil {
		return err
	}

	dir := e.Cfg.ContentPath()
	var removed []string
	for _, name := range names {
		found := e.Lock.Find(name)
		if found == nil {
			if i := e.Cfg.FindMod(name); i >= 0 {
				// Declared but never installed: drop the declaration.
				e.Cfg.Mods = append(e.Cfg.Mods[:i], e.Cfg.Mods[i+1:]...)
				removed = append(removed, name)
				ui.Ok("removed %s", name)
				continue
			}
			ui.Warn("%s is not installed", name)
			continue
		}
		// Find returns a pointer into the lock's slice, and deleting from that
		// slice shifts later entries down over it. Copy first, or every read
		// after the delete silently refers to a different mod.
		entry := *found

		if err := os.Remove(filepath.Join(dir, entry.Filename)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", entry.Filename, err)
		}
		e.Lock.Delete(entry.ID)
		if i := e.Cfg.FindMod(entry.ID); i >= 0 {
			e.Cfg.Mods = append(e.Cfg.Mods[:i], e.Cfg.Mods[i+1:]...)
		}
		removed = append(removed, entry.Name)
		ui.Ok("removed %s", entry.Name)
	}

	if !opts.KeepDeps {
		if err := e.pruneOrphans(); err != nil {
			return err
		}
	}
	if len(removed) == 0 {
		return nil
	}
	return e.save()
}

// pruneOrphans deletes dependency-only mods that nothing explicit requires any
// more.
func (e *Env) pruneOrphans() error {
	keep := map[string]bool{}
	for _, m := range e.Cfg.Mods {
		keep[strings.ToLower(m.ID)] = true
	}
	dir := e.Cfg.ContentPath()
	for _, orphan := range e.Lock.Orphans(keep) {
		if err := os.Remove(filepath.Join(dir, orphan.Filename)); err != nil && !os.IsNotExist(err) {
			return err
		}
		e.Lock.Delete(orphan.ID)
		ui.Ok("removed unused dependency %s", orphan.Name)
	}
	return nil
}

// UpdateOptions controls an update pass.
type UpdateOptions struct {
	DryRun   bool
	Channels Channels
	Force    bool
}

// Update upgrades installed mods to their newest compatible versions. Pinned
// entries are reported and skipped. With no names, everything is considered.
func (e *Env) Update(names []string, opts UpdateOptions) error {
	if err := e.checkLoader(); err != nil {
		return err
	}
	if !opts.DryRun {
		if err := e.Guard(opts.Force); err != nil {
			return err
		}
	}

	targets := e.Lock.Mods
	if len(names) > 0 {
		targets = nil
		for _, n := range names {
			entry := e.Lock.Find(n)
			if entry == nil {
				ui.Warn("%s is not installed", n)
				continue
			}
			targets = append(targets, *entry)
		}
	}

	filter := FilterFor(e.Cfg, opts.Channels)
	changed := 0

	for _, entry := range targets {
		if i := e.Cfg.FindMod(entry.ID); i >= 0 && e.Cfg.Mods[i].Pinned() {
			ui.Info("%s%-28s%s pinned to %s", ui.Dim, entry.ID, ui.Reset, e.Cfg.Mods[i].Version)
			continue
		}

		prov, err := Get(entry.Provider)
		if err != nil {
			ui.Warn("%s: %v", entry.ID, err)
			continue
		}
		versions, err := prov.Versions(entry.ProjectID, filter)
		if err != nil {
			ui.Warn("%s: %v", entry.ID, err)
			continue
		}
		best, err := pickBest(versions, filter)
		if err != nil {
			ui.Warn("%s: %v", entry.ID, err)
			continue
		}
		if best.ID == entry.VersionID {
			continue
		}

		changed++
		if opts.DryRun {
			ui.Info("%s%-28s%s %s -> %s%s%s", ui.Bold, entry.ID, ui.Reset,
				entry.VersionNumber, ui.Green, best.Number, ui.Reset)
			continue
		}

		proj := &Project{Provider: entry.Provider, ID: entry.ProjectID, Slug: entry.ID, Name: entry.Name}
		if err := e.install(proj, best, entry.Explicit, entry.RequiredBy); err != nil {
			return err
		}
		ui.Ok("%s %s -> %s", entry.Name, entry.VersionNumber, best.Number)
	}

	if changed == 0 {
		ui.Info("everything is up to date")
		return nil
	}
	if opts.DryRun {
		ui.Info("\n%d update(s) available; run without --dry-run to apply", changed)
		return nil
	}
	return e.save()
}

// SyncOptions controls a sync.
type SyncOptions struct {
	// Prune deletes jars in the content directory that mcmgr does not manage.
	Prune    bool
	Channels Channels
	Force    bool
}

// Sync makes the content directory match the manifest: it installs anything
// declared but missing, re-downloads files that are absent or corrupt, and
// removes entries no longer declared. It is the command to run after cloning a
// server config or editing mcmgr.json by hand.
func (e *Env) Sync(opts SyncOptions) error {
	if err := e.checkLoader(); err != nil {
		return err
	}
	if err := e.Guard(opts.Force); err != nil {
		return err
	}

	dir := e.Cfg.ContentPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	filter := FilterFor(e.Cfg, opts.Channels)
	installed, unresolved := 0, 0

	for _, spec := range e.Cfg.Mods {
		entry := e.Lock.Find(spec.ID)

		// Locked and the file on disk is intact: nothing to do.
		if entry != nil && fileMatches(filepath.Join(dir, entry.Filename), entry.SHA1) {
			continue
		}

		prov, err := Get(spec.Provider)
		if err != nil {
			return err
		}

		// Re-download from the lock when possible so sync is reproducible.
		if entry != nil {
			proj := &Project{Provider: entry.Provider, ID: entry.ProjectID, Slug: entry.ID, Name: entry.Name}
			v := &Version{
				Provider: entry.Provider, ID: entry.VersionID, ProjectID: entry.ProjectID,
				Number: entry.VersionNumber, Filename: entry.Filename, URL: entry.URL,
				SHA1: entry.SHA1, Size: entry.Size,
			}
			if v.URL != "" {
				if err := e.install(proj, v, entry.Explicit, entry.RequiredBy); err != nil {
					return err
				}
				installed++
				continue
			}
		}

		// Not locked yet: resolve it fresh. One mod that cannot be resolved
		// must not abandon the rest of the sync, because an upgrade
		// deliberately leaves entries declared that have no build yet.
		proj, err := e.resolveProject(prov, spec.ID, filter)
		if err != nil {
			ui.Warn("%s: %v", spec.ID, err)
			unresolved++
			continue
		}
		if proj.ClientOnly() {
			ui.Warn("%s is marked client-only; it does nothing on this server", proj.Name)
		}
		version, err := e.selectVersion(prov, proj, spec.Version, filter)
		if err != nil {
			ui.Warn("%v", err)
			unresolved++
			continue
		}
		if err := e.install(proj, version, true, nil); err != nil {
			return err
		}
		if err := e.installDeps(prov, version, proj.Slug, filter, map[string]bool{proj.ID: true}, 0); err != nil {
			return err
		}
		installed++
	}

	// Drop lock entries for mods no longer declared, then unused dependencies.
	declared := map[string]bool{}
	for _, m := range e.Cfg.Mods {
		declared[strings.ToLower(m.ID)] = true
	}
	for _, entry := range append([]config.LockedMod(nil), e.Lock.Mods...) {
		if entry.Explicit && !declared[strings.ToLower(entry.ID)] {
			_ = os.Remove(filepath.Join(dir, entry.Filename))
			e.Lock.Delete(entry.ID)
			ui.Ok("removed %s (no longer in %s)", entry.Name, config.ManifestName)
		}
	}
	if err := e.pruneOrphans(); err != nil {
		return err
	}

	if opts.Prune {
		if err := e.pruneUnmanaged(); err != nil {
			return err
		}
	} else {
		e.reportUnmanaged()
	}

	if installed == 0 {
		ui.Info("everything is in sync")
	} else {
		ui.Ok("synced %d %s(s)", installed, e.Cfg.Loader.ContentNoun())
	}
	if unresolved > 0 {
		ui.Info("       %d declared %s(s) could not be resolved and were left uninstalled",
			unresolved, e.Cfg.Loader.ContentNoun())
	}
	return e.save()
}

// managedFiles is the set of jar names the lockfile accounts for.
func (e *Env) managedFiles() map[string]bool {
	m := map[string]bool{}
	for _, entry := range e.Lock.Mods {
		m[strings.ToLower(entry.Filename)] = true
	}
	return m
}

// unmanaged lists jars in the content directory that mcmgr did not install.
func (e *Env) unmanaged() ([]string, error) {
	entries, err := os.ReadDir(e.Cfg.ContentPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	managed := e.managedFiles()
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".jar") {
			continue
		}
		if !managed[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (e *Env) reportUnmanaged() {
	extra, err := e.unmanaged()
	if err != nil || len(extra) == 0 {
		return
	}
	ui.Warn("%d file(s) in %s were not installed by mcmgr: %s",
		len(extra), e.Cfg.Loader.ContentDir(), strings.Join(extra, ", "))
	ui.Info("       run 'mcmgr mod sync --prune' to delete them, or leave them alone")
}

func (e *Env) pruneUnmanaged() error {
	extra, err := e.unmanaged()
	if err != nil {
		return err
	}
	for _, name := range extra {
		if err := os.Remove(filepath.Join(e.Cfg.ContentPath(), name)); err != nil {
			return err
		}
		ui.Ok("pruned unmanaged file %s", name)
	}
	return nil
}

// Adopt identifies add-ons already present in the content directory by hashing
// them and asking Modrinth what they are, recording the matches in the
// manifest and lockfile.
//
// This is what turns a directory someone has been managing by hand into one
// mcmgr can update: without it, every pre-existing jar would stay an unmanaged
// file forever. Files Modrinth does not recognise are returned so the caller
// can say so, and are otherwise left untouched.
func (e *Env) Adopt() (int, []string, error) {
	dir := e.Cfg.ContentPath()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}

	managed := e.managedFiles()
	hashes := map[string]string{} // sha1 -> filename
	var order []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".jar") {
			continue
		}
		if managed[strings.ToLower(name)] {
			continue
		}
		sum, herr := httpx.FileSHA1(filepath.Join(dir, name))
		if herr != nil {
			continue
		}
		hashes[strings.ToLower(sum)] = name
		order = append(order, strings.ToLower(sum))
	}
	if len(order) == 0 {
		return 0, nil, nil
	}

	mr := &Modrinth{}
	found, err := mr.VersionsByHash(order)
	if err != nil {
		return 0, nil, err
	}

	projectIDs := make([]string, 0, len(found))
	for _, v := range found {
		projectIDs = append(projectIDs, v.ProjectID)
	}
	projects, err := mr.Projects(projectIDs)
	if err != nil {
		return 0, nil, err
	}

	adopted := 0
	for _, sum := range order {
		version, ok := found[sum]
		if !ok {
			continue
		}
		proj, ok := projects[version.ProjectID]
		if !ok {
			continue
		}
		// The file is already on disk, so record it rather than downloading.
		e.Lock.Upsert(config.LockedMod{
			ID:            proj.Slug,
			Name:          proj.Name,
			Provider:      ProviderModrinth,
			ProjectID:     proj.ID,
			VersionID:     version.ID,
			VersionNumber: version.Number,
			Filename:      hashes[sum],
			SHA1:          sum,
			Size:          version.Size,
			URL:           version.URL,
			Explicit:      true,
			GameVersions:  version.GameVersions,
			InstalledAt:   time.Now().Unix(),
		})
		e.recordSpec(&proj, "*")
		adopted++
		delete(hashes, sum)
	}

	var unknown []string
	for _, name := range hashes {
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)

	if adopted > 0 {
		if err := e.save(); err != nil {
			return adopted, unknown, err
		}
	}
	return adopted, unknown, nil
}

// Outdated is one mod with a newer version available.
type Outdated struct {
	Entry   config.LockedMod
	Latest  string
	Pinned  bool
	Problem error
}

// CheckOutdated queries every provider for newer versions. It is only used by
// `mod list --outdated`, since it costs one request per mod.
func (e *Env) CheckOutdated(ch Channels) []Outdated {
	filter := FilterFor(e.Cfg, ch)
	out := make([]Outdated, 0, len(e.Lock.Mods))

	for _, entry := range e.Lock.Mods {
		row := Outdated{Entry: entry}
		if i := e.Cfg.FindMod(entry.ID); i >= 0 && e.Cfg.Mods[i].Pinned() {
			row.Pinned = true
			out = append(out, row)
			continue
		}
		prov, err := Get(entry.Provider)
		if err != nil {
			row.Problem = err
			out = append(out, row)
			continue
		}
		versions, err := prov.Versions(entry.ProjectID, filter)
		if err != nil {
			row.Problem = err
			out = append(out, row)
			continue
		}
		if best, err := pickBest(versions, filter); err == nil && best.ID != entry.VersionID {
			row.Latest = best.Number
		}
		out = append(out, row)
	}
	return out
}

// save persists both the manifest and the lockfile.
func (e *Env) save() error {
	if err := e.Cfg.Save(); err != nil {
		return err
	}
	return e.Cfg.SaveLock(e.Lock)
}

// parseRef splits a user reference into provider and identifier, accepting
// "modrinth:sodium", a Modrinth or CurseForge URL, or a bare slug.
// ParseRef is exported for the info command, which resolves a reference
// without installing anything.
func ParseRef(ref, defaultProvider string) (provider, id string) {
	provider = defaultProvider

	switch {
	case strings.Contains(ref, "modrinth.com/"):
		return ProviderModrinth, lastPathSegment(ref)
	case strings.Contains(ref, "curseforge.com/"):
		return ProviderCurseForge, lastPathSegment(ref)
	}

	if name, rest, ok := strings.Cut(ref, ":"); ok {
		switch strings.ToLower(name) {
		case ProviderModrinth, "mr":
			return ProviderModrinth, rest
		case ProviderCurseForge, "cf", "curse":
			return ProviderCurseForge, rest
		}
	}
	return provider, ref
}

// lastPathSegment extracts the slug from a project URL.
func lastPathSegment(u string) string {
	u = strings.TrimSuffix(u, "/")
	if i := strings.LastIndex(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return u
}

// fileMatches reports whether a file exists and matches the expected sha1. An
// empty sum only checks existence, since not every provider publishes hashes.
func fileMatches(path, sum string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if sum == "" {
		return true
	}
	got, err := httpx.FileSHA1(path)
	return err == nil && strings.EqualFold(got, sum)
}

// mergeStrings unions two slices, case-insensitively, preserving order.
func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		k := strings.ToLower(s)
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

package mods

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/ui"
)

// UpgradeEntry is what happens to one add-on when the server moves to a new
// Minecraft version.
type UpgradeEntry struct {
	ID   string
	Name string
	// Current is the installed version number.
	Current string
	// Target is the version that would be installed, empty when none exists.
	Target string
	// Pinned marks a mod the manifest holds at an exact version, which an
	// upgrade must not silently move.
	Pinned bool
	// Reason explains an empty Target.
	Reason string
	// UnstableOnly marks a mod that has builds for the new version, but only
	// pre-release ones, which --allow-unstable would accept.
	UnstableOnly bool
	// Channels are the pre-release channels its builds are in, so the caller
	// can name the one to enable.
	Channels []string

	project *Project
	version *Version
}

// Available reports whether this mod has a build for the new version.
func (e UpgradeEntry) Available() bool { return e.Target != "" }

// UpgradePlan is the full picture of an upgrade, worked out before anything on
// disk is touched.
type UpgradePlan struct {
	From string
	To   string
	// Entries is every installed add-on, in name order.
	Entries []UpgradeEntry
}

// Ready lists add-ons that have a compatible build for the new version.
func (p *UpgradePlan) Ready() []UpgradeEntry {
	return p.filter(func(e UpgradeEntry) bool { return e.Available() })
}

// Blocked lists add-ons with no compatible build, which the upgrade would
// leave uninstalled.
func (p *UpgradePlan) Blocked() []UpgradeEntry {
	return p.filter(func(e UpgradeEntry) bool { return !e.Available() && !e.Pinned })
}

// PinnedEntries lists add-ons held at an exact version, which an upgrade
// deliberately does not move.
func (p *UpgradePlan) PinnedEntries() []UpgradeEntry {
	return p.filter(func(e UpgradeEntry) bool { return e.Pinned })
}

func (p *UpgradePlan) filter(keep func(UpgradeEntry) bool) []UpgradeEntry {
	var out []UpgradeEntry
	for _, e := range p.Entries {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// PlanUpgrade works out what moving to a new Minecraft version would do to
// every installed add-on, without changing anything.
//
// This is deliberately a separate step: the useful question before an upgrade
// is "will my mods survive it", and that has to be answerable while the server
// is still on the old version and working.
func (e *Env) PlanUpgrade(to string, ch Channels) (*UpgradePlan, error) {
	if err := e.checkLoader(); err != nil && len(e.Lock.Mods) > 0 {
		return nil, err
	}

	plan := &UpgradePlan{From: e.Cfg.Minecraft, To: to}
	filter := Filter{
		Minecraft: to,
		Loaders:   e.Cfg.Loader.ModrinthFacets(),
		Channels:  ch,
	}

	for _, locked := range e.Lock.Mods {
		entry := UpgradeEntry{ID: locked.ID, Name: locked.Name, Current: locked.VersionNumber}

		if i := e.Cfg.FindMod(locked.ID); i >= 0 && e.Cfg.Mods[i].Pinned() {
			entry.Pinned = true
			entry.Reason = "pinned to " + e.Cfg.Mods[i].Version
			plan.Entries = append(plan.Entries, entry)
			continue
		}

		prov, err := Get(locked.Provider)
		if err != nil {
			entry.Reason = err.Error()
			plan.Entries = append(plan.Entries, entry)
			continue
		}
		versions, err := prov.Versions(locked.ProjectID, filter)
		if err != nil {
			entry.Reason = err.Error()
			plan.Entries = append(plan.Entries, entry)
			continue
		}
		best, err := pickBest(versions, filter)
		if err != nil {
			// "nothing exists yet" and "only betas exist" call for different
			// responses, so they are not collapsed into one message.
			switch {
			case errors.Is(err, errNoCompatible):
				entry.Reason = "no build yet"
			case errors.Is(err, errUnstableOnly):
				entry.Reason = "pre-release only"
				entry.UnstableOnly = true
				var detail *UnstableOnlyError
				if errors.As(err, &detail) && len(detail.Channels) > 0 {
					entry.Reason = strings.Join(detail.Channels, "/") + " only"
					entry.Channels = detail.Channels
				}
			default:
				entry.Reason = err.Error()
			}
			plan.Entries = append(plan.Entries, entry)
			continue
		}

		entry.Target = best.Number
		entry.version = best
		entry.project = &Project{
			Provider: locked.Provider, ID: locked.ProjectID,
			Slug: locked.ID, Name: locked.Name, ServerSide: locked.ServerSide,
		}
		plan.Entries = append(plan.Entries, entry)
	}

	sort.Slice(plan.Entries, func(i, j int) bool { return plan.Entries[i].ID < plan.Entries[j].ID })
	return plan, nil
}

// ApplyUpgrade installs the new versions the plan resolved and removes add-ons
// that have no build for the new Minecraft version.
//
// Incompatible jars are deleted rather than left in place: a mod built for the
// old version will usually stop the server from starting. The manifest entry
// stays, so the mod comes back on a later `mcmgr mod sync` once its author
// publishes a compatible build.
func (e *Env) ApplyUpgrade(plan *UpgradePlan, ch Channels) error {
	dir := e.Cfg.ContentPath()
	filter := Filter{
		Minecraft: plan.To,
		Loaders:   e.Cfg.Loader.ModrinthFacets(),
		Channels:  ch,
	}

	for _, entry := range plan.Entries {
		switch {
		case entry.Pinned:
			ui.Info("%s%-28s%s left at %s (pinned)", ui.Dim, entry.ID, ui.Reset, entry.Current)

		case entry.Available():
			locked := e.Lock.Find(entry.ID)
			explicit, requiredBy := true, []string(nil)
			if locked != nil {
				explicit, requiredBy = locked.Explicit, locked.RequiredBy
			}
			if err := e.install(entry.project, entry.version, explicit, requiredBy); err != nil {
				return err
			}
			if entry.Current != entry.Target {
				ui.Ok("%s %s -> %s", entry.Name, entry.Current, entry.Target)
			}

		default:
			// No build for the new version: take it out so it cannot break the
			// first start on the new version.
			if locked := e.Lock.Find(entry.ID); locked != nil {
				if err := os.Remove(filepath.Join(dir, locked.Filename)); err != nil && !os.IsNotExist(err) {
					return err
				}
				e.Lock.Delete(entry.ID)
			}
			ui.Warn("%s removed: %s", entry.Name, entry.Reason)
		}
	}

	// New versions can want different dependencies than the old ones did, and
	// that is as true of a mod installed only as a dependency as of one the
	// user asked for by name, so every upgraded entry is re-resolved.
	for _, entry := range plan.Ready() {
		if entry.version == nil || entry.project == nil {
			continue
		}
		// Whatever a dependency-only mod pulls in belongs to the mods that
		// require it, not to the mod itself: only declared mods keep a
		// dependency alive through the orphan sweep below.
		roots := []string{entry.ID}
		if locked := e.Lock.Find(entry.ID); locked != nil && !locked.Explicit {
			if len(locked.RequiredBy) == 0 {
				// Nothing needs it any more; the sweep is about to drop it,
				// so installing its dependencies would only add more orphans.
				continue
			}
			roots = locked.RequiredBy
		}
		prov, err := Get(entry.project.Provider)
		if err != nil {
			continue
		}
		if err := e.installDeps(prov, entry.version, roots, filter,
			map[string]bool{entry.project.ID: true}, 0); err != nil {
			return err
		}
	}
	if err := e.pruneOrphans(); err != nil {
		return err
	}
	return e.save()
}

// StillDeclared lists mods that the upgrade removed from disk but left in the
// manifest, so the caller can say they will return when a build appears.
func (p *UpgradePlan) StillDeclared() []string {
	var out []string
	for _, e := range p.Blocked() {
		out = append(out, e.ID)
	}
	sort.Strings(out)
	return out
}

// ClientOnlyInstalled lists installed add-ons the provider marks as unusable on
// a server, which is worth pointing out while reviewing an upgrade.
func (e *Env) ClientOnlyInstalled() []string {
	var out []string
	for _, m := range e.Lock.Mods {
		if strings.EqualFold(m.ServerSide, "unsupported") {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out
}

// LockedClientOnly reports whether a locked entry is marked client-only.
func LockedClientOnly(m config.LockedMod) bool {
	return strings.EqualFold(m.ServerSide, "unsupported")
}

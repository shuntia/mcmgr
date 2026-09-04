// Package mods resolves, downloads and tracks server add-ons from Modrinth
// and CurseForge.
//
// Providers are normalised behind one interface so the command layer never
// has to care where a mod came from, and the lockfile records enough to
// reinstall it byte for byte.
package mods

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mcmgr/internal/config"
)

// Provider names as they appear in the manifest and lockfile.
const (
	ProviderModrinth   = "modrinth"
	ProviderCurseForge = "curseforge"
)

// Project is an add-on as listed by a provider.
type Project struct {
	Provider    string
	ID          string
	Slug        string
	Name        string
	Description string
	Downloads   int64
	Categories  []string
	URL         string

	// ClientSide and ServerSide are Modrinth's compatibility flags:
	// "required", "optional" or "unsupported". They are empty for providers
	// that do not publish the distinction.
	ClientSide string
	ServerSide string
}

// ClientOnly reports whether a project explicitly does not run on a server, so
// installing it server-side is at best useless and at worst a crash on start.
func (p Project) ClientOnly() bool { return p.ServerSide == "unsupported" }

// NeedsClient reports whether every player must install the project too, which
// is what a required client side means. Such a mod is not server-side: it
// works only for players who have it, and can refuse those who do not.
func (p Project) NeedsClient() bool { return p.ClientSide == "required" }

// ServerSideOnly reports whether a project runs on the server without players
// installing anything, which is the only kind offered by the guided flows.
// Providers that do not publish either flag pass, since there is nothing to
// refuse on.
func (p Project) ServerSideOnly() bool { return !p.ClientOnly() && !p.NeedsClient() }

// Dependency is another project a version needs.
type Dependency struct {
	ProjectID string
	VersionID string
	// Type is one of "required", "optional", "incompatible" or "embedded".
	Type string
}

// Version is one downloadable release of a project.
type Version struct {
	Provider     string
	ID           string
	ProjectID    string
	Number       string
	Name         string
	Filename     string
	URL          string
	SHA1         string
	Size         int64
	GameVersions []string
	Loaders      []string
	// ReleaseType is "release", "beta" or "alpha".
	ReleaseType  string
	Published    time.Time
	Dependencies []Dependency
}

// Channels says which pre-release types may be installed. Full releases are
// always eligible; beta and alpha are opted into independently, so a server can
// take betas without also taking alphas.
type Channels struct {
	Beta  bool
	Alpha bool
}

// Any reports whether any pre-release channel is enabled.
func (c Channels) Any() bool { return c.Beta || c.Alpha }

// Accepts reports whether a release type may be installed.
func (c Channels) Accepts(releaseType string) bool {
	switch strings.ToLower(releaseType) {
	case "", "release":
		return true
	case "beta":
		return c.Beta
	case "alpha":
		return c.Alpha
	}
	// An unrecognised type is treated as a full release rather than hidden.
	return true
}

// Names lists the enabled pre-release channels, for messages.
func (c Channels) Names() []string {
	var out []string
	if c.Beta {
		out = append(out, "beta")
	}
	if c.Alpha {
		out = append(out, "alpha")
	}
	return out
}

// ChannelsFrom reads the channel preferences recorded in a server's manifest.
func ChannelsFrom(d config.Defaults) Channels {
	return Channels{Beta: d.AllowBeta, Alpha: d.AllowAlpha}
}

// Filter narrows a search or version listing to what a server can actually run.
type Filter struct {
	Minecraft string
	Loaders   []string
	Channels  Channels
}

// FilterFor builds a Filter from a server config.
func FilterFor(cfg *config.Config, ch Channels) Filter {
	return Filter{
		Minecraft: cfg.Minecraft,
		Loaders:   cfg.Loader.ModrinthFacets(),
		Channels:  ch,
	}
}

// Provider is one upstream mod source.
type Provider interface {
	// Name is the identifier stored in the manifest.
	Name() string
	// Search returns projects matching a free-text query.
	Search(query string, f Filter, limit int) ([]Project, error)
	// Project looks up one project by slug or id.
	Project(idOrSlug string) (*Project, error)
	// Versions lists releases compatible with the filter, newest first.
	Versions(projectID string, f Filter) ([]Version, error)
	// Version fetches one specific release by id.
	Version(versionID string) (*Version, error)
}

// overrides substitutes providers by name. Only tests populate it, which is
// how they exercise resolution and dependency walking without the network.
var overrides map[string]Provider

// Get returns the provider with the given name. An empty name means Modrinth,
// which needs no credentials.
func Get(name string) (Provider, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = ProviderModrinth
	}
	if p, ok := overrides[key]; ok {
		return p, nil
	}
	switch key {
	case "", ProviderModrinth:
		return &Modrinth{}, nil
	case ProviderCurseForge, "cf", "curse":
		return NewCurseForge()
	}
	return nil, fmt.Errorf("unknown provider %q (supported: %s, %s)", name, ProviderModrinth, ProviderCurseForge)
}

// All returns every provider that is usable in this environment. CurseForge is
// skipped when no API key is configured, so search still works out of the box.
func All() []Provider {
	ps := []Provider{&Modrinth{}}
	if cf, err := NewCurseForge(); err == nil {
		ps = append(ps, cf)
	}
	return ps
}

// Stable reports whether a version is a full release.
func (v Version) Stable() bool { return v.ReleaseType == "" || v.ReleaseType == "release" }

// pickBest chooses the newest version whose release channel is enabled.
//
// Opting into a channel means wanting builds from it, so an enabled beta that
// is newer than the latest release wins. With no channels enabled only full
// releases are eligible.
func pickBest(versions []Version, f Filter) (*Version, error) {
	if len(versions) == 0 {
		return nil, errNoCompatible
	}
	sorted := make([]Version, len(versions))
	copy(sorted, versions)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Published.After(sorted[j].Published) })

	for _, v := range sorted {
		if f.Channels.Accepts(v.ReleaseType) {
			return &v, nil
		}
	}

	// Builds exist, but every one is in a channel that is switched off. Say
	// which, so the message can name the setting that would unlock it.
	seen := map[string]bool{}
	var blocked []string
	for _, v := range sorted {
		t := strings.ToLower(v.ReleaseType)
		if t != "" && !seen[t] {
			seen[t] = true
			blocked = append(blocked, t)
		}
	}
	sort.Strings(blocked)
	return nil, &UnstableOnlyError{Channels: blocked}
}

// errNoCompatible is returned when nothing matches the server's Minecraft
// version and loader.
var errNoCompatible = errors.New("no compatible version found")

// NoBuildError reports that a project has no build for the server's Minecraft
// version, and says which versions it does have builds for on this loader, so
// the caller can offer to move the server rather than just give up.
type NoBuildError struct {
	Name      string
	Slug      string
	Loader    string
	Minecraft string
	// Supported lists full releases with a build for this loader, newest
	// first. It is empty when the project has builds for no release at all,
	// or when looking them up failed.
	Supported []string
}

func (e *NoBuildError) Error() string {
	msg := fmt.Sprintf("%s has no release for %s %s", e.Name, e.Loader, e.Minecraft)
	if len(e.Supported) == 0 {
		return msg
	}
	newer, older := e.Nearest()
	msg += fmt.Sprintf("\n       it does have builds for %s", strings.Join(e.Nearby(6), ", "))
	switch {
	case newer != "":
		msg += fmt.Sprintf("\n       move this server with:  mcmgr upgrade %s   (then re-run this command)", newer)
	case older != "":
		msg += fmt.Sprintf("\n       move this server with:  mcmgr upgrade %s   (then re-run this command)"+
			"\n       that is a downgrade: a world already played on %s will not load", older, e.Minecraft)
	}
	return msg
}

// Is lets errors.Is(err, errNoCompatible) keep matching, for callers that only
// care that nothing fitted.
func (e *NoBuildError) Is(target error) bool { return target == errNoCompatible }

// Nearest is the supported version just above the server's, and the one just
// below it. Either is empty when there is nothing on that side.
func (e *NoBuildError) Nearest() (newer, older string) {
	return Nearest(e.Minecraft, e.Supported)
}

// Nearby lists up to n supported versions closest to the server's, oldest
// first, so a long list such as a library's reads as "the ones around yours"
// rather than a wall of history.
func (e *NoBuildError) Nearby(n int) []string {
	all := SortReleases(e.Supported) // newest first
	var above, below []string
	for _, v := range all {
		if CompareMC(v, e.Minecraft) > 0 {
			above = append(above, v)
		} else {
			below = append(below, v)
		}
	}
	half := n / 2
	if len(above) > half {
		above = above[len(above)-half:] // the ones nearest to current
	}
	if len(below) > n-len(above) {
		below = below[:n-len(above)]
	}
	// above is newest first; reverse into ascending order and put below first.
	out := make([]string, 0, len(above)+len(below))
	for i := len(below) - 1; i >= 0; i-- {
		out = append(out, below[i])
	}
	for i := len(above) - 1; i >= 0; i-- {
		out = append(out, above[i])
	}
	return out
}

// errUnstableOnly identifies the "only pre-release builds exist" case for
// errors.Is, while UnstableOnlyError carries which channels were involved.
var errUnstableOnly = errors.New("only pre-release builds are available")

// UnstableOnlyError reports that compatible builds exist but all of them are in
// channels that are not enabled. This is a different situation from having no
// build at all: the fix is enabling a channel, not waiting for a release.
type UnstableOnlyError struct {
	// Channels are the release types that were found and rejected.
	Channels []string
}

func (e *UnstableOnlyError) Error() string {
	if len(e.Channels) == 0 {
		return "only pre-release builds are available"
	}
	return fmt.Sprintf("only %s builds are available", strings.Join(e.Channels, " and "))
}

// Is lets errors.Is(err, errUnstableOnly) match, so callers can test the case
// without caring about the detail.
func (e *UnstableOnlyError) Is(target error) bool { return target == errUnstableOnly }

// Enable returns the flags and settings that would allow these channels.
func (e *UnstableOnlyError) Enable() string {
	if len(e.Channels) == 0 {
		return "--allow-unstable"
	}
	parts := make([]string, len(e.Channels))
	for i, c := range e.Channels {
		parts[i] = "--allow-" + c
	}
	return strings.Join(parts, " ")
}

// Setting names the manifest keys that would allow these channels for good.
func (e *UnstableOnlyError) Setting() string {
	if len(e.Channels) == 0 {
		return "defaults.allow_beta"
	}
	parts := make([]string, len(e.Channels))
	for i, c := range e.Channels {
		parts[i] = "defaults.allow_" + c
	}
	return strings.Join(parts, " / ")
}

// matches reports whether a version supports the filter's Minecraft version
// and at least one of its loaders.
func (v Version) matches(f Filter) bool {
	if f.Minecraft != "" && len(v.GameVersions) > 0 && !containsFold(v.GameVersions, f.Minecraft) {
		return false
	}
	if len(f.Loaders) > 0 && len(v.Loaders) > 0 {
		found := false
		for _, l := range f.Loaders {
			if containsFold(v.Loaders, l) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

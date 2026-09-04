package cli

import (
	"fmt"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
	"mcmgr/internal/prompt"
	"mcmgr/internal/ui"
)

// addonPicks collects the add-ons a user has chosen to install, in the order
// they chose them and without repeats.
type addonPicks struct {
	slugs []string
	seen  map[string]bool
}

func (p *addonPicks) add(slug string) bool {
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" || p.seen[slug] {
		return false
	}
	p.seen[slug] = true
	p.slugs = append(p.slugs, slug)
	return true
}

func (p *addonPicks) has(slug string) bool { return p.seen[strings.ToLower(slug)] }

// pickRecommended shows the curated list for a loader as a tick list, with the
// safe-everywhere entries already ticked. skip filters out entries that are
// already installed, so the prompt only offers what would actually change.
func pickRecommended(loader config.Loader, skip func(slug string) bool) ([]string, error) {
	var (
		options  []prompt.Option
		selected []string
	)
	for _, r := range mods.Recommended(loader) {
		if skip != nil && skip(r.Slug) {
			continue
		}
		options = append(options, prompt.Option{
			Label: fmt.Sprintf("%-18s %s", r.Name, r.Why),
			Value: r.Slug,
		})
		if r.Default {
			selected = append(selected, r.Slug)
		}
	}
	if len(options) == 0 {
		return nil, nil
	}
	noun := loader.ContentNoun()
	return prompt.MultiSelect(
		fmt.Sprintf("Recommended %ss", noun),
		"Well-known server-side "+noun+"s worth having from the start. The ticked ones\n"+
			"change no gameplay and are safe on any server.",
		options, selected)
}

// searchAndPick lets the user search Modrinth for anything the curated list
// does not cover, one query at a time, until an empty query ends it. Results
// are filtered to what the server can run, and client-only projects are hidden
// rather than offered and then refused.
func searchAndPick(filter mods.Filter, loader config.Loader, picks *addonPicks) error {
	prov, err := mods.Get(mods.ProviderModrinth)
	if err != nil {
		return err
	}
	noun := loader.ContentNoun()
	const none = ""
	for {
		query, err := prompt.Text("Search for more "+noun+"s",
			"Type a name or keyword to search Modrinth for "+noun+"s that run on this\n"+
				"server's version and loader. Leave it empty to finish.",
			"", nil)
		if err != nil {
			return err
		}
		if query == "" {
			return nil
		}

		results, err := prov.Search(query, filter, 10)
		if err != nil {
			ui.Warn("%v", err)
			continue
		}
		var options []prompt.Option
		hidden := 0
		for _, r := range results {
			// Only strictly server-side results are offered: nothing that
			// refuses to run on a server, and nothing every player would
			// have to install as well.
			if !r.ServerSideOnly() {
				hidden++
				continue
			}
			label := fmt.Sprintf("%-24s %s", r.Slug, truncate(r.Description, 52))
			if picks.has(r.Slug) {
				label = fmt.Sprintf("%-24s (already picked)", r.Slug)
			}
			options = append(options, prompt.Option{Label: label, Value: r.Slug})
		}
		if len(options) == 0 {
			msg := fmt.Sprintf("nothing matching %q runs on %s %s", query, loader, filter.Minecraft)
			if hidden > 0 {
				msg += fmt.Sprintf(" (%d hidden: client-only, or needing every player to install them)", hidden)
			}
			ui.Info("%s", msg)
			continue
		}
		options = append(options, prompt.Option{Label: "none of these", Value: none})

		desc := fmt.Sprintf("Results for %q on %s %s.", query, loader, filter.Minecraft)
		if hidden > 0 {
			desc += fmt.Sprintf(" %d hidden: client-only, or needing every player to install them.", hidden)
		}
		chosen, err := prompt.Select("Add which one?", desc, options, options[0].Value)
		if err != nil {
			return err
		}
		if chosen != none && picks.add(chosen) {
			ui.Ok("will install %s", chosen)
		}
	}
}

// installAddons installs each slug from Modrinth, continuing past failures so
// one add-on with no build for this version does not cost the rest. It returns
// what could not be installed.
//
// With offer set, an add-on with no build for the server's version prompts an
// offer to move the server to one it supports, after which it is retried;
// add-ons installed earlier in the list are refetched by that move.
func installAddons(cfg *config.Config, slugs []string, ch mods.Channels, force, offer bool) (failed []string, err error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	env, err := mods.Open(cfg)
	if err != nil {
		return nil, err
	}
	opts := mods.AddOptions{Provider: mods.ProviderModrinth, Channels: ch, Force: force}
	for _, slug := range slugs {
		aerr := env.Add(slug, opts)
		if aerr != nil {
			moved, merr := offerVersionMove(env, aerr, ch, offer)
			if merr != nil {
				return failed, merr
			}
			if moved {
				aerr = env.Add(slug, opts)
			}
		}
		if aerr != nil {
			ui.Warn("%s: %v", slug, aerr)
			failed = append(failed, slug)
		}
	}
	return failed, nil
}

// recommendedSlugs lists the slugs of a recommendation set.
func recommendedSlugs(rs []mods.Recommendation) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Slug
	}
	return out
}

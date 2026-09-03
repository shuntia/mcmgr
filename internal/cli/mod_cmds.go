package cli

import (
	"flag"
	"fmt"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
	"mcmgr/internal/ui"
)

func modCmd() *Command {
	return &Command{
		Name:    "mod",
		Aliases: []string{"mods", "plugin", "plugins"},
		Short:   "Install, update and remove mods and plugins",
		Long: "Install, update and remove mods and plugins from Modrinth and CurseForge.\n\n" +
			"Every command that writes to the mods directory refuses to run while the\n" +
			"server is up, so jars are never swapped under a live JVM.",
		Sub: []*Command{
			modAddCmd(),
			modRemoveCmd(),
			modListCmd(),
			modUpdateCmd(),
			modSyncCmd(),
			modSearchCmd(),
			modInfoCmd(),
		},
	}
}

// modFlags registers the options shared by the mutating mod commands. Each of
// these also has a manifest default under [defaults], which the flag overrides
// when it is actually given.
type modFlags struct {
	provider *string
	force    *bool
	channels channelFlags
}

func addSharedModFlags(fs *flag.FlagSet, withProvider bool) modFlags {
	m := modFlags{
		channels: addChannelFlags(fs),
		force: fs.Bool("force", false,
			"apply changes even while the server is running (takes effect on next restart)"),
	}
	if withProvider {
		m.provider = fs.String("provider", "", "modrinth or curseforge (default: defaults.provider)")
	}
	return m
}

// providerName resolves the provider from the flag, then the manifest default.
func (m modFlags) providerName(cfg *config.Config) string {
	name := deref(m.provider)
	if cfg != nil {
		return stringDefault(name, cfg.Defaults.Provider)
	}
	return name
}

func modAddCmd() *Command {
	cmd := &Command{
		Name:    "add",
		Aliases: []string{"install", "i"},
		Usage:   "<mod>...",
		Short:   "Install mods and their required dependencies",
		Long: "Install one or more mods, resolving each to the newest release compatible\n" +
			"with the server's Minecraft version and loader, and pulling in required\n" +
			"dependencies automatically.\n\n" +
			"A mod can be given as a slug, a project id, a provider-qualified name such as\n" +
			"curseforge:jei, a full project URL, or a free-text search term.",
		Examples: []string{
			"mcmgr mod add fabric-api sodium lithium",
			"mcmgr mod add curseforge:jei",
			"mcmgr mod add https://modrinth.com/mod/lithium",
			"mcmgr mod add sodium --version mc1.21.4-0.6.0",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr mod add")
		shared := addSharedModFlags(fs, true)
		var (
			version  = fs.String("version", "", "install and pin this exact version instead of the newest")
			noDeps   = fs.Bool("no-deps", false, "do not install required dependencies")
			clientOK = fs.Bool("allow-client-only", false,
				"install even if the provider says the mod does not run on servers")
		)
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr mod add", "<mod>..."); err != nil {
			return err
		}
		if *version != "" && len(rest) > 1 {
			return fmt.Errorf("--version applies to a single mod, but %d were given", len(rest))
		}

		env, err := g.modEnv()
		if err != nil {
			return err
		}
		opts := mods.AddOptions{
			Provider:        shared.providerName(env.Cfg),
			Version:         *version,
			NoDeps:          *noDeps,
			Channels:        shared.channels.resolve(fs, env.Cfg),
			Force:           *shared.force,
			AllowClientOnly: boolDefault(fs, "allow-client-only", clientOK, env.Cfg.Defaults.AllowClientOnly),
		}
		for _, query := range rest {
			if err := env.Add(query, opts); err != nil {
				return err
			}
		}
		ui.Ok("done; run 'mcmgr server restart' to load the changes")
		return nil
	}
	return cmd
}

func modRemoveCmd() *Command {
	cmd := &Command{
		Name:    "remove",
		Aliases: []string{"rm", "uninstall"},
		Usage:   "<mod>...",
		Short:   "Uninstall mods and clean up unused dependencies",
		Examples: []string{
			"mcmgr mod remove sodium",
			"mcmgr mod remove sodium lithium --keep-deps",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr mod remove")
		shared := addSharedModFlags(fs, false)
		keepDeps := fs.Bool("keep-deps", false, "leave dependencies installed even if nothing needs them")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr mod remove", "<mod>..."); err != nil {
			return err
		}

		env, err := g.modEnv()
		if err != nil {
			return err
		}
		return env.Remove(rest, mods.RemoveOptions{KeepDeps: *keepDeps, Force: *shared.force})
	}
	return cmd
}

func modListCmd() *Command {
	cmd := &Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Short:   "List installed mods",
		Examples: []string{
			"mcmgr mod list",
			"mcmgr mod list --outdated",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr mod list")
		var (
			outdated = fs.Bool("outdated", false, "check each provider for newer versions")
		)
		channels := addChannelFlags(fs)
		if _, err := parse(fs, g, args); err != nil {
			return err
		}

		env, err := g.modEnv()
		if err != nil {
			return err
		}
		noun := env.Cfg.Loader.ContentNoun()
		if len(env.Lock.Mods) == 0 {
			ui.Info("no %ss installed; add one with 'mcmgr mod add <name>'", noun)
			return nil
		}

		if !*outdated {
			rows := [][]string{{"NAME", "VERSION", "SOURCE", "KIND"}}
			clientOnly := 0
			for _, m := range env.Lock.Mods {
				kind := "dependency"
				if m.Explicit {
					kind = "requested"
				}
				if i := env.Cfg.FindMod(m.ID); i >= 0 && env.Cfg.Mods[i].Pinned() {
					kind += " (pinned)"
				}
				if mods.LockedClientOnly(m) {
					kind = ui.Yellow + "client-only" + ui.Reset
					clientOnly++
				}
				rows = append(rows, []string{m.ID, m.VersionNumber, m.Provider, kind})
			}
			fmt.Print(ui.Table(rows))
			if clientOnly > 0 {
				ui.Warn("%d installed %s(s) are marked as not running on servers; "+
					"they do nothing here and may stop the server starting", clientOnly, noun)
			}
			fmt.Printf("\n%d %s(s) for %s %s\n", len(env.Lock.Mods), noun, env.Cfg.Loader, env.Cfg.Minecraft)
			return nil
		}

		ui.Step("checking %d %s(s) for updates", len(env.Lock.Mods), noun)
		rows := [][]string{{"NAME", "INSTALLED", "LATEST", "STATUS"}}
		updates := 0
		for _, row := range env.CheckOutdated(channels.resolve(fs, env.Cfg)) {
			latest, status := row.Entry.VersionNumber, ui.Dim+"up to date"+ui.Reset
			switch {
			case row.Problem != nil:
				status = ui.Yellow + "check failed" + ui.Reset
			case row.Pinned:
				status = "pinned"
			case row.Latest != "":
				latest = row.Latest
				status = ui.Green + "update available" + ui.Reset
				updates++
			}
			rows = append(rows, []string{row.Entry.ID, row.Entry.VersionNumber, latest, status})
		}
		fmt.Print(ui.Table(rows))
		if updates > 0 {
			fmt.Printf("\n%d update(s) available; run 'mcmgr mod update'\n", updates)
		} else {
			fmt.Printf("\neverything is up to date\n")
		}
		return nil
	}
	return cmd
}

func modUpdateCmd() *Command {
	cmd := &Command{
		Name:    "update",
		Aliases: []string{"upgrade", "up"},
		Usage:   "[mod]...",
		Short:   "Update mods to their newest compatible versions",
		Long: "Update installed mods. With no arguments every unpinned mod is considered.\n" +
			"Mods pinned to an exact version in mcmgr.json are reported and skipped.",
		Examples: []string{
			"mcmgr mod update",
			"mcmgr mod update --dry-run",
			"mcmgr mod update sodium",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr mod update")
		shared := addSharedModFlags(fs, false)
		dryRun := fs.Bool("dry-run", false, "show what would change without downloading anything")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}

		env, err := g.modEnv()
		if err != nil {
			return err
		}
		return env.Update(fs.Args(), mods.UpdateOptions{
			DryRun:   *dryRun,
			Channels: shared.channels.resolve(fs, env.Cfg),
			Force:    *shared.force,
		})
	}
	return cmd
}

func modSyncCmd() *Command {
	cmd := &Command{
		Name:  "sync",
		Short: "Make the mods directory match mcmgr.json",
		Long: "Install everything declared in mcmgr.json, re-download files that are missing\n" +
			"or corrupt, and remove mods that are no longer declared.\n\n" +
			"Run this after cloning a server config or editing mcmgr.json by hand.",
		Examples: []string{
			"mcmgr mod sync",
			"mcmgr mod sync --prune   # also delete jars mcmgr did not install",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr mod sync")
		shared := addSharedModFlags(fs, false)
		prune := fs.Bool("prune", false, "delete jars in the mods directory that mcmgr did not install")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}

		env, err := g.modEnv()
		if err != nil {
			return err
		}
		return env.Sync(mods.SyncOptions{
			Prune:    boolDefault(fs, "prune", prune, env.Cfg.Defaults.PruneUnmanaged),
			Channels: shared.channels.resolve(fs, env.Cfg),
			Force:    *shared.force,
		})
	}
	return cmd
}

func modSearchCmd() *Command {
	cmd := &Command{
		Name:    "search",
		Aliases: []string{"find"},
		Usage:   "<query>",
		Short:   "Search for mods compatible with this server",
		Long: "Search providers for mods matching a query.\n\n" +
			"Results are filtered to the selected server's Minecraft version and loader\n" +
			"unless --any is given.",
		Examples: []string{
			"mcmgr mod search performance",
			"mcmgr mod search jei --provider curseforge",
			"mcmgr mod search worldedit --any",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr mod search")
		var (
			providerName = fs.String("provider", "", "modrinth (default), curseforge, or all")
			limit        = fs.Int("limit", 15, "maximum results per provider")
			any          = fs.Bool("any", false, "do not filter by the server's version and loader")
		)
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr mod search", "<query>"); err != nil {
			return err
		}
		query := strings.Join(rest, " ")

		var filter mods.Filter
		if !*any {
			cfg, err := g.config()
			if err != nil {
				return fmt.Errorf("%w\n\n       or search without a server using --any", err)
			}
			filter = mods.FilterFor(cfg, mods.Channels{Beta: true, Alpha: true})
		}

		var providers []mods.Provider
		if strings.EqualFold(*providerName, "all") {
			providers = mods.All()
		} else {
			p, err := mods.Get(*providerName)
			if err != nil {
				return err
			}
			providers = []mods.Provider{p}
		}

		found := 0
		for _, p := range providers {
			results, err := p.Search(query, filter, *limit)
			if err != nil {
				ui.Warn("%s: %v", p.Name(), err)
				continue
			}
			if len(results) == 0 {
				continue
			}
			if len(providers) > 1 {
				fmt.Printf("\n%s%s%s\n", ui.Bold, p.Name(), ui.Reset)
			}
			for _, r := range results {
				found++
				marker := ""
				if r.ClientOnly() {
					// Worth seeing before installing rather than after.
					marker = ui.Yellow + "  client-only" + ui.Reset
				}
				fmt.Printf("  %s%-24s%s %s%s%s%s\n", ui.Bold, r.Slug, ui.Reset,
					ui.Dim, downloadsLabel(r.Downloads), ui.Reset, marker)
				fmt.Printf("  %-24s %s\n", "", truncate(r.Description, 68))
			}
		}
		if found == 0 {
			scope := "for this server"
			if *any {
				scope = "anywhere"
			}
			return fmt.Errorf("nothing matching %q was found %s", query, scope)
		}
		fmt.Printf("\n%sinstall one with: mcmgr mod add <name>%s\n", ui.Dim, ui.Reset)
		return nil
	}
	return cmd
}

func modInfoCmd() *Command {
	cmd := &Command{
		Name:  "info",
		Usage: "<mod>",
		Short: "Show details and available versions for a mod",
		Examples: []string{
			"mcmgr mod info sodium",
			"mcmgr mod info curseforge:jei",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr mod info")
		var (
			providerName = fs.String("provider", "", "modrinth (default) or curseforge")
			limit        = fs.Int("versions", 8, "number of recent versions to list")
		)
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr mod info", "<mod>"); err != nil {
			return err
		}

		cfg, err := g.config()
		if err != nil {
			return err
		}
		env, err := g.modEnv()
		if err != nil {
			return err
		}

		provName, ref := mods.ParseRef(rest[0], *providerName)
		prov, err := mods.Get(provName)
		if err != nil {
			return err
		}
		proj, err := prov.Project(ref)
		if err != nil {
			return err
		}

		heading("%s", proj.Name)
		field("slug", "%s", proj.Slug)
		field("source", "%s", proj.Provider)
		field("downloads", "%s", downloadsLabel(proj.Downloads))
		if proj.URL != "" {
			field("url", "%s", proj.URL)
		}
		if proj.Description != "" {
			field("about", "%s", truncate(proj.Description, 70))
		}
		if installed := env.Lock.Find(proj.Slug); installed != nil {
			field("installed", "%s%s%s", ui.Green, installed.VersionNumber, ui.Reset)
		}

		filter := mods.FilterFor(cfg, mods.Channels{Beta: true, Alpha: true})
		versions, err := prov.Versions(proj.ID, filter)
		if err != nil {
			return err
		}
		if len(versions) == 0 {
			fmt.Printf("\n%sno versions for %s %s%s\n", ui.Yellow, cfg.Loader, cfg.Minecraft, ui.Reset)
			return nil
		}

		fmt.Printf("\n%sVERSIONS for %s %s%s\n", ui.Bold, cfg.Loader, cfg.Minecraft, ui.Reset)
		rows := [][]string{{"VERSION", "TYPE", "PUBLISHED", "FILE"}}
		for i, v := range versions {
			if i >= *limit {
				break
			}
			rows = append(rows, []string{v.Number, v.ReleaseType, v.Published.Format("2006-01-02"), v.Filename})
		}
		fmt.Print(ui.Table(rows))
		return nil
	}
	return cmd
}

// --- helpers -----------------------------------------------------------

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func downloadsLabel(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM downloads", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk downloads", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d downloads", n)
	}
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

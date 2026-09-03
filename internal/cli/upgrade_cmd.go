package cli

import (
	"fmt"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
	"mcmgr/internal/prompt"
	"mcmgr/internal/props"
	"mcmgr/internal/server"
	"mcmgr/internal/ui"
	"mcmgr/internal/warn"
)

func upgradeCmd() *Command {
	cmd := &Command{
		Name:    "upgrade",
		Aliases: []string{"up"},
		Usage:   "[version]",
		Short:   "Move the server to a new Minecraft version and refetch its mods",
		Long: "Move a server to a different Minecraft version: reinstall the server binary\n" +
			"for that version, then refetch every add-on at a build that matches it.\n\n" +
			"The plan is worked out and shown before anything changes, so you can see\n" +
			"which mods have a build for the new version and which do not. Mods with no\n" +
			"compatible build are removed from the mods directory, because one built for\n" +
			"the old version usually stops the server starting, but they stay in " + config.ManifestName + "\n" +
			"and return on a later 'mcmgr mod sync' once their authors catch up.\n\n" +
			"With no version, the newest Minecraft release is used.",
		Examples: []string{
			"mcmgr upgrade --dry-run       # just show what would happen",
			"mcmgr upgrade 1.21.5",
			"mcmgr upgrade                 # to the latest release",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr upgrade")
		var (
			to     = fs.String("to", "", "target Minecraft version (same as the positional argument)")
			dryRun = fs.Bool("dry-run", false, "show the plan without changing anything")
			yes    = fs.Bool("yes", false, "do not ask for confirmation")
		)
		channels := addChannelFlags(fs)
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}

		cfg, err := g.config()
		if err != nil {
			return err
		}
		env, err := mods.Open(cfg)
		if err != nil {
			return err
		}

		// Swapping the server binary under a live JVM is never sensible, so
		// this guard has no override.
		if err := env.Guard(false); err != nil {
			return err
		}

		target := *to
		if target == "" && len(rest) > 0 {
			target = rest[0]
		}
		if target == "" {
			ui.Step("looking up the latest Minecraft release")
			if target, err = server.LatestRelease(); err != nil {
				return err
			}
		}
		if target == cfg.Minecraft {
			ui.Info("%s is already on Minecraft %s", cfg.Name, target)
			return nil
		}

		ui.Step("checking which add-ons have a build for %s", target)
		ch := channels.resolve(fs, cfg)
		plan, err := env.PlanUpgrade(target, ch)
		if err != nil {
			return err
		}
		printUpgradePlan(cfg, plan)

		// Only the world warning applies here. The generic "your mods are for
		// the old version" caution is left out on purpose: the plan above
		// already says exactly what happens to each one, which is better
		// information than a count.
		p, _ := props.Load(cfg.Dir())
		ctx := warn.Context{Dir: cfg.Dir(), Old: cfg.Minecraft}
		if p != nil {
			ctx.LevelName = p.GetOr("level-name", "world")
		}
		danger := warn.PrintAll(warn.ForManifest("minecraft", target, ctx))

		if *dryRun {
			ui.Info("dry run: nothing was changed")
			return nil
		}

		if !*yes {
			question := fmt.Sprintf("Upgrade %s from %s to %s?", cfg.Name, cfg.Minecraft, target)
			if !prompt.Interactive() {
				return fmt.Errorf("this upgrade needs confirmation, but there is no terminal to ask on\n" +
					"       re-run with --yes, or --dry-run to see the plan first")
			}
			ok, cerr := prompt.Confirm(question, upgradeConfirmBody(plan, danger), false)
			if cerr != nil {
				return promptErr(cerr)
			}
			if !ok {
				return fmt.Errorf("cancelled; nothing was changed")
			}
		}

		// The server binary first: if this fails, the manifest is untouched and
		// the server still starts on its old version.
		previous, previousLoaderVer := cfg.Minecraft, cfg.LoaderVer
		cfg.Minecraft = target
		cfg.LoaderVer = "" // let the loader resolve a build for the new version

		ui.Step("installing the %s server for %s", cfg.Loader, target)
		if err := server.Install(cfg, server.InstallOptions{}); err != nil {
			cfg.Minecraft, cfg.LoaderVer = previous, previousLoaderVer
			return fmt.Errorf("%w\n\n       nothing was changed; %s is still on %s",
				err, cfg.Name, previous)
		}
		_ = server.RecordJavaRequirement(cfg)
		if err := cfg.Save(); err != nil {
			return err
		}

		ui.Step("refetching add-ons for %s", target)
		if err := env.ApplyUpgrade(plan, ch); err != nil {
			return err
		}

		ui.Ok("%s upgraded from %s to %s", cfg.Name, previous, target)
		if left := plan.StillDeclared(); len(left) > 0 {
			ui.Info("       still declared but not installed: %s", strings.Join(left, ", "))
			ui.Info("       run 'mcmgr mod sync' later to pick them up when builds appear")
		}
		ui.Info("       back up the world before the first start if you have not already")
		return nil
	}
	return cmd
}

// printUpgradePlan shows every add-on's fate before anything is changed.
func printUpgradePlan(cfg *config.Config, plan *mods.UpgradePlan) {
	fmt.Println()
	heading("%s: Minecraft %s -> %s", cfg.Name, plan.From, plan.To)

	if len(plan.Entries) == 0 {
		ui.Info("  no add-ons installed")
		fmt.Println()
		return
	}

	rows := [][]string{{"NAME", "INSTALLED", "AFTER UPGRADE", "STATUS"}}
	for _, e := range plan.Entries {
		switch {
		case e.Pinned:
			rows = append(rows, []string{e.ID, e.Current, e.Current, ui.Yellow + e.Reason + ui.Reset})
		case e.Available() && e.Current == e.Target:
			rows = append(rows, []string{e.ID, e.Current, e.Target, ui.Dim + "already compatible" + ui.Reset})
		case e.Available():
			rows = append(rows, []string{e.ID, e.Current, e.Target, ui.Green + "will update" + ui.Reset})
		case e.UnstableOnly:
			rows = append(rows, []string{e.ID, e.Current, "-", ui.Yellow + e.Reason + ui.Reset})
		default:
			rows = append(rows, []string{e.ID, e.Current, "-", ui.Red + e.Reason + ui.Reset})
		}
	}
	fmt.Print(ui.Table(rows))

	ready, blocked, pinned := len(plan.Ready()), len(plan.Blocked()), len(plan.PinnedEntries())
	fmt.Printf("\n  %d ready", ready)
	if blocked > 0 {
		fmt.Printf(", %s%d with no build for %s%s", ui.Red, blocked, plan.To, ui.Reset)
	}
	if pinned > 0 {
		fmt.Printf(", %d pinned", pinned)
	}
	fmt.Println()
	// Name the specific channels involved, so the hint is one the user can act
	// on rather than a blanket "unstable".
	channels := map[string]bool{}
	for _, e := range plan.Entries {
		for _, c := range e.Channels {
			channels[c] = true
		}
	}
	if len(channels) > 0 {
		var flags, settings []string
		for _, c := range []string{"beta", "alpha"} {
			if channels[c] {
				flags = append(flags, "--allow-"+c)
				settings = append(settings, "defaults.allow_"+c)
			}
		}
		fmt.Printf("  %s%s builds exist for some of these; %s would use them%s\n",
			ui.Dim, strings.Join(keys(channels), " and "), strings.Join(flags, " "), ui.Reset)
		fmt.Printf("  %sset %s to make that the default%s\n",
			ui.Dim, strings.Join(settings, " / "), ui.Reset)
	}
}

// upgradeConfirmBody summarises the consequences inside the confirmation
// prompt, where anything printed to stderr would already have scrolled away.
func upgradeConfirmBody(plan *mods.UpgradePlan, danger bool) string {
	var b strings.Builder
	if blocked := plan.Blocked(); len(blocked) > 0 {
		names := make([]string, len(blocked))
		for i, e := range blocked {
			names[i] = e.ID
		}
		fmt.Fprintf(&b, "These have no build for %s and will be removed from the mods\ndirectory: %s\n\n",
			plan.To, strings.Join(names, ", "))
	}
	if danger {
		b.WriteString("The world will be upgraded to the new version's format, which cannot\n" +
			"be undone. Back it up first if it matters.\n\n")
	}
	b.WriteString("The server binary and every add-on will be refetched.")
	return b.String()
}

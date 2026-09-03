package cli

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/prompt"
	"mcmgr/internal/server"
	"mcmgr/internal/settings"
	"mcmgr/internal/ui"
	"mcmgr/internal/warn"
)

func configCmd() *Command {
	cmd := &Command{
		Name:    "config",
		Aliases: []string{"cfg"},
		Short:   "Read and change server and global settings",
		Long: "Read and change settings across all three places they live:\n\n" +
			"  mcmgr.toml          mcmgr's own settings for this server\n" +
			"  server.properties   Minecraft's settings, such as the whitelist\n" +
			"  global              machine-wide settings, such as the CurseForge key\n\n" +
			"Run it with no arguments to edit settings interactively. Settings that can\n" +
			"lock you out or damage a world explain themselves before being applied.",
		Sub: []*Command{
			configGetCmd(),
			configSetCmd(),
			configListCmd(),
			configEditCmd(),
		},
		Examples: []string{
			"mcmgr config                          # interactive editor",
			"mcmgr config get whitelist",
			"mcmgr config set whitelist true",
			"mcmgr config set memory 6G",
			"mcmgr config list",
		},
	}
	// Bare "mcmgr config" opens the editor rather than printing help.
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr config")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		return runConfigEditor(g)
	}
	return cmd
}

// openStore resolves the server (when one applies) and loads every settings
// file. globalOnly skips server resolution so global keys work anywhere.
func openStore(g *globals, globalOnly bool) (*settings.Store, error) {
	if globalOnly {
		return settings.Open(nil)
	}
	cfg, err := g.config()
	if err != nil {
		// Global settings still work with no server selected.
		if errors.Is(err, config.ErrNoServer) {
			return settings.Open(nil)
		}
		return nil, err
	}
	store, err := settings.Open(cfg)
	if err != nil {
		return nil, err
	}
	running, _, rerr := server.IsRunning(cfg.Dir())
	if rerr == nil {
		store.SetRunning(running)
	}
	return store, nil
}

func configGetCmd() *Command {
	cmd := &Command{
		Name:  "get",
		Usage: "<key>",
		Short: "Print one setting's current value",
		Examples: []string{
			"mcmgr config get minecraft",
			"mcmgr config get properties.online-mode",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr config get")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr config get", "<key>"); err != nil {
			return err
		}
		store, err := openStore(g, false)
		if err != nil {
			return err
		}
		key, err := store.Resolve(rest[0])
		if err != nil {
			return err
		}
		fmt.Println(store.Get(key))
		return nil
	}
	return cmd
}

func configSetCmd() *Command {
	cmd := &Command{
		Name:  "set",
		Usage: "<key> <value>",
		Short: "Change one setting",
		Long: "Change one setting, in whichever file owns it.\n\n" +
			"Changes to server.properties are refused while the server is running, because\n" +
			"the server rewrites that file when it stops and would discard them.",
		Examples: []string{
			"mcmgr config set whitelist true",
			"mcmgr config set memory 6G",
			"mcmgr config set properties.motd 'Friends only'",
			"mcmgr config set global.curseforge_key <key>",
		},
	}
	var (
		force *bool
		yes   *bool
	)
	cmd.Flags = func(fs *flag.FlagSet) {
		force = fs.Bool("force", false, "apply even when the server is running or a warning is severe")
		yes = fs.Bool("yes", false, "do not ask for confirmation on dangerous changes")
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr config set")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 2, "mcmgr config set", "<key> <value>"); err != nil {
			return err
		}

		store, err := openStore(g, false)
		if err != nil {
			return err
		}
		key, err := store.Resolve(rest[0])
		if err != nil {
			return err
		}
		value := strings.Join(rest[1:], " ")

		if err := applySetting(store, key, value, *force, *yes); err != nil {
			return err
		}
		if err := store.Save(); err != nil {
			return err
		}
		shown := value
		if key.Kind == settings.KindSecret {
			shown = "(set)"
		}
		ui.Ok("%s = %s  %s(%s)%s", key.Name, shown, ui.Dim, key.Scope, ui.Reset)
		if store.Ctx.Running {
			ui.Info("       the server is running; this takes effect on the next start")
		}
		return nil
	}
	return cmd
}

// applySetting validates, warns, confirms and then applies one change.
func applySetting(store *settings.Store, key settings.Key, value string, force, yes bool) error {
	if err := store.Validate(key, value); err != nil {
		return err
	}
	if store.Get(key) == value {
		ui.Info("%s is already %s", key.Name, value)
		return nil
	}

	// Editing server.properties under a running server loses the edit.
	if key.Scope == settings.Properties && store.Ctx.Running && !force {
		warn.Print(warn.PropertiesWhileRunning)
		return fmt.Errorf("refusing to edit %s while the server is running", key.Name)
	}

	warnings := store.Warnings(key, value)
	if len(warnings) > 0 {
		danger := warn.PrintAll(warnings)
		if danger && !yes && !force {
			ok, err := confirmDanger(fmt.Sprintf("Set %s to %s anyway?", key.Name, value))
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("cancelled; nothing was changed")
			}
		}
	}
	return store.Set(key, value)
}

// confirmDanger asks for confirmation, or refuses when there is no terminal to
// ask on so a script cannot silently do something destructive.
func confirmDanger(question string) (bool, error) {
	if !prompt.Interactive() {
		return false, fmt.Errorf("this change needs confirmation, but there is no terminal to ask on\n" +
			"       re-run with --yes if you are sure")
	}
	ok, err := prompt.Confirm(question, "", false)
	if err != nil {
		return false, promptErr(err)
	}
	return ok, nil
}

func configListCmd() *Command {
	cmd := &Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Short:   "Show every setting and its current value",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr config list")
		showHelp := fs.Bool("help-text", false, "include a description of each setting")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		store, err := openStore(g, false)
		if err != nil {
			return err
		}

		keys := store.Keys()
		settings.SortKeys(keys)

		var current settings.Scope = -1
		for _, k := range keys {
			if k.Scope != current {
				current = k.Scope
				fmt.Printf("\n%s%s%s\n", ui.Bold, k.Scope, ui.Reset)
			}
			value := store.Get(k)
			if value == "" {
				value = ui.Dim + "(unset)" + ui.Reset
			}
			fmt.Printf("  %-34s %s\n", k.Name, value)
			if *showHelp && k.Help != "" {
				fmt.Printf("  %s%-34s %s%s\n", ui.Dim, "", k.Help, ui.Reset)
			}
		}
		fmt.Printf("\n%schange one with: mcmgr config set <key> <value>%s\n", ui.Dim, ui.Reset)
		return nil
	}
	return cmd
}

func configEditCmd() *Command {
	cmd := &Command{
		Name:  "edit",
		Short: "Edit settings interactively",
		Long: "Pick a setting with the arrow keys, change it, and repeat until done.\n" +
			"This is what 'mcmgr config' runs when given no arguments.",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr config edit")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		return runConfigEditor(g)
	}
	return cmd
}

// runConfigEditor is the interactive settings editor.
//
// The default is an in-place editor: one list, values changed where they sit.
// Accessible mode falls back to the prompt-per-setting flow below, which is
// what works with a screen reader and over a pipe.
func runConfigEditor(g *globals) error {
	if !prompt.Interactive() {
		return fmt.Errorf("interactive editing needs a terminal\n" +
			"       use 'mcmgr config set <key> <value>' instead")
	}
	store, err := openStore(g, false)
	if err != nil {
		return err
	}

	name := "global settings"
	if store.Cfg != nil {
		name = store.Cfg.Name
	}

	if !prompt.Accessible() {
		return runConfigEditorTUI(store, "Settings for "+name)
	}

	if store.Ctx.Running {
		ui.Warn("%s is running: server.properties cannot be edited until it stops", name)
	}

	for {
		keys := store.Keys()
		settings.SortKeys(keys)

		options := make([]prompt.Option, 0, len(keys)+1)
		for _, k := range keys {
			value := store.Get(k)
			if value == "" {
				value = "(unset)"
			}
			options = append(options, prompt.Option{
				Label: fmt.Sprintf("%-32s %s", k.Name, value),
				Value: k.Name,
			})
		}
		options = append(options, prompt.Option{Label: "Save and exit", Value: ""})

		choice, err := prompt.Select("Settings for "+name,
			"Choose a setting to change, or save and exit.", options, "")
		if err != nil {
			return promptErr(err)
		}
		if choice == "" {
			break
		}

		key, err := store.Resolve(choice)
		if err != nil {
			return err
		}
		if err := editOne(store, key); err != nil {
			if errors.Is(err, prompt.ErrAborted) {
				continue // cancelling one field returns to the list
			}
			return err
		}
	}

	if !store.Changed() {
		ui.Info("no changes")
		return nil
	}
	if err := store.Save(); err != nil {
		return err
	}
	ui.Ok("settings saved")
	if store.Ctx.Running {
		ui.Info("       the server is running; changes take effect on the next start")
	}
	return nil
}

// editOne prompts for a single setting's new value, using the widget that
// suits its kind, and applies it after showing any warnings.
func editOne(store *settings.Store, key settings.Key) error {
	current := store.Get(key)
	desc := key.Help
	if desc != "" {
		desc += "\n"
	}
	desc += "Currently: " + orUnset(current)

	if key.Scope == settings.Properties && store.Ctx.Running {
		return fmt.Errorf("cannot edit %s while the server is running; stop it first", key.Name)
	}

	var value string
	var err error
	switch {
	case key.Kind == settings.KindBool:
		var b bool
		b, err = prompt.Confirm(key.Name, desc, current == "true")
		value = strconv.FormatBool(b)

	case len(key.Choices) > 0:
		opts := make([]prompt.Option, len(key.Choices))
		for i, c := range key.Choices {
			opts[i] = prompt.Opt(c)
		}
		value, err = prompt.Select(key.Name, desc, opts, current)

	case key.Kind == settings.KindSecret:
		value, err = prompt.Secret(key.Name, desc)

	default:
		value, err = prompt.Text(key.Name, desc, current, func(s string) error {
			return store.Validate(key, s)
		})
	}
	if err != nil {
		return err
	}
	if value == current {
		return nil
	}

	// Warnings go through a Pause so they survive the form redraw, and a
	// dangerous change needs an explicit yes.
	for _, w := range store.Warnings(key, value) {
		if w.Level == warn.Danger {
			ok, cerr := prompt.Confirm("Apply this change?", warn.Summary(w), false)
			if cerr != nil {
				return cerr
			}
			if !ok {
				return nil
			}
			continue
		}
		if perr := prompt.Pause(w.Title, w.Body, "Got it"); perr != nil {
			return perr
		}
	}
	return store.Set(key, value)
}

func orUnset(v string) string {
	if v == "" {
		return "(unset)"
	}
	return v
}

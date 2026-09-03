package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/prompt"
	"mcmgr/internal/props"
	"mcmgr/internal/server"
	"mcmgr/internal/ui"
	"mcmgr/internal/warn"
)

// newFlagValues collects everything `mcmgr new` needs, from flags first and
// from prompts for whatever is still missing.
type newFlagValues struct {
	mc         *string
	loader     *string
	memory     *string
	javaPath   *string
	port       *string
	whitelist  *string
	onlineMode *string
	acceptEULA *bool
	noRegister *bool
	noInput    *bool
	javaArgs   stringList
}

// register declares the flags shared by `mcmgr new` and `mcmgr server create`.
// Tri-state options are strings so "not given" is distinguishable from "false",
// which is what decides whether the user gets prompted.
func (v *newFlagValues) register(fs *flag.FlagSet) {
	v.mc = fs.String("mc", "", "Minecraft version (default: latest release)")
	v.loader = fs.String("loader", "", "server platform: "+config.JoinLoaders())
	v.memory = fs.String("memory", "", "heap size for the JVM, e.g. 4G")
	v.javaPath = fs.String("java", "", "path to the java binary (default: java from PATH)")
	v.port = fs.String("port", "", "server port (default 25565)")
	v.whitelist = fs.String("whitelist", "", "enable the whitelist: true or false")
	v.onlineMode = fs.String("online-mode", "", "verify players against Mojang: true or false")
	v.acceptEULA = fs.Bool("accept-eula", false, "accept the Minecraft EULA (https://aka.ms/MinecraftEULA)")
	v.noRegister = fs.Bool("no-register", false, "do not add the server to the global registry")
	v.noInput = fs.Bool("no-input", false, "never prompt; fail if a required value is missing")
	fs.Var(&v.javaArgs, "java-arg", "extra JVM argument; repeatable")
}

func newCmd() *Command {
	cmd := &Command{
		Name:    "new",
		Aliases: []string{"create"},
		Usage:   "[name] [directory]",
		Short:   "Create a server, asking for anything not given",
		Long: "Create a new Minecraft server.\n\n" +
			"Anything not supplied as a flag is asked for interactively: navigate with the\n" +
			"arrow keys, type to fill in text, and press enter to accept. Supply every flag\n" +
			"(or pass --no-input) to run it unattended in a script.\n\n" +
			"Settings that can lock you out or damage a world are explained before they are\n" +
			"applied.",
		Examples: []string{
			"mcmgr new                                   # ask for everything",
			"mcmgr new survival --loader fabric          # ask only for the rest",
			"mcmgr new lobby --loader paper --mc 1.21.4 --memory 4G --accept-eula --no-input",
		},
	}
	cmd.Run = func(args []string) error { return runNew(cmd, "mcmgr new", args) }
	return cmd
}

// serverCreateCmd exposes the same implementation under `mcmgr server create`.
func serverCreateCmd() *Command {
	cmd := &Command{
		Name:  "create",
		Usage: "[name] [directory]",
		Short: "Create a server (same as 'mcmgr new')",
		Long: "Create a new Minecraft server. Identical to 'mcmgr new', which is the shorter\n" +
			"way to say it.",
	}
	cmd.Run = func(args []string) error { return runNew(cmd, "mcmgr server create", args) }
	return cmd
}

func runNew(cmd *Command, path string, args []string) error {
	fs, g := newFlags(cmd, path)
	var v newFlagValues
	v.register(fs)

	rest, err := parse(fs, g, args)
	if err != nil {
		return err
	}
	interactive := prompt.Interactive() && !*v.noInput

	// --- name -----------------------------------------------------------
	name := ""
	if len(rest) > 0 {
		name = rest[0]
	}
	if name == "" {
		if !interactive {
			return fmt.Errorf("a server name is required\n\n  usage: %s <name> [directory]", path)
		}
		suggested := suggestName()
		if name, err = prompt.Text("Server name",
			"Used to refer to this server, e.g. 'mcmgr server launch -s "+suggested+"'.",
			suggested, prompt.ValidateName); err != nil {
			return promptErr(err)
		}
	}
	if err := prompt.ValidateName(name); err != nil {
		return err
	}

	// --- directory ------------------------------------------------------
	dir := filepath.Join(".", name)
	if len(rest) > 1 {
		dir = rest[1]
	} else if interactive {
		if dir, err = prompt.Text("Directory",
			"Where the world, jar and configuration are stored.",
			dir, prompt.ValidateNonEmpty("directory")); err != nil {
			return promptErr(err)
		}
	}
	if config.Exists(dir) {
		return fmt.Errorf("%s is already an mcmgr server\n"+
			"       use 'mcmgr config' to change its settings", dir)
	}

	// Machine-wide preferences stand in for the flags people retype most.
	globals, gerr := config.LoadGlobal()
	if gerr != nil {
		globals = &config.Global{}
	}

	// --- loader ---------------------------------------------------------
	loaderName := *v.loader
	if loaderName == "" {
		loaderName = globals.DefaultLoader
	}
	if loaderName == "" {
		if !interactive {
			loaderName = string(config.Vanilla)
		} else if loaderName, err = prompt.Select("Server platform",
			"Vanilla runs the game as Mojang ships it. Fabric and NeoForge load mods.\n"+
				"Paper is a performance-focused server that loads Bukkit plugins.",
			loaderOptions(), string(config.Fabric)); err != nil {
			return promptErr(err)
		}
	}
	loader, err := config.ParseLoader(loaderName)
	if err != nil {
		return err
	}
	if loader == config.Quilt {
		ui.Warn("mcmgr cannot install Quilt automatically; after this, install it with the " +
			"Quilt installer and run 'mcmgr server set-jar <jar>'")
	}

	// --- Minecraft version ----------------------------------------------
	version := *v.mc
	if version == "" {
		ui.Step("looking up the latest Minecraft release")
		latest, lerr := server.LatestRelease()
		if lerr != nil {
			return lerr
		}
		if !interactive {
			version = latest
			ui.Info("  using Minecraft %s", version)
		} else if version, err = prompt.Text("Minecraft version",
			"The version this server runs. Enter to accept the latest release.",
			latest, prompt.ValidateNonEmpty("version")); err != nil {
			return promptErr(err)
		}
	}

	// --- memory and port -------------------------------------------------
	memory := *v.memory
	if memory == "" {
		memory = globals.DefaultMemory
	}
	if memory == "" && interactive {
		if memory, err = prompt.Text("Memory",
			"Maximum heap the server may use, as a JVM size such as 2G or 4096M.",
			"2G", nil); err != nil {
			return promptErr(err)
		}
	}

	port := *v.port
	if port == "" && interactive {
		if port, err = prompt.Text("Port",
			"TCP port players connect to. 25565 is the default Minecraft port.",
			"25565", prompt.ValidateInt("port", 1, 65535)); err != nil {
			return promptErr(err)
		}
	}
	if port != "" {
		if _, perr := strconv.Atoi(port); perr != nil {
			return fmt.Errorf("--port must be a whole number, got %q", port)
		}
	}

	// --- whitelist -------------------------------------------------------
	whitelist, err := askBool(v.whitelist, interactive, false,
		"Enable the whitelist? (recommended)",
		"Recommended: only players you have added can join, which is the difference\n"+
			"between a private server and one anyone who finds the address can enter.\n\n"+
			"Remember to add yourself: mcmgr server cmd whitelist add <your-username>")
	if err != nil {
		return promptErr(err)
	}

	// --- online mode -----------------------------------------------------
	onlineMode, err := askBool(v.onlineMode, interactive, true,
		"Verify players with Mojang? (recommended)",
		"Recommended. Turning this off lets anyone connect using any username,\n"+
			"including an operator's, and is only safe on a private network.")
	if err != nil {
		return promptErr(err)
	}
	if !onlineMode {
		for _, w := range warn.ForProperty("online-mode", "false", warn.Context{}) {
			if interactive {
				if perr := prompt.Pause("Offline mode", warn.Summary(w), "I understand"); perr != nil {
					return promptErr(perr)
				}
			} else {
				warn.Print(w)
			}
		}
	}

	// --- EULA -------------------------------------------------------------
	acceptEULA := *v.acceptEULA
	if !acceptEULA && interactive {
		if acceptEULA, err = prompt.Confirm("Do you accept the Minecraft EULA?",
			warn.EULA.Body, false); err != nil {
			return promptErr(err)
		}
	}

	// --- build it ---------------------------------------------------------
	jvm := append([]string{}, v.javaArgs...)
	if memory != "" {
		jvm = append([]string{"-Xmx" + memory}, jvm...)
	}

	cfg, err := config.Init(dir, &config.Config{
		Name:       name,
		Minecraft:  version,
		Loader:     loader,
		Java:       config.Java{Path: *v.javaPath, Args: jvm},
		ServerArgs: []string{"nogui"},
		Mods:       []config.ModSpec{},
	})
	if err != nil {
		return err
	}

	if err := server.Install(cfg, server.InstallOptions{}); err != nil {
		if loader == config.Quilt {
			return err
		}
		return fmt.Errorf("%w\n\n       %s was created but has no server binary yet.\n"+
			"       Once the problem above is resolved, finish it with:\n"+
			"         cd %s && mcmgr server install", err, cfg.Dir(), dir)
	}
	// Record which Java this version needs while the metadata is to hand, so
	// later launches can check without a network call.
	_ = server.RecordJavaRequirement(cfg)
	if err := cfg.Save(); err != nil {
		return err
	}

	// Seed server.properties so the first start uses the chosen settings.
	p, err := props.Load(cfg.Dir())
	if err != nil {
		return err
	}
	if port != "" {
		p.Set("server-port", port)
	}
	p.SetBool("white-list", whitelist)
	p.SetBool("online-mode", onlineMode)
	if err := p.Save(); err != nil {
		return err
	}

	if acceptEULA {
		if err := server.AcceptEULA(cfg.Dir()); err != nil {
			return err
		}
	}
	if !*v.noRegister {
		reg, rerr := config.LoadRegistry()
		if rerr != nil {
			return rerr
		}
		if rerr := reg.Add(name, cfg.Dir()); rerr != nil {
			return rerr
		}
	}

	ui.Ok("created %s in %s", name, cfg.Dir())
	summariseNew(cfg, whitelist, onlineMode, port)

	// Warnings are printed after the summary so they are the last thing read.
	var post []warn.Warning
	if whitelist {
		post = append(post, warn.ForProperty("white-list", "true", warn.Context{})...)
	}
	if !onlineMode {
		post = append(post, warn.ForProperty("online-mode", "false", warn.Context{})...)
	}
	warn.PrintAll(post)

	fmt.Printf("%sNext steps%s\n", ui.Bold, ui.Reset)
	if !acceptEULA {
		fmt.Printf("  mcmgr server accept-eula -s %s   %s# required before the server will start%s\n",
			name, ui.Dim, ui.Reset)
	}
	if loader.SupportsMods() {
		fmt.Printf("  mcmgr mod add %s -s %s\n", exampleAddon(loader), name)
	}
	fmt.Printf("  mcmgr server launch -s %s\n", name)
	return nil
}

// summariseNew prints what was created, so the choices are visible in the
// scrollback after the prompts have been cleared.
func summariseNew(cfg *config.Config, whitelist, onlineMode bool, port string) {
	field("directory", "%s", cfg.Dir())
	if cfg.LoaderVer != "" {
		field("minecraft", "%s (%s %s)", cfg.Minecraft, cfg.Loader, cfg.LoaderVer)
	} else {
		field("minecraft", "%s (%s)", cfg.Minecraft, cfg.Loader)
	}
	if port != "" {
		field("port", "%s", port)
	}
	field("whitelist", "%s", onOff(whitelist))
	field("online-mode", "%s", onOff(onlineMode))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// askBool resolves a tri-state flag: an explicit --flag=value wins, otherwise
// the user is asked, otherwise the default applies.
func askBool(flagValue *string, interactive, def bool, title, description string) (bool, error) {
	if flagValue != nil && *flagValue != "" {
		b, err := strconv.ParseBool(*flagValue)
		if err != nil {
			return false, fmt.Errorf("expected true or false, got %q", *flagValue)
		}
		return b, nil
	}
	if !interactive {
		return def, nil
	}
	return prompt.Confirm(title, description, def)
}

// loaderOptions labels each loader with what it is for.
func loaderOptions() []prompt.Option {
	labels := map[config.Loader]string{
		config.Vanilla:  "vanilla    unmodified Minecraft, no mods",
		config.Fabric:   "fabric     lightweight mod loader, the most common choice",
		config.Paper:    "paper      high-performance server, loads Bukkit plugins",
		config.NeoForge: "neoforge   mod loader for the Forge ecosystem",
		config.Quilt:    "quilt      Fabric fork (mcmgr cannot install it for you)",
	}
	order := []config.Loader{config.Fabric, config.Paper, config.NeoForge, config.Vanilla, config.Quilt}
	out := make([]prompt.Option, 0, len(order))
	for _, l := range order {
		out = append(out, prompt.Option{Label: labels[l], Value: string(l)})
	}
	return out
}

// suggestName proposes a name based on the working directory.
func suggestName() string {
	wd, err := os.Getwd()
	if err != nil {
		return "server"
	}
	base := filepath.Base(wd)
	if prompt.ValidateName(base) != nil || base == "." {
		return "server"
	}
	return strings.ToLower(base)
}

// promptErr turns a cancelled prompt into a clean message rather than a stack
// of wrapped errors.
func promptErr(err error) error {
	if errors.Is(err, prompt.ErrAborted) {
		return fmt.Errorf("cancelled; nothing was changed")
	}
	if errors.Is(err, prompt.ErrNotInteractive) {
		return fmt.Errorf("%w\n       pass the value as a flag, or run this in a terminal", err)
	}
	return err
}

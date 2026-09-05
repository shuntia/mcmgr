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
	"mcmgr/internal/java"
	"mcmgr/internal/mods"
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
	// zgc is tri-state like whitelist: "" asks, "true"/"false" decides.
	zgc *string
	// javaDist downloads a runtime for the server rather than relying on the
	// Java already on the machine: "" asks, or leaves it to the first launch.
	javaDist *string
	graalvm  *bool
	// recommended installs the pre-selected recommended add-ons; addons are
	// extra ones named on the command line. Either given skips the prompt.
	recommended *bool
	addons      stringList
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
	v.whitelist = fs.String("whitelist", "", "enable the whitelist: true or false (default true)")
	v.onlineMode = fs.String("online-mode", "", "verify players against Mojang: true or false")
	v.acceptEULA = fs.Bool("accept-eula", false, "accept the Minecraft EULA (https://aka.ms/MinecraftEULA)")
	v.noRegister = fs.Bool("no-register", false, "do not add the server to the global registry")
	v.noInput = fs.Bool("no-input", false, "never prompt; fail if a required value is missing")
	fs.Var(&v.javaArgs, "java-arg", "extra JVM argument; repeatable")
	v.zgc = fs.String("zgc", "", "run on the ZGC garbage collector: true or false (default true)")
	v.javaDist = fs.String("java-dist", "", "download a Java for this server: temurin or graalvm (default: ask, or java from PATH)")
	v.graalvm = fs.Bool("graalvm", false, "download Oracle GraalVM for this server; the same as --java-dist graalvm")
	v.recommended = fs.Bool("recommended", false,
		"install the recommended server-side mods or plugins without asking")
	fs.Var(&v.addons, "mod", "mod or plugin to install, as for 'mcmgr mod add'; repeatable")
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
			"applied. The whitelist is on unless --whitelist=false is given, and the JVM\n" +
			"runs on ZGC unless --zgc=false is given or --java-arg names a collector.\n\n" +
			"The server can run on the Java already installed, or on one downloaded for it:\n" +
			"Eclipse Temurin, or Oracle GraalVM (--graalvm), whose Graal JIT compiler runs\n" +
			"some servers measurably faster. Nothing is installed system-wide either way.\n\n" +
			"On a platform that loads mods or plugins, a curated list of server-side ones\n" +
			"is offered, and Modrinth can be searched for more; --recommended and --mod\n" +
			"answer that without a prompt.",
		Examples: []string{
			"mcmgr new                                   # ask for everything",
			"mcmgr new survival --loader fabric          # ask only for the rest",
			"mcmgr new lobby --loader paper --mc 1.21.4 --memory 4G --accept-eula --no-input",
			"mcmgr new smp --loader fabric --recommended --mod chunky --no-input",
			"mcmgr new smp --loader paper --graalvm     # download Oracle GraalVM for it",
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

	// --- Java ---------------------------------------------------------------
	// Which Java the server runs on: the one already here, or a download of
	// Temurin or GraalVM. Asked now, while every other question is, rather
	// than at the first launch when the Java on PATH turns out too old.
	javaPick, err := chooseJava(&v, interactive, version)
	if err != nil {
		return err
	}

	// --- garbage collector -----------------------------------------------
	// ZGC is recommended (see java.ZGCArgs for why). The offer depends on the
	// Java that would run: ZGC is unknown to Java 8 and experimental before 15,
	// and the flags differ by release, so that Java is detected first. A
	// collector named in --java-arg is the user's decision and skips the
	// question; --zgc=false skips it too.
	gc := config.GCDefault
	if !java.SelectsGC(v.javaArgs) {
		useZGC, cerr := chooseZGC(v.zgc, interactive, *v.javaPath, javaPick.downloadMajor())
		if cerr != nil {
			return cerr
		}
		if useZGC {
			gc = config.GCZGC
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
	// On by default: a fresh server is private until its owner decides
	// otherwise, rather than open to anyone who finds the address.
	whitelist, err := askBool(v.whitelist, interactive, true,
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

	// --- mods and plugins --------------------------------------------------
	// Chosen before anything is built so every question is asked up front,
	// and installed after, once there is a manifest to record them in.
	var picks addonPicks
	if loader.SupportsMods() {
		if *v.recommended {
			for _, slug := range recommendedSlugs(mods.RecommendedDefaults(loader)) {
				picks.add(slug)
			}
		}
		for _, slug := range v.addons {
			picks.add(slug)
		}
		if !*v.recommended && len(v.addons) == 0 && interactive {
			chosen, perr := pickRecommended(loader, nil)
			if perr != nil {
				return promptErr(perr)
			}
			for _, slug := range chosen {
				picks.add(slug)
			}
			filter := mods.FilterFor(&config.Config{Minecraft: version, Loader: loader}, mods.Channels{})
			if perr := searchAndPick(filter, loader, &picks); perr != nil {
				return promptErr(perr)
			}
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
		Java:       config.Java{Path: *v.javaPath, Args: jvm, GC: gc},
		ServerArgs: []string{"nogui"},
		Mods:       []config.ModSpec{},
	})
	if err != nil {
		return err
	}

	// Java comes before the server jar: the NeoForge installer needs a JVM
	// to run, and the manifest is already there to retry from.
	if javaPick.dist != "" {
		rt := java.Runtime{Dist: javaPick.dist, Major: javaPick.major}
		bin, jerr := java.Install(rt)
		if jerr != nil {
			return fmt.Errorf("%w\n\n       %s was created but has no Java or server binary yet.\n"+
				"       Once the problem above is resolved, finish it with:\n"+
				"         cd %s && mcmgr venv install --dist %s && mcmgr server install",
				jerr, cfg.Dir(), dir, rt.Dist)
		}
		cfg.Java.Path = bin
	}
	if javaPick.major > 0 {
		cfg.Java.RequiredMajor = javaPick.major
	}
	if err := cfg.Save(); err != nil {
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

	// Add-ons are installed last, so a failure here leaves a usable server
	// that is only missing a mod, and the summary can say which.
	failed, ierr := installAddons(cfg, picks.slugs, mods.ChannelsFrom(cfg.Defaults), false, interactive)
	if ierr != nil {
		return ierr
	}

	ui.Ok("created %s in %s", name, cfg.Dir())
	summariseNew(cfg, whitelist, onlineMode, port)
	if javaPick.dist != "" {
		field("java", "%s %d, downloaded", javaPick.dist.Label(), javaPick.major)
	}
	if gc == config.GCZGC {
		field("gc", "ZGC")
	}
	if len(picks.slugs) > 0 {
		field(loader.ContentNoun()+"s", "%d installed", len(picks.slugs)-len(failed))
	}
	if len(failed) > 0 {
		ui.Warn("%d %s(s) could not be installed: %s\n"+
			"       retry later with:  mcmgr mod add %s -s %s",
			len(failed), loader.ContentNoun(), strings.Join(failed, ", "),
			strings.Join(failed, " "), name)
	}

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
	if loader.SupportsMods() && len(picks.slugs) == 0 {
		fmt.Printf("  mcmgr mod recommend -s %s   %s# pick from a curated list, or: mcmgr mod add %s%s\n",
			name, ui.Dim, exampleAddon(loader), ui.Reset)
	}
	fmt.Printf("  mcmgr server launch -s %s\n", name)
	if whitelist {
		fmt.Printf("  mcmgr server cmd whitelist add <your-username> -s %s   %s# once it is running%s\n",
			name, ui.Dim, ui.Reset)
	}
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

// javaChoice is the outcome of the Java step of `mcmgr new`: a runtime to
// download, or none, in which case the server uses java from PATH (or the
// global setting) and the first launch offers a download if that is too old.
// major is the release Minecraft needs, when it was looked up.
type javaChoice struct {
	dist  java.Distribution
	major int
}

// downloadMajor is the release about to be downloaded, or 0 when the server
// will run on whatever Java is already here.
func (c javaChoice) downloadMajor() int {
	if c.dist == "" {
		return 0
	}
	return c.major
}

// chooseJava decides which Java a new server runs on. --java names a binary
// and settles it; --java-dist or --graalvm orders a download without asking;
// otherwise, in a terminal, the choice is offered alongside whatever Java is
// already installed, and in a script it is left to the first launch as
// before.
func chooseJava(v *newFlagValues, interactive bool, version string) (javaChoice, error) {
	var choice javaChoice
	want := *v.javaDist
	if *v.graalvm {
		if want != "" && !strings.EqualFold(want, string(java.GraalVM)) {
			return choice, fmt.Errorf("--graalvm and --java-dist %s disagree", want)
		}
		want = string(java.GraalVM)
	}
	if *v.javaPath != "" {
		if want != "" {
			return choice, fmt.Errorf("--java names the Java to use, so there is nothing to download; drop --java-dist")
		}
		return choice, nil
	}
	if want == "" && !interactive {
		return choice, nil
	}

	ui.Step("asking Mojang which Java Minecraft %s needs", version)
	major, err := java.Required(version)
	if err != nil {
		if want != "" {
			return choice, fmt.Errorf("%w\n       download one by release instead, e.g. 'mcmgr venv install 21 --dist %s'", err, want)
		}
		ui.Warn("%v; the first launch will check Java instead", err)
		return choice, nil
	}
	choice.major = major

	if want != "" {
		dist, err := java.ParseDistribution(want)
		if err != nil {
			return choice, err
		}
		if !dist.Supports(major) {
			return choice, fmt.Errorf("%s is only published for Java 17 and newer, but Minecraft %s needs Java %d\n"+
				"       drop --java-dist to use Temurin", dist.Label(), version, major)
		}
		choice.dist = dist
		return choice, nil
	}

	// What is already here, and is it enough?
	current, derr := java.Detect((&config.Config{}).JavaBin())
	usable := derr == nil && current.Major >= major
	preferred := preferredDist()

	var options []prompt.Option
	def := ""
	if usable {
		options = append(options, prompt.Option{
			Label: fmt.Sprintf("%-22s %s", fmt.Sprintf("Java %d, already here", current.Major), current.Path),
			Value: "existing",
		})
		def = "existing"
	}
	for _, d := range java.Distributions {
		if !d.Supports(major) {
			continue
		}
		note := "download"
		if _, ok := java.Find(java.Runtime{Dist: d, Major: major}); ok {
			note = "already downloaded"
		}
		switch d {
		case java.Temurin:
			note += "; the usual choice"
		case java.GraalVM:
			note += "; Graal JIT compiler, faster for some servers"
		}
		options = append(options, prompt.Option{
			Label: fmt.Sprintf("%-22s %s", fmt.Sprintf("%s %d", d.Label(), major), note),
			Value: string(d),
		})
		if def == "" && d == preferred {
			def = string(d)
		}
	}
	if def == "" {
		def = string(java.Temurin)
	}

	desc := fmt.Sprintf("Minecraft %s needs Java %d.", version, major)
	if derr == nil && !usable {
		desc += fmt.Sprintf(" The Java on this machine is %d, which is too old.", current.Major)
	} else if derr != nil {
		desc += " No Java was found on this machine."
	}
	desc += "\n\nA downloaded runtime is unpacked into mcmgr's cache and shared between\n" +
		"servers; nothing is installed system-wide. Oracle GraalVM is free to use\n" +
		"under the GraalVM Free Terms and Conditions."
	pick, err := prompt.Select("Java runtime", desc, options, def)
	if err != nil {
		return choice, promptErr(err)
	}
	if pick == "existing" {
		return choice, nil
	}
	choice.dist, err = java.ParseDistribution(pick)
	return choice, err
}

// chooseZGC decides whether a new server runs on ZGC. Only the choice is
// recorded; the flags are built at launch (server.JVMArgs) for whichever Java
// runs then. javaPath is the --java flag; empty means the global setting or
// java from PATH, as at launch. downloadMajor is the release about to be
// downloaded for the server, when one is, which settles the question without
// looking at the machine.
//
// When no Java can be found yet (it may be installed by `mcmgr venv` later),
// the question is still asked. When the Java found is too old for ZGC, the
// question is skipped, and an explicit --zgc=true is an error rather than a
// choice that would silently do nothing.
func chooseZGC(flagValue *string, interactive bool, javaPath string, downloadMajor int) (bool, error) {
	var info java.Info
	var derr error
	if downloadMajor > 0 {
		info = java.Info{Major: downloadMajor, Path: fmt.Sprintf("the Java %d being downloaded", downloadMajor)}
	} else {
		bin := (&config.Config{Java: config.Java{Path: javaPath}}).JavaBin()
		info, derr = java.Detect(bin)
	}
	if derr == nil && !java.SupportsZGC(info.Major) {
		if flagValue != nil && *flagValue != "" {
			if b, perr := strconv.ParseBool(*flagValue); perr == nil && b {
				return false, fmt.Errorf("--zgc: %s is Java %d, but ZGC needs Java 15 or newer",
					info.Path, info.Major)
			}
		}
		return false, nil
	}
	use, err := askBool(flagValue, interactive, true,
		"Run on ZGC? (recommended)",
		"Recommended. Minecraft is latency-bound: a tick has 50 ms, and a pause from\n"+
			"the JVM's default collector on a multi-gigabyte heap is felt as lag by every\n"+
			"player at once. ZGC keeps pauses under a millisecond at any heap size, for a\n"+
			"little throughput a server does not miss. Java 15 or newer.")
	if err != nil {
		return false, promptErr(err)
	}
	return use, nil
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
		return errCancelled
	}
	if errors.Is(err, prompt.ErrNotInteractive) {
		return fmt.Errorf("%w\n       pass the value as a flag, or run this in a terminal", err)
	}
	return err
}

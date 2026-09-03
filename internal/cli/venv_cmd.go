package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"mcmgr/internal/config"
	"mcmgr/internal/java"
	"mcmgr/internal/prompt"
	"mcmgr/internal/server"
	"mcmgr/internal/ui"
)

func venvCmd() *Command {
	cmd := &Command{
		Name:    "venv",
		Aliases: []string{"java"},
		Short:   "Manage the Java runtime a server uses",
		Long: "Install and select a standalone Java runtime, so a server does not depend on\n" +
			"whatever Java happens to be on the system PATH.\n\n" +
			"Minecraft raises its Java requirement every few releases, and an older Java\n" +
			"fails with an UnsupportedClassVersionError that mentions neither Minecraft nor\n" +
			"a version anyone recognises. 'mcmgr venv install' fetches the release Mojang\n" +
			"says this server needs.\n\n" +
			"Runtimes are downloaded once and shared between servers, since a JRE is the\n" +
			"same hundred megabytes for every server that wants it. What --global changes\n" +
			"is the scope of the setting: without it the runtime is recorded for this\n" +
			"server, with it as the machine-wide default for servers that name none.",
		Sub: []*Command{
			venvInstallCmd(),
			venvUseCmd(),
			venvListCmd(),
			venvRemoveCmd(),
		},
		Examples: []string{
			"mcmgr venv                    # what Java does this server use?",
			"mcmgr venv install            # fetch the version this server needs",
			"mcmgr venv install 21 -g      # make Java 21 the machine-wide default",
			"mcmgr venv use /opt/jdk21/bin/java",
		},
	}
	// Bare "mcmgr venv" reports rather than printing help.
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr venv")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		return venvStatus(g)
	}
	return cmd
}

// venvStatus reports which Java is in use and whether it is new enough.
func venvStatus(g *globals) error {
	cfg, err := g.config()
	if err != nil {
		// With no server selected, at least report the machine-wide default.
		if !errors.Is(err, config.ErrNoServer) {
			return err
		}
		glob := config.CachedGlobal()
		heading("Java")
		if glob.JavaPath == "" {
			field("global", "%snot set; servers use java from PATH%s", ui.Dim, ui.Reset)
		} else {
			field("global", "%s", glob.JavaPath)
		}
		printInstalledRuntimes()
		return nil
	}

	status := server.CheckJava(cfg)

	heading("Java for %s", cfg.Name)
	field("minecraft", "%s", cfg.Minecraft)
	if status.Required > 0 {
		field("requires", "Java %d", status.Required)
	} else {
		field("requires", "%sunknown; run 'mcmgr server install' to record it%s", ui.Dim, ui.Reset)
	}

	switch {
	case status.Missing:
		field("using", "%snone found%s", ui.Red, ui.Reset)
	case status.Err != nil:
		field("using", "%s%v%s", ui.Yellow, status.Err, ui.Reset)
	default:
		mark := ui.Green + "ok" + ui.Reset
		if status.TooOld() {
			mark = ui.Red + "too old" + ui.Reset
		}
		field("using", "Java %d  %s  %s", status.Info.Major, status.Info.String(), mark)
		field("path", "%s", status.Info.Path)
	}

	source := "java from PATH"
	switch {
	case cfg.Java.Path != "":
		source = config.ManifestName + " (java.path)"
	case config.CachedGlobal().JavaPath != "":
		source = "global java_path"
	}
	field("source", "%s", source)

	printInstalledRuntimes()

	if !status.OK() {
		fmt.Println()
		ui.Warn("%s", status.Explain(cfg))
	}
	return nil
}

func printInstalledRuntimes() {
	installed := java.Installed()
	if len(installed) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("%sDownloaded runtimes%s\n", ui.Bold, ui.Reset)
	for _, r := range installed {
		fmt.Printf("  Java %-3d %s%s%s\n", r.Major, ui.Dim, r.Path, ui.Reset)
	}
}

func venvInstallCmd() *Command {
	cmd := &Command{
		Name:  "install",
		Usage: "[java-version]",
		Short: "Download a Java runtime and use it",
		Long: "Download an Eclipse Temurin runtime and point this server at it.\n\n" +
			"With no version, the one Mojang says this server's Minecraft version needs is\n" +
			"used. Runtimes are shared between servers rather than copied per server.",
		Examples: []string{
			"mcmgr venv install",
			"mcmgr venv install 25",
			"mcmgr venv install 21 --global",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr venv install")
		var (
			global  = fs.Bool("global", false, "set as the machine-wide default instead of for this server")
			globalS = fs.Bool("g", false, "shorthand for --global")
		)
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		toGlobal := *global || *globalS

		var cfg *config.Config
		if !toGlobal || len(rest) == 0 {
			cfg, err = g.config()
			if err != nil {
				if !toGlobal || !errors.Is(err, config.ErrNoServer) {
					return err
				}
			}
		}

		major, err := resolveJavaMajor(rest, cfg)
		if err != nil {
			return err
		}

		bin, err := java.Install(major)
		if err != nil {
			return err
		}
		return useJava(cfg, bin, major, toGlobal)
	}
	return cmd
}

// resolveJavaMajor works out which Java release to install: an explicit
// argument, otherwise whatever this server's Minecraft version needs.
func resolveJavaMajor(args []string, cfg *config.Config) (int, error) {
	if len(args) > 0 {
		major, err := strconv.Atoi(args[0])
		if err != nil || major < 8 {
			return 0, fmt.Errorf("expected a Java feature release such as 21 or 25, got %q", args[0])
		}
		return major, nil
	}
	if cfg == nil {
		return 0, fmt.Errorf("no server selected, so there is nothing to infer a Java version from\n" +
			"       name one explicitly, e.g. 'mcmgr venv install 21'")
	}
	if cfg.Java.RequiredMajor > 0 {
		return cfg.Java.RequiredMajor, nil
	}
	ui.Step("asking Mojang which Java Minecraft %s needs", cfg.Minecraft)
	major, err := java.Required(cfg.Minecraft)
	if err != nil {
		return 0, fmt.Errorf("%w\n       name a version explicitly, e.g. 'mcmgr venv install 21'", err)
	}
	cfg.Java.RequiredMajor = major
	return major, nil
}

// useJava records a runtime, either for one server or machine-wide.
func useJava(cfg *config.Config, bin string, major int, toGlobal bool) error {
	if toGlobal {
		glob, err := config.LoadGlobal()
		if err != nil {
			return err
		}
		glob.JavaPath = bin
		if err := glob.Save(); err != nil {
			return err
		}
		ui.Ok("Java %d is now the default for servers that do not set java.path", major)
		ui.Info("       %s", bin)
		if cfg != nil && cfg.Java.Path != "" {
			ui.Warn("%s still sets java.path, so it keeps using %s", cfg.Name, cfg.Java.Path)
		}
		return nil
	}

	if cfg == nil {
		return fmt.Errorf("no server selected; use --global, or run this in a server directory")
	}
	cfg.Java.Path = bin
	if err := cfg.Save(); err != nil {
		return err
	}
	ui.Ok("%s will use Java %d", cfg.Name, major)
	ui.Info("       %s", bin)
	return nil
}

func venvUseCmd() *Command {
	cmd := &Command{
		Name:  "use",
		Usage: "<path-to-java>",
		Short: "Use a Java installation you already have",
		Long: "Point a server at an existing Java binary, rather than downloading one.\n" +
			"The version is checked before it is recorded.",
		Examples: []string{
			`mcmgr venv use "C:\Program Files\Java\jdk-25\bin\java.exe"`,
			"mcmgr venv use /usr/lib/jvm/temurin-21/bin/java --global",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr venv use")
		var (
			global  = fs.Bool("global", false, "set as the machine-wide default instead of for this server")
			globalS = fs.Bool("g", false, "shorthand for --global")
		)
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr venv use", "<path-to-java>"); err != nil {
			return err
		}
		toGlobal := *global || *globalS

		info, err := java.Detect(rest[0])
		if err != nil {
			return err
		}

		var cfg *config.Config
		if cfg, err = g.config(); err != nil {
			if !toGlobal || !errors.Is(err, config.ErrNoServer) {
				return err
			}
			cfg = nil
		}
		if cfg != nil && cfg.Java.RequiredMajor > 0 && info.Major < cfg.Java.RequiredMajor {
			ui.Warn("Minecraft %s needs Java %d, and this is Java %d; the server will not start",
				cfg.Minecraft, cfg.Java.RequiredMajor, info.Major)
		}
		return useJava(cfg, info.Path, info.Major, toGlobal)
	}
	return cmd
}

func venvListCmd() *Command {
	cmd := &Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Short:   "List downloaded Java runtimes",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr venv list")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		installed := java.Installed()
		if len(installed) == 0 {
			store, _ := java.StoreDir()
			ui.Info("no runtimes downloaded yet (they would live in %s)", store)
			ui.Info("       install one with 'mcmgr venv install'")
			return nil
		}
		rows := [][]string{{"JAVA", "VERSION", "PATH"}}
		for _, r := range installed {
			rows = append(rows, []string{strconv.Itoa(r.Major), r.String(), r.Path})
		}
		fmt.Print(ui.Table(rows))
		return nil
	}
	return cmd
}

func venvRemoveCmd() *Command {
	cmd := &Command{
		Name:    "remove",
		Aliases: []string{"rm"},
		Usage:   "<java-version>",
		Short:   "Delete a downloaded Java runtime",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr venv remove")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr venv remove", "<java-version>"); err != nil {
			return err
		}
		major, err := strconv.Atoi(rest[0])
		if err != nil {
			return fmt.Errorf("expected a Java feature release such as 21, got %q", rest[0])
		}

		// Refuse to delete a runtime something still points at.
		bin, _ := java.Find(major)
		if bin != "" {
			if glob := config.CachedGlobal(); glob.JavaPath == bin {
				return fmt.Errorf("Java %d is the machine-wide default\n"+
					"       change it first with 'mcmgr venv use <path> --global'", major)
			}
			if cfg, cerr := g.config(); cerr == nil && cfg.Java.Path == bin {
				return fmt.Errorf("%s uses Java %d\n"+
					"       change it first with 'mcmgr venv install <other>'", cfg.Name, major)
			}
		}

		if err := java.Remove(major); err != nil {
			return err
		}
		ui.Ok("removed Java %d", major)
		return nil
	}
	return cmd
}

// ensureJava checks a server's Java before it is started, and offers to fix a
// version mismatch rather than letting the JVM fail with a stack trace.
//
// A missing or stale requirement is looked up once and recorded, so this is a
// local check on every later launch.
func ensureJava(cfg *config.Config, allowPrompt bool) error {
	if cfg.Java.RequiredMajor == 0 && cfg.Minecraft != "" {
		if err := server.RecordJavaRequirement(cfg); err == nil {
			_ = cfg.Save()
		}
	}

	status := server.CheckJava(cfg)
	if status.OK() {
		return nil
	}

	// Already downloaded, just not selected: use it without asking.
	if status.TooOld() || status.Missing {
		if bin, ok := java.Find(status.Required); ok && status.Required > 0 {
			cfg.Java.Path = bin
			if err := cfg.Save(); err == nil {
				ui.Info("switching %s to the Java %d runtime already downloaded", cfg.Name, status.Required)
				return nil
			}
		}
	}

	problem := status.Explain(cfg)
	if !allowPrompt || !prompt.Interactive() || status.Required <= 0 {
		return fmt.Errorf("%s", problem)
	}

	ui.Warn("%s", problem)
	ok, err := prompt.Confirm(
		fmt.Sprintf("Download Java %d now?", status.Required),
		fmt.Sprintf("Minecraft %s needs Java %d.\n\n"+
			"mcmgr can fetch an Eclipse Temurin runtime and use it for this server.\n"+
			"Nothing is installed system-wide: it is unpacked into mcmgr's cache and\n"+
			"used only by servers you point at it.", cfg.Minecraft, status.Required),
		true)
	if err != nil {
		return promptErr(err)
	}
	if !ok {
		return fmt.Errorf("cannot start without Java %d", status.Required)
	}

	bin, err := java.Install(status.Required)
	if err != nil {
		return err
	}
	cfg.Java.Path = bin
	if err := cfg.Save(); err != nil {
		return err
	}
	ui.Ok("%s will use Java %d", cfg.Name, status.Required)
	return nil
}

// reportFailure prints an explanation for a server that would not start, when
// mcmgr recognises the reason.
func reportFailure(dir string) {
	if hint := server.DiagnoseFailure(dir); hint != "" {
		fmt.Fprintln(os.Stderr)
		ui.Warn("%s", hint)
	}
}

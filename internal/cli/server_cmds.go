package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/prompt"
	"mcmgr/internal/server"
	"mcmgr/internal/ui"
)

func serverCmd() *Command {
	return &Command{
		Name:    "server",
		Aliases: []string{"srv"},
		Short:   "Create, run and control Minecraft servers",
		Long:    "Create, run and control Minecraft servers.",
		Sub: []*Command{
			grouped("Run and control", serverRunCmd()),
			grouped("Run and control", serverLaunchCmd()),
			grouped("Run and control", serverAttachCmd()),
			grouped("Run and control", serverStopCmd()),
			grouped("Run and control", serverRestartCmd()),
			grouped("Run and control", serverKillCmd()),

			grouped("Inspect", serverStatusCmd()),
			grouped("Inspect", serverLogsCmd()),
			grouped("Inspect", serverCmdCmd()),

			grouped("Set up", serverCreateCmd()),
			grouped("Set up", serverInstallCmd()),
			grouped("Set up", serverAcceptEULACmd()),
			grouped("Set up", serverSetJarCmd()),

			grouped("Registry", serverListCmd()),
			grouped("Registry", serverUseCmd()),
			grouped("Registry", serverForgetCmd()),

			serverSuperviseCmd(),
		},
		Examples: []string{
			"mcmgr server run             # console here; Ctrl-C stops, Ctrl-D detaches",
			"mcmgr server launch          # start in the background",
			"mcmgr server attach          # reopen the console of a background server",
			"mcmgr server stop",
			"mcmgr server logs -f",
			"mcmgr server list",
		},
	}
}

// --- install -----------------------------------------------------------

// exampleAddon names an add-on that actually exists for a loader, so the
// suggestion printed after creating a server is one the user can run verbatim.
func exampleAddon(l config.Loader) string {
	switch l {
	case config.Paper:
		return "worldedit"
	case config.NeoForge:
		return "jei"
	default:
		return "fabric-api"
	}
}

func serverInstallCmd() *Command {
	cmd := &Command{
		Name:  "install",
		Short: "Download or repair the server jar",
		Long: "Download the server binary for the configured Minecraft version and loader.\n" +
			"Safe to re-run: it verifies what is already on disk and only fetches what is\n" +
			"missing or wrong.",
		Examples: []string{
			"mcmgr server install",
			"mcmgr server install --loader-version 0.16.9",
			"mcmgr server install --force",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server install")
		var (
			loaderVer = fs.String("loader-version", "", "pin the loader build (Paper: build number)")
			force     = fs.Bool("force", false, "re-download even when the jar is present")
			mc        = fs.String("mc", "", "change the Minecraft version before installing")
		)
		if _, err := parse(fs, g, args); err != nil {
			return err
		}

		cfg, err := g.config()
		if err != nil {
			return err
		}
		if err := server.EnsureStopped(cfg.Dir(), cfg.Name); err != nil {
			return err
		}
		if *mc != "" {
			cfg.Minecraft = *mc
		}
		if err := server.Install(cfg, server.InstallOptions{LoaderVersion: *loaderVer, Force: *force}); err != nil {
			return err
		}
		// Record which Java this version needs, so launches can check locally.
		if err := server.RecordJavaRequirement(cfg); err != nil {
			ui.Warn("could not determine which Java %s needs: %v", cfg.Minecraft, err)
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		ui.Ok("%s is ready (%s %s)", cfg.Name, cfg.Loader, cfg.Minecraft)
		return nil
	}
	return cmd
}

func serverSetJarCmd() *Command {
	cmd := &Command{
		Name:  "set-jar",
		Usage: "<path>",
		Short: "Point the server at a jar you installed yourself",
		Long: "Record a server jar that mcmgr did not download, for loaders it cannot\n" +
			"install automatically. The path is stored relative to the server directory.",
		Examples: []string{"mcmgr server set-jar quilt-server-launch.jar"},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server set-jar")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr server set-jar", "<path>"); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}

		jar := rest[0]
		if filepath.IsAbs(jar) {
			rel, err := filepath.Rel(cfg.Dir(), jar)
			if err != nil {
				return fmt.Errorf("%s is not inside the server directory %s", jar, cfg.Dir())
			}
			jar = rel
		}
		if _, err := os.Stat(filepath.Join(cfg.Dir(), jar)); err != nil {
			return fmt.Errorf("no such file: %s", filepath.Join(cfg.Dir(), jar))
		}

		cfg.Jar = filepath.ToSlash(jar)
		cfg.LaunchArgs = nil
		if err := cfg.Save(); err != nil {
			return err
		}
		ui.Ok("%s will launch %s", cfg.Name, cfg.Jar)
		return nil
	}
	return cmd
}

func serverAcceptEULACmd() *Command {
	cmd := &Command{
		Name:  "accept-eula",
		Short: "Record acceptance of the Minecraft EULA",
		Long: "Write eula.txt marking the Minecraft EULA as accepted.\n\n" +
			"Only run this if you have read and agree to https://aka.ms/MinecraftEULA.\n" +
			"A Minecraft server refuses to start until this is done.",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server accept-eula")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}
		if err := server.AcceptEULA(cfg.Dir()); err != nil {
			return err
		}
		ui.Ok("recorded EULA acceptance for %s", cfg.Name)
		return nil
	}
	return cmd
}

// --- registry ----------------------------------------------------------

func serverListCmd() *Command {
	cmd := &Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Short:   "List registered servers and whether they are running",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server list")
		prune := fs.Bool("prune", false, "forget servers whose directories are gone")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}

		reg, err := config.LoadRegistry()
		if err != nil {
			return err
		}
		if *prune {
			gone, err := reg.Prune()
			if err != nil {
				return err
			}
			for _, n := range gone {
				ui.Ok("forgot %s (directory no longer holds a server)", n)
			}
		}
		names := reg.Names()
		if len(names) == 0 {
			ui.Info("no servers registered yet; create one with 'mcmgr new', or adopt an existing one with 'mcmgr init'")
			return nil
		}

		rows := [][]string{{"", "NAME", "STATE", "MINECRAFT", "LOADER", "DIRECTORY"}}
		for _, name := range names {
			dir := reg.Servers[name]
			marker := " "
			if name == reg.Default {
				marker = "*"
			}

			state, mc, loader := "stopped", "?", "?"
			if cfg, err := config.Load(dir); err != nil {
				state = "unreadable"
			} else {
				mc, loader = cfg.Minecraft, string(cfg.Loader)
				if live, _ := server.Probe(dir); live != nil {
					state = "running"
					if !live.Responsive {
						state = "unresponsive"
					} else if live.Status != nil && !live.Status.Ready {
						state = "starting"
					}
				}
			}
			rows = append(rows, []string{marker, name, colorState(state), mc, loader, dir})
		}
		fmt.Print(ui.Table(rows))
		if reg.Default != "" {
			fmt.Printf("\n%s* = default server%s\n", ui.Dim, ui.Reset)
		}
		return nil
	}
	return cmd
}

// colorState tints a state word without changing its width in the table, which
// is computed before colouring.
func colorState(state string) string {
	switch state {
	case "running":
		return ui.Green + state + ui.Reset
	case "starting":
		return ui.Cyan + state + ui.Reset
	case "unresponsive", "unreadable":
		return ui.Yellow + state + ui.Reset
	}
	return state
}

func serverUseCmd() *Command {
	cmd := &Command{
		Name:  "use",
		Usage: "<name>",
		Short: "Set the default server for commands run outside a server directory",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server use")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr server use", "<name>"); err != nil {
			return err
		}
		reg, err := config.LoadRegistry()
		if err != nil {
			return err
		}
		if err := reg.SetDefault(rest[0]); err != nil {
			return err
		}
		ui.Ok("default server is now %s", rest[0])
		return nil
	}
	return cmd
}

func serverForgetCmd() *Command {
	cmd := &Command{
		Name:  "forget",
		Usage: "<name>",
		Short: "Remove a server from the registry without deleting its files",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server forget")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr server forget", "<name>"); err != nil {
			return err
		}
		reg, err := config.LoadRegistry()
		if err != nil {
			return err
		}
		dir := reg.Servers[rest[0]]
		if err := reg.Remove(rest[0]); err != nil {
			return err
		}
		ui.Ok("forgot %s; its files remain in %s", rest[0], dir)
		return nil
	}
	return cmd
}

// --- lifecycle ---------------------------------------------------------

func serverRunCmd() *Command {
	cmd := &Command{
		Name:  "run",
		Short: "Run the server with its console in this terminal",
		Long: "Start the server and keep its console in this terminal, like running the jar\n" +
			"by hand. Typed lines go to the server console.\n\n" +
			"  Ctrl-C   shuts the server down cleanly; a second Ctrl-C terminates it\n" +
			"  Ctrl-D   detaches and leaves the server running in the background\n\n" +
			"The server itself runs in a detached supervisor, so closing the terminal does\n" +
			"not kill it either; 'server attach' reopens the console and 'server stop'\n" +
			"shuts it down. Pass --foreground to keep the supervisor in this process\n" +
			"instead, which is what a service manager such as systemd expects. That is\n" +
			"also the default when stdin is not a terminal.",
		Examples: []string{
			"mcmgr server run",
			"mcmgr server run --java-arg -XX:+UseZGC",
			"mcmgr server run --foreground     # for systemd, docker and the like",
		},
	}
	var (
		javaArgs   stringList
		foreground *bool
	)
	cmd.Flags = func(fs *flag.FlagSet) {
		javaArgs = nil
		fs.Var(&javaArgs, "java-arg", "extra JVM argument for this run; repeatable")
		foreground = fs.Bool("foreground", false, "keep the supervisor in this process; Ctrl-D cannot detach")
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server run")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}

		if err := ensureJava(cfg, true); err != nil {
			return err
		}

		if *foreground || !prompt.Interactive() {
			code, err := server.Supervise(cfg, server.Options{Foreground: true, ExtraJavaArgs: javaArgs})
			if err != nil {
				return err
			}
			if code != 0 {
				reportFailure(cfg.Dir())
				return fmt.Errorf("server exited with code %d", code)
			}
			return nil
		}

		ui.Step("starting %s (%s %s)", cfg.Name, cfg.Loader, cfg.Minecraft)
		if _, err := server.Launch(cfg, server.Options{ExtraJavaArgs: javaArgs}); err != nil {
			reportFailure(cfg.Dir())
			return err
		}
		live, err := server.Probe(cfg.Dir())
		if err != nil || live == nil {
			return fmt.Errorf("server started but is no longer reachable")
		}
		code, err := server.Attach(live, server.AttachOptions{
			Tail:           ringCapacityHint, // the server just started, so show everything so far
			InterruptStops: true,
			StopTimeout:    cfg.StopTimeoutSecs(),
		})
		if errors.Is(err, server.ErrDetached) {
			return nil
		}
		if err != nil {
			return err
		}
		if code != 0 {
			reportFailure(cfg.Dir())
			return fmt.Errorf("server exited with code %d", code)
		}
		return nil
	}
	return cmd
}

func serverLaunchCmd() *Command {
	cmd := &Command{
		Name:    "launch",
		Aliases: []string{"start"},
		Short:   "Start the server in the background",
		Long: "Start the server in a detached supervisor process that outlives this shell.\n" +
			"Use 'server attach' to open its console and 'server stop' to shut it down.",
		Examples: []string{
			"mcmgr server launch",
			"mcmgr server launch --attach",
			"mcmgr server launch -s survival",
		},
	}
	var (
		javaArgs stringList
		attach   *bool
	)
	cmd.Flags = func(fs *flag.FlagSet) {
		javaArgs = nil
		fs.Var(&javaArgs, "java-arg", "extra JVM argument for this run; repeatable")
		attach = fs.Bool("attach", false, "attach to the console once the server is up")
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server launch")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}

		if err := ensureJava(cfg, true); err != nil {
			return err
		}

		ui.Step("starting %s (%s %s)", cfg.Name, cfg.Loader, cfg.Minecraft)
		st, err := server.Launch(cfg, server.Options{ExtraJavaArgs: javaArgs})
		if err != nil {
			reportFailure(cfg.Dir())
			return err
		}
		ui.Ok("%s is starting in the background (pid %d)", cfg.Name, st.PID)

		if *attach {
			live, err := server.Probe(cfg.Dir())
			if err != nil || live == nil {
				return fmt.Errorf("server started but is no longer reachable")
			}
			_, err = server.Attach(live, server.AttachOptions{Tail: 200})
			if errors.Is(err, server.ErrDetached) {
				return nil
			}
			return err
		}
		ui.Info("       console:  mcmgr server attach -s %s", cfg.Name)
		ui.Info("       shutdown: mcmgr server stop -s %s", cfg.Name)
		return nil
	}
	return cmd
}

func serverAttachCmd() *Command {
	cmd := &Command{
		Name:  "attach",
		Short: "Attach to a running server's console",
		Long: "Stream a running server's console output and send it typed commands.\n\n" +
			"Ctrl-C or Ctrl-D detaches and leaves the server running. Use 'server run'\n" +
			"for a console where Ctrl-C stops the server instead.",
	}
	var tail *int
	cmd.Flags = func(fs *flag.FlagSet) {
		tail = fs.Int("tail", 200, "lines of recent output to show first")
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server attach")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}
		live, err := requireRunning(cfg)
		if err != nil {
			return err
		}
		if !live.Responsive {
			return fmt.Errorf("the supervisor for %s is not responding; try 'mcmgr server kill'", cfg.Name)
		}
		_, err = server.Attach(live, server.AttachOptions{Tail: *tail})
		if errors.Is(err, server.ErrDetached) {
			return nil
		}
		return err
	}
	return cmd
}

func serverStopCmd() *Command {
	cmd := &Command{
		Name:  "stop",
		Short: "Shut the server down cleanly",
		Long: "Send 'stop' to the server console so it saves and shuts down.\n" +
			"If it has not exited after the grace period it is terminated.",
	}
	var (
		timeout *int
		noWait  *bool
	)
	cmd.Flags = func(fs *flag.FlagSet) {
		timeout = fs.Int("timeout", 0, "seconds to wait before terminating (default: from mcmgr.toml)")
		noWait = fs.Bool("no-wait", false, "return immediately instead of waiting for shutdown")
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server stop")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}
		live, err := requireRunning(cfg)
		if err != nil {
			return err
		}

		grace := *timeout
		if grace <= 0 {
			grace = cfg.StopTimeoutSecs()
		}
		ui.Step("stopping %s", cfg.Name)
		if err := server.Stop(live, grace, !*noWait); err != nil {
			return err
		}
		if *noWait {
			ui.Ok("shutdown requested")
		} else {
			ui.Ok("%s stopped", cfg.Name)
		}
		return nil
	}
	return cmd
}

func serverRestartCmd() *Command {
	cmd := &Command{
		Name:  "restart",
		Short: "Stop the server and start it again in the background",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server restart")
		timeout := fs.Int("timeout", 0, "seconds to wait for shutdown before terminating")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}

		if live, err := server.Probe(cfg.Dir()); err != nil {
			return err
		} else if live != nil {
			grace := *timeout
			if grace <= 0 {
				grace = cfg.StopTimeoutSecs()
			}
			ui.Step("stopping %s", cfg.Name)
			if err := server.Stop(live, grace, true); err != nil {
				return err
			}
		} else {
			ui.Info("%s was not running", cfg.Name)
		}

		ui.Step("starting %s", cfg.Name)
		st, err := server.Launch(cfg, server.Options{})
		if err != nil {
			return err
		}
		ui.Ok("%s restarted (pid %d)", cfg.Name, st.PID)
		return nil
	}
	return cmd
}

func serverKillCmd() *Command {
	cmd := &Command{
		Name:  "kill",
		Short: "Terminate the server immediately without saving",
		Long: "Terminate the server process without giving it a chance to save.\n" +
			"Use 'server stop' unless the server is wedged: unsaved chunks are lost.",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server kill")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}
		live, err := requireRunning(cfg)
		if err != nil {
			return err
		}
		if err := server.Kill(live); err != nil {
			return err
		}
		ui.Ok("%s terminated", cfg.Name)
		return nil
	}
	return cmd
}

// --- inspection --------------------------------------------------------

func serverStatusCmd() *Command {
	cmd := &Command{
		Name:    "status",
		Aliases: []string{"info"},
		Short:   "Show the selected server's configuration and state",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server status")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}
		live, err := server.Probe(cfg.Dir())
		if err != nil {
			return err
		}

		state := ui.Dim + "stopped" + ui.Reset
		if live != nil {
			switch {
			case !live.Responsive:
				state = ui.Yellow + "running (unresponsive)" + ui.Reset
			case live.Status != nil && live.Status.Stopping:
				state = ui.Yellow + "stopping" + ui.Reset
			case live.Status != nil && !live.Status.Ready:
				state = ui.Cyan + "starting" + ui.Reset
			default:
				state = ui.Green + "running" + ui.Reset
			}
		}

		heading("%s  %s", cfg.Name, state)
		field("directory", "%s", cfg.Dir())
		if cfg.LoaderVer != "" {
			field("minecraft", "%s (%s %s)", cfg.Minecraft, cfg.Loader, cfg.LoaderVer)
		} else {
			field("minecraft", "%s (%s)", cfg.Minecraft, cfg.Loader)
		}
		if cfg.Jar != "" {
			field("jar", "%s", cfg.Jar)
		} else if len(cfg.LaunchArgs) > 0 {
			field("launch", "%s", strings.Join(cfg.ExpandedLaunchArgs(), " "))
		}

		if lock, err := cfg.LoadLock(); err == nil && len(lock.Mods) > 0 {
			explicit := 0
			for _, m := range lock.Mods {
				if m.Explicit {
					explicit++
				}
			}
			field(cfg.Loader.ContentDir(), "%d installed (%d requested, %d dependencies)",
				len(lock.Mods), explicit, len(lock.Mods)-explicit)
		}

		if live != nil {
			field("pid", "%d (jvm %d)", live.State.PID, live.State.ChildPID)
			field("uptime", "%s", ui.Duration(live.State.Uptime()))
			field("port", "%d %s(mcmgr control)%s", live.State.Port, ui.Dim, ui.Reset)
		} else {
			if !server.EULAAccepted(cfg.Dir()) {
				field("eula", "%snot accepted - run 'mcmgr server accept-eula'%s", ui.Yellow, ui.Reset)
			}
		}
		field("console", "%s", server.ConsoleLogFor(cfg.Dir()))

		jvm := server.Command(cfg, nil)
		field("command", "%s%s %s%s", ui.Dim, jvm.Path, strings.Join(jvm.Args[1:], " "), ui.Reset)

		// A manifest that disagrees with the jars on disk silently breaks mod
		// compatibility, so say so rather than letting it be discovered later.
		if mismatch := server.VersionMismatch(cfg); mismatch != "" {
			ui.Warn("version mismatch: %s", mismatch)
			ui.Info("       fix with: mcmgr config set minecraft <version>, or mcmgr server install")
		}
		return nil
	}
	return cmd
}

func serverLogsCmd() *Command {
	cmd := &Command{
		Name:  "logs",
		Short: "Show captured console output",
		Long: "Print the console output mcmgr captured for this server.\n" +
			"Works whether or not the server is running.",
		Examples: []string{
			"mcmgr server logs -n 50",
			"mcmgr server logs --follow",
		},
	}
	var (
		lines   *int
		follow  *bool
		followS *bool
	)
	cmd.Flags = func(fs *flag.FlagSet) {
		lines = fs.Int("n", 100, "number of lines to show; -1 for all")
		follow = fs.Bool("follow", false, "keep printing new output until interrupted")
		followS = fs.Bool("f", false, "shorthand for --follow")
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server logs")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}
		return server.TailLog(cfg.Dir(), *lines, *follow || *followS)
	}
	return cmd
}

func serverCmdCmd() *Command {
	cmd := &Command{
		Name:    "cmd",
		Aliases: []string{"exec", "console"},
		Usage:   "<console command>...",
		Short:   "Send one command to a running server and print the reply",
		Examples: []string{
			"mcmgr server cmd list",
			"mcmgr server cmd say Restarting in 5 minutes",
			"mcmgr server cmd whitelist add someone",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server cmd")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr server cmd", "<console command>..."); err != nil {
			return err
		}
		cfg, err := g.config()
		if err != nil {
			return err
		}
		live, err := requireRunning(cfg)
		if err != nil {
			return err
		}
		if !live.Responsive {
			return fmt.Errorf("the supervisor for %s is not responding", cfg.Name)
		}
		return server.SendCommand(live, strings.Join(rest, " "))
	}
	return cmd
}

// --- internal ----------------------------------------------------------

// serverSuperviseCmd is the entry point re-executed by `server launch`. It is
// hidden because it is an implementation detail of running detached.
func serverSuperviseCmd() *Command {
	cmd := &Command{
		Name:   "_supervise",
		Hidden: true,
		Short:  "Internal: run the supervisor in this process",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr server _supervise")
		dir := fs.String("dir", "", "server directory")
		var javaArgs stringList
		fs.Var(&javaArgs, "java-arg", "extra JVM argument; repeatable")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		if *dir == "" {
			return fmt.Errorf("--dir is required")
		}
		cfg, err := config.Load(*dir)
		if err != nil {
			return err
		}
		code, err := server.Supervise(cfg, server.Options{ExtraJavaArgs: javaArgs})
		if err != nil {
			return err
		}
		os.Exit(code)
		return nil
	}
	return cmd
}

// ringCapacityHint is more lines than a server emits between starting and
// answering its control port, so 'server run' shows its output from the top.
const ringCapacityHint = 1000

// requireRunning fetches the live server or explains that there is none.
func requireRunning(cfg *config.Config) (*server.Live, error) {
	live, err := server.Probe(cfg.Dir())
	if err != nil {
		return nil, err
	}
	if live == nil {
		return nil, fmt.Errorf("server %q is not running; start it with 'mcmgr server launch'", cfg.Name)
	}
	return live, nil
}

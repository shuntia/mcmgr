package cli

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/java"
	"mcmgr/internal/mods"
	"mcmgr/internal/props"
	"mcmgr/internal/server"
	"mcmgr/internal/ui"
)

// doctorCmd runs the checks a start would make, all of them, and says which
// failed.
//
// A launch stops at the first problem, so fixing one only reveals the next.
// Doctor exists to show them together, with the command that fixes each.
func doctorCmd() *Command {
	cmd := &Command{
		Name:  "doctor",
		Short: "Check that a server is ready to start, and say what is not",
		Long: "Run every check a start makes and report them all, rather than stopping at\n" +
			"the first failure the way a launch does.\n\n" +
			"Each line ends in the command that fixes it. The exit status is non-zero if\n" +
			"any check failed, so it can gate a deploy script.",
		Examples: []string{
			"mcmgr doctor",
			"mcmgr doctor -s survival",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr doctor")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		return runDoctor(g)
	}
	return cmd
}

// checkState is how a single check came out.
type checkState int

const (
	checkPass checkState = iota
	// checkWarn is something worth knowing that will not stop a start.
	checkWarn
	checkFail
)

// checkLine is one reported check: what was looked at, what was found, and the
// command that would fix it.
type checkLine struct {
	name   string
	state  checkState
	detail string
	fix    string
}

func okCheck(name, detail string) checkLine {
	return checkLine{name: name, state: checkPass, detail: detail}
}

func warnCheck(name, detail, fix string) checkLine {
	return checkLine{name: name, state: checkWarn, detail: detail, fix: fix}
}

func failCheck(name, detail, fix string) checkLine {
	return checkLine{name: name, state: checkFail, detail: detail, fix: fix}
}

func runDoctor(g *globals) error {
	cfg, err := g.config()
	if err != nil {
		// Nothing else can be checked without a manifest, and the resolver's
		// error already says how to select a server.
		return err
	}

	live, probeErr := server.Probe(cfg.Dir())
	running := probeErr == nil && live != nil

	lines := []checkLine{
		okCheck("config", cfg.Path()),
		checkLaunchTarget(cfg),
		checkJava(cfg),
		checkGC(cfg),
		checkEULA(cfg),
		checkManifestMatchesDisk(cfg),
		checkPort(cfg, running),
		checkMods(cfg),
	}

	state := "stopped"
	if running {
		state = "running"
	}
	heading("%s  %s(%s)%s", cfg.Name, ui.Dim, state, ui.Reset)

	width := 0
	for _, l := range lines {
		if n := len(l.name); n > width {
			width = n
		}
	}

	failed := 0
	for _, l := range lines {
		if l.state == checkFail {
			failed++
		}
		fmt.Printf("  %s  %-*s  %s\n", marker(l.state), width, l.name, l.detail)
		if l.fix != "" {
			// Aligned under the detail it belongs to, so the eye reads the
			// problem and its fix as one line.
			fmt.Printf("  %s  %-*s  %s%s%s\n", "    ", width, "", ui.Dim, l.fix, ui.Reset)
		}
	}

	fmt.Println()
	if failed == 0 {
		ui.Ok("%s looks ready to start", cfg.Name)
		return nil
	}
	return fmt.Errorf("%s", plural(failed, "check")+" failed; see above")
}

// marker is the status word each line starts with. All three are four
// characters wide so colour never shifts the columns.
func marker(s checkState) string {
	switch s {
	case checkFail:
		return ui.Red + "fail" + ui.Reset
	case checkWarn:
		return ui.Yellow + "warn" + ui.Reset
	}
	return ui.Green + "ok  " + ui.Reset
}

func checkLaunchTarget(cfg *config.Config) checkLine {
	target := cfg.Jar
	if len(cfg.LaunchArgs) > 0 {
		target = strings.Join(cfg.ExpandedLaunchArgs(), " ")
	}
	if err := server.CheckLaunchTarget(cfg); err != nil {
		return failCheck("server", problem(err), "run: mcmgr server install")
	}
	return okCheck("server", target)
}

// checkGC points out a server still on the JVM's default collector. Not a
// failure: the server starts fine, it just pauses more than it needs to.
func checkGC(cfg *config.Config) checkLine {
	if java.SelectsGC(cfg.Java.Args) {
		return okCheck("gc", "set in java.args")
	}
	status := server.CheckJava(cfg)
	if !status.OK() {
		// Java itself is the problem, and reported just above.
		return okCheck("gc", "JVM default")
	}
	switch {
	case cfg.Java.GC == config.GCZGC && !java.SupportsZGC(status.Info.Major):
		return warnCheck("gc", fmt.Sprintf("zgc is set, but Java %d has no ZGC; the JVM default runs",
			status.Info.Major), "run: mcmgr venv install")
	case cfg.Java.GC == config.GCZGC:
		return okCheck("gc", "ZGC")
	case !java.SupportsZGC(status.Info.Major):
		return okCheck("gc", "JVM default")
	}
	return warnCheck("gc", "JVM default; ZGC is recommended for Minecraft (shorter pauses)",
		"run: mcmgr config set java.gc zgc")
}

func checkJava(cfg *config.Config) checkLine {
	status := server.CheckJava(cfg)
	switch {
	case status.Missing:
		return failCheck("java", fmt.Sprintf("not found (looked for %q)", cfg.JavaBin()),
			"run: mcmgr venv install")
	case status.Err != nil:
		return failCheck("java", problem(status.Err), "run: mcmgr venv install")
	case status.TooOld():
		return failCheck("java", fmt.Sprintf("%s is Java %d, but Minecraft %s needs Java %d",
			status.Info.Path, status.Info.Major, cfg.Minecraft, status.Required),
			fmt.Sprintf("run: mcmgr venv install %d", status.Required))
	case status.Required == 0:
		// Nothing to compare against, so the server may still fail on start.
		return warnCheck("java", fmt.Sprintf("Java %d; no requirement recorded for Minecraft %s",
			status.Info.Major, cfg.Minecraft), "run: mcmgr server install")
	}
	return okCheck("java", fmt.Sprintf("Java %d at %s", status.Info.Major, status.Info.Path))
}

func checkEULA(cfg *config.Config) checkLine {
	if server.EULAAccepted(cfg.Dir()) {
		return okCheck("eula", "accepted")
	}
	return failCheck("eula", "not accepted; the server refuses to start until it is",
		"run: mcmgr server accept-eula")
}

func checkManifestMatchesDisk(cfg *config.Config) checkLine {
	if mismatch := server.VersionMismatch(cfg); mismatch != "" {
		// Not a hard failure: the server starts, but mods are resolved for the
		// version the manifest claims, which is the wrong one.
		return warnCheck("version", mismatch, "run: mcmgr server install, or mcmgr config set minecraft <version>")
	}
	return okCheck("version", fmt.Sprintf("%s (%s)", cfg.Minecraft, cfg.Loader))
}

// defaultPort is Minecraft's own default, used when server.properties does not
// name one — which is the case before the server's first run.
const defaultPort = 25565

func checkPort(cfg *config.Config, running bool) checkLine {
	port, addr := serverAddr(cfg)
	where := net.JoinHostPort(addr, strconv.Itoa(port))

	if running {
		// The server holds the port itself, so a bind test would fail and mean
		// nothing.
		return okCheck("port", fmt.Sprintf("%d, held by this server", port))
	}
	ln, err := net.Listen("tcp", where)
	if err != nil {
		return failCheck("port", fmt.Sprintf("%s is already in use by something else", where),
			fmt.Sprintf("stop it, or run: mcmgr config set port <number>  (currently %d)", port))
	}
	_ = ln.Close()
	return okCheck("port", fmt.Sprintf("%d is free", port))
}

// serverAddr reads the address the server would bind, from server.properties.
func serverAddr(cfg *config.Config) (port int, addr string) {
	port = defaultPort
	p, err := props.Load(cfg.Dir())
	if err != nil {
		return port, ""
	}
	if v, ok := p.Get("server-port"); ok {
		if n, cerr := strconv.Atoi(strings.TrimSpace(v)); cerr == nil && n > 0 {
			port = n
		}
	}
	// An empty server-ip means every interface, which is what an empty host in
	// a listen address means too.
	return port, strings.TrimSpace(p.GetOr("server-ip", ""))
}

func checkMods(cfg *config.Config) checkLine {
	if !cfg.Loader.SupportsMods() {
		return okCheck("mods", fmt.Sprintf("none; a %s server loads no add-ons", cfg.Loader))
	}
	env, err := mods.Open(cfg)
	if err != nil {
		return failCheck("mods", problem(err), "check "+config.LockName)
	}
	drifted := env.Drift()
	if len(drifted) == 0 {
		return okCheck("mods", fmt.Sprintf("%d installed, all matching %s",
			len(env.Lock.Mods), config.LockName))
	}
	return failCheck("mods", fmt.Sprintf("%s missing or changed on disk: %s",
		plural(len(drifted), cfg.Loader.ContentNoun()), strings.Join(drifted, ", ")),
		"run: mcmgr mod sync")
}

// problem reduces an error to the part that fits on a check line: its first
// line, minus any "; run 'x'" advice, since the fix is printed underneath.
func problem(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	line, _, _ = strings.Cut(line, "; run ")
	return strings.TrimSpace(line)
}

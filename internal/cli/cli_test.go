package cli

import (
	"slices"
	"testing"

	"mcmgr/internal/config"
)

// TestParseInterleavesFlagsAndArguments covers a bug where the standard flag
// package stopped parsing at the first positional argument, so
// "mcmgr server create survival --loader fabric" silently ignored --loader and
// treated it as a directory name.
func TestParseInterleavesFlagsAndArguments(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantLoader string
		wantMemory string
		wantRest   []string
	}{
		{
			name:       "flags before arguments",
			args:       []string{"--loader", "fabric", "survival"},
			wantLoader: "fabric",
			wantRest:   []string{"survival"},
		},
		{
			name:       "flags after arguments",
			args:       []string{"survival", "--loader", "fabric"},
			wantLoader: "fabric",
			wantRest:   []string{"survival"},
		},
		{
			name:       "flags surrounding arguments",
			args:       []string{"--memory", "4G", "survival", "--loader", "fabric", "./dir"},
			wantLoader: "fabric",
			wantMemory: "4G",
			wantRest:   []string{"survival", "./dir"},
		},
		{
			name:       "equals form after an argument",
			args:       []string{"survival", "--loader=paper"},
			wantLoader: "paper",
			wantRest:   []string{"survival"},
		},
		{
			name:       "no flags at all",
			args:       []string{"say", "hello", "world"},
			wantLoader: "vanilla",
			wantRest:   []string{"say", "hello", "world"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := &Command{Name: "create"}
			fs, g := newFlags(cmd, "mcmgr server create")
			loader := fs.String("loader", "vanilla", "")
			memory := fs.String("memory", "", "")

			rest, err := parse(fs, g, c.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if *loader != c.wantLoader {
				t.Errorf("loader = %q, want %q", *loader, c.wantLoader)
			}
			if *memory != c.wantMemory {
				t.Errorf("memory = %q, want %q", *memory, c.wantMemory)
			}
			if !slices.Equal(rest, c.wantRest) {
				t.Errorf("positional args = %q, want %q", rest, c.wantRest)
			}
		})
	}
}

// TestParseDoubleDashEndsFlags lets console commands contain dashes, e.g.
// mcmgr server cmd -- time set day.
func TestParseDoubleDashEndsFlags(t *testing.T) {
	cmd := &Command{Name: "cmd"}
	fs, g := newFlags(cmd, "mcmgr server cmd")
	quiet := fs.Bool("dry-run", false, "")

	rest, err := parse(fs, g, []string{"--", "say", "--dry-run", "is", "off"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *quiet {
		t.Error("a flag after -- should have been treated as a positional argument")
	}
	want := []string{"say", "--dry-run", "is", "off"}
	if !slices.Equal(rest, want) {
		t.Errorf("positional args = %q, want %q", rest, want)
	}
}

// TestGlobalFlagsWorkAnywhere checks that --server is accepted in any position,
// since it is registered on every command.
func TestGlobalFlagsWorkAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"-s", "survival", "sodium"},
		{"sodium", "-s", "survival"},
		{"sodium", "--server", "survival"},
	} {
		cmd := &Command{Name: "add"}
		fs, g := newFlags(cmd, "mcmgr mod add")
		rest, err := parse(fs, g, args)
		if err != nil {
			t.Fatalf("parse(%q): %v", args, err)
		}
		if g.server != "survival" {
			t.Errorf("parse(%q): server = %q, want survival", args, g.server)
		}
		if !slices.Equal(rest, []string{"sodium"}) {
			t.Errorf("parse(%q): rest = %q, want [sodium]", args, rest)
		}
	}
}

// TestUnknownCommandSuggests checks that a typo gets a pointer rather than a
// bare rejection.
func TestUnknownCommandSuggests(t *testing.T) {
	root := Root()
	err := dispatch(root, []string{"serve"}, "mcmgr")
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	if !contains(err.Error(), "Did you mean") || !contains(err.Error(), "server") {
		t.Errorf("error should suggest 'server', got: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestHoistGlobalFlags covers accepting global flags before the subcommand,
// e.g. "mcmgr -s live mod add sodium".
func TestHoistGlobalFlags(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"-s", "live", "mod", "add", "sodium"}, []string{"mod", "add", "sodium", "-s", "live"}},
		{[]string{"--server", "live", "server", "status"}, []string{"server", "status", "--server", "live"}},
		{[]string{"--server=live", "server", "status"}, []string{"server", "status", "--server=live"}},
		{[]string{"-q", "-s", "live", "mod", "list"}, []string{"mod", "list", "-q", "-s", "live"}},
		// Nothing to hoist.
		{[]string{"mod", "add", "sodium"}, []string{"mod", "add", "sodium"}},
		{[]string{"mod", "add", "-s", "live"}, []string{"mod", "add", "-s", "live"}},
		// Help and unknown flags are left for dispatch to handle.
		{[]string{"--help"}, []string{"--help"}},
		{[]string{"--bogus", "server"}, []string{"--bogus", "server"}},
		// A value-taking flag with no value is left for the flag package.
		{[]string{"-s"}, []string{"-s"}},
	}
	for _, c := range cases {
		got := hoistGlobalFlags(append([]string(nil), c.in...))
		if !slices.Equal(got, c.want) {
			t.Errorf("hoistGlobalFlags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGlobalFlagBeforeSubcommandReaches checks the hoist end to end: the flag
// survives dispatch and lands in the leaf command's flag set.
func TestGlobalFlagBeforeSubcommandReaches(t *testing.T) {
	var gotServer string
	leaf := &Command{Name: "add"}
	leaf.Run = func(args []string) error {
		fs, g := newFlags(leaf, "mcmgr mod add")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		gotServer = g.server
		return nil
	}
	root := &Command{Name: "mcmgr", Sub: []*Command{{Name: "mod", Sub: []*Command{leaf}}}}

	if err := dispatch(root, []string{"-s", "live", "mod", "add", "sodium"}, "mcmgr"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if gotServer != "live" {
		t.Errorf("server = %q, want live", gotServer)
	}
}

// TestChannelFlagsPrecedence covers how a manifest default and an explicit flag
// combine: the config supplies the baseline, and a flag wins only when it was
// actually typed. Without that distinction there would be no way to switch a
// configured default off for a single command.
func TestChannelFlagsPrecedence(t *testing.T) {
	betaDefault := &config.Config{Defaults: config.Defaults{AllowBeta: true}}
	nothingDefault := &config.Config{}

	cases := []struct {
		name      string
		cfg       *config.Config
		args      []string
		wantBeta  bool
		wantAlpha bool
	}{
		{"config default applies with no flags", betaDefault, nil, true, false},
		{"no config, no flags", nothingDefault, nil, false, false},
		{"flag enables beta", nothingDefault, []string{"--allow-beta"}, true, false},
		{"flag enables alpha only", nothingDefault, []string{"--allow-alpha"}, false, true},
		{"unstable is shorthand for both", nothingDefault, []string{"--allow-unstable"}, true, true},
		// The point of tracking "was it set": an explicit false must beat the
		// configured true.
		{"explicit false overrides the config", betaDefault, []string{"--allow-beta=false"}, false, false},
		{"explicit true is redundant but fine", betaDefault, []string{"--allow-beta"}, true, false},
		{"alpha adds to a configured beta", betaDefault, []string{"--allow-alpha"}, true, true},
		{"unstable=false clears a configured beta", betaDefault, []string{"--allow-unstable=false"}, false, false},
		// A nil config means no baseline at all.
		{"nil config is safe", nil, []string{"--allow-beta"}, true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := &Command{Name: "add"}
			fs, g := newFlags(cmd, "mcmgr mod add")
			channels := addChannelFlags(fs)
			if _, err := parse(fs, g, c.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := channels.resolve(fs, c.cfg)
			if got.Beta != c.wantBeta || got.Alpha != c.wantAlpha {
				t.Errorf("resolve = {Beta:%v Alpha:%v}, want {Beta:%v Alpha:%v}",
					got.Beta, got.Alpha, c.wantBeta, c.wantAlpha)
			}
		})
	}
}

func TestWasSet(t *testing.T) {
	cmd := &Command{Name: "x"}
	fs, g := newFlags(cmd, "mcmgr x")
	flagValue := fs.Bool("thing", false, "")
	other := fs.Bool("other", true, "")

	if _, err := parse(fs, g, []string{"--thing=false"}); err != nil {
		t.Fatal(err)
	}
	// Passed explicitly, even though the value equals the zero default.
	if !wasSet(fs, "thing") {
		t.Error("a flag given on the command line should report as set")
	}
	if wasSet(fs, "other") {
		t.Error("a flag never given should not report as set")
	}
	if *flagValue || !*other {
		t.Errorf("flag values wrong: thing=%v other=%v", *flagValue, *other)
	}
}

func TestBoolAndStringDefaults(t *testing.T) {
	cmd := &Command{Name: "x"}
	fs, g := newFlags(cmd, "mcmgr x")
	prune := fs.Bool("prune", false, "")
	if _, err := parse(fs, g, nil); err != nil {
		t.Fatal(err)
	}

	// Not given: the configured value stands.
	if !boolDefault(fs, "prune", prune, true) {
		t.Error("an unset flag should fall back to the configured value")
	}
	if boolDefault(fs, "prune", prune, false) {
		t.Error("an unset flag with a false config should stay false")
	}

	if got := stringDefault("", "modrinth"); got != "modrinth" {
		t.Errorf("stringDefault = %q, want the configured value", got)
	}
	if got := stringDefault("curseforge", "modrinth"); got != "curseforge" {
		t.Errorf("stringDefault = %q, want the explicit value to win", got)
	}
}

// TestAllFlagsAppearInHelp guards against a flag disappearing from help
// because its description happened to start with the phrase the renderer uses
// to fold one-letter aliases into their long form.
func TestAllFlagsAppearInHelp(t *testing.T) {
	cmd := &Command{Name: "add"}
	fs, _ := newFlags(cmd, "mcmgr mod add")
	addChannelFlags(fs)

	rendered := renderFlags(fs)
	for _, want := range []string{"--allow-beta", "--allow-alpha", "--allow-unstable"} {
		if !contains(rendered, want) {
			t.Errorf("%s is missing from the rendered help:\n%s", want, rendered)
		}
	}
	// The genuine shorthands should still be folded onto one line.
	if !contains(rendered, "--server, -s") {
		t.Errorf("shorthand folding broke:\n%s", rendered)
	}
}

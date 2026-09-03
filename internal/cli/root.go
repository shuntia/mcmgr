package cli

import (
	"fmt"
	"runtime"
	"strings"

	"mcmgr/internal/httpx"
	"mcmgr/internal/ui"
)

// Root builds the top-level command tree.
func Root() *Command {
	return &Command{
		Name:  "mcmgr",
		Short: "Fast, cross-platform Minecraft server and mod manager",
		Long: "mcmgr manages Minecraft servers and their mods from one command,\n" +
			"on Windows, macOS and Linux.",
		Sub: []*Command{
			newCmd(),
			initCmd(),
			serverCmd(),
			modCmd(),
			upgradeCmd(),
			venvCmd(),
			configCmd(),
			versionCmd(),
		},
		Examples: []string{
			"mcmgr new                    # create a server, asking what it needs",
			"mcmgr init                   # adopt a server that already exists here",
			"mcmgr mod add fabric-api sodium lithium",
			"mcmgr server launch          # start in the background",
			"mcmgr server attach          # open its console",
			"mcmgr upgrade                # move to a new Minecraft version",
			"mcmgr config                 # review and change settings",
		},
	}
}

func versionCmd() *Command {
	cmd := &Command{
		Name:  "version",
		Short: "Print the mcmgr version",
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr version")
		if _, err := parse(fs, g, args); err != nil {
			return err
		}
		fmt.Printf("mcmgr %s (%s, %s/%s)\n", httpx.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	}
	return cmd
}

// stringList is a flag.Value for options that may be repeated, such as
// --java-arg.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, " ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// requireArgs reports a usage error when too few positional arguments are given.
func requireArgs(got []string, min int, path, usage string) error {
	if len(got) >= min {
		return nil
	}
	return fmt.Errorf("%s needs %s\n\n  usage: %s %s", path, plural(min, "argument"), path, usage)
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// heading prints a bold section title used by status output.
func heading(format string, a ...any) {
	fmt.Printf("%s%s%s\n", ui.Bold, fmt.Sprintf(format, a...), ui.Reset)
}

// field prints an aligned "  label  value" line.
func field(label, format string, a ...any) {
	fmt.Printf("  %s%-11s%s %s\n", ui.Dim, label, ui.Reset, fmt.Sprintf(format, a...))
}

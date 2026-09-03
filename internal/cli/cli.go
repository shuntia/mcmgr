// Package cli defines the mcmgr command tree.
//
// Commands are a plain tree over the standard flag package rather than a
// third-party framework, which keeps the binary dependency-free and startup
// down to parsing the arguments actually given.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
	"mcmgr/internal/ui"
)

// ErrUsage signals that usage was printed and the process should exit 2
// without printing a further error.
var ErrUsage = errors.New("usage")

// errHelpShown means the user asked for help and got it, which is a success.
var errHelpShown = errors.New("help shown")

// Command is one node in the command tree. A node has either subcommands or a
// Run function, never both.
type Command struct {
	Name    string
	Aliases []string
	// Usage is the argument spec shown after the command path, e.g. "<mod>...".
	Usage string
	// Short is the one-line description shown in listings.
	Short string
	// Long is the paragraph shown in the command's own help.
	Long string
	// Examples are shown verbatim under an EXAMPLES heading.
	Examples []string
	// Hidden keeps internal commands out of help listings.
	Hidden bool
	// Group is the heading a command is listed under in its parent's help.
	// Commands with no group are listed under "Other" when any sibling has
	// one, and in a flat list otherwise.
	Group string
	Sub   []*Command
	// Flags registers this leaf's own options on a flag set. It is optional:
	// a command may still declare its flags inline in Run. Declaring them
	// here as well is what lets `mcmgr completion` list a command's flags
	// without running it.
	Flags func(*flag.FlagSet)
	// Run executes a leaf command. Flags are registered inside it via
	// newFlags, so no parsing happens for commands that are not invoked.
	Run func(args []string) error
}

// matches reports whether a typed name selects this command.
func (c *Command) matches(name string) bool {
	if c.Name == name {
		return true
	}
	for _, a := range c.Aliases {
		if a == name {
			return true
		}
	}
	return false
}

// Main parses args and runs the selected command.
func Main(args []string) error {
	err := dispatch(Root(), args, "mcmgr")
	if errors.Is(err, errHelpShown) {
		return nil
	}
	return err
}

// dispatch walks the command tree.
func dispatch(cmd *Command, args []string, path string) error {
	if len(cmd.Sub) == 0 {
		if cmd.Run == nil {
			return fmt.Errorf("command %q is not implemented", path)
		}
		return cmd.Run(args)
	}

	// Global flags are accepted before the subcommand too ("mcmgr -s live mod
	// list"), so move any leading ones to the end where the leaf command's own
	// flag set will parse them.
	args = hoistGlobalFlags(args)

	if len(args) == 0 {
		// A parent command may define its own behaviour for the bare form,
		// which is how "mcmgr config" opens the interactive editor.
		if cmd.Run != nil {
			return cmd.Run(nil)
		}
		printHelp(cmd, path)
		return ErrUsage
	}

	name := args[0]
	switch name {
	case "-h", "--help", "help":
		// "mcmgr server help stop" shows help for the named subcommand.
		if len(args) > 1 {
			for _, s := range cmd.Sub {
				if s.matches(args[1]) {
					printHelp(s, path+" "+s.Name)
					return nil
				}
			}
		}
		printHelp(cmd, path)
		return nil
	}

	for _, s := range cmd.Sub {
		if s.matches(name) {
			return dispatch(s, args[1:], path+" "+s.Name)
		}
	}
	return unknownCommand(cmd, path, name)
}

// globalFlagArity lists the flags every command accepts, and whether each
// takes a value. They are the only flags meaningful before a subcommand.
var globalFlagArity = map[string]bool{
	"s": true, "server": true,
	"q": false, "quiet": false,
}

// hoistGlobalFlags moves leading global flags to the end of the argument list,
// so "mcmgr -s live mod add sodium" reaches the leaf command as
// "add sodium -s live". Parsing stops at the first argument that is not a
// recognised global flag, leaving everything else untouched.
func hoistGlobalFlags(args []string) []string {
	var hoisted []string
	i := 0
scan:
	for i < len(args) {
		arg := args[i]
		if arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-") {
			break
		}

		name := strings.TrimLeft(arg, "-")
		base, _, hasInlineValue := strings.Cut(name, "=")

		takesValue, known := globalFlagArity[base]
		if !known {
			// -h, --help and anything unrecognised are left in place for
			// dispatch to handle or report.
			break
		}
		switch {
		case hasInlineValue || !takesValue:
			hoisted = append(hoisted, arg)
			i++
		case i+1 < len(args):
			hoisted = append(hoisted, arg, args[i+1])
			i += 2
		default:
			// A value-taking flag with nothing after it: leave it in place so
			// the flag package reports the missing value.
			break scan
		}
	}

	if len(hoisted) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	out = append(out, args[i:]...)
	return append(out, hoisted...)
}

// unknownCommand builds an error that suggests near matches rather than just
// rejecting the input.
func unknownCommand(cmd *Command, path, name string) error {
	var near []string
	for _, s := range cmd.Sub {
		if s.Hidden {
			continue
		}
		if strings.HasPrefix(s.Name, name) || strings.HasPrefix(name, s.Name) {
			near = append(near, s.Name)
		}
	}
	msg := fmt.Sprintf("unknown command %q for %q", name, path)
	if len(near) > 0 {
		msg += fmt.Sprintf("\n\nDid you mean: %s", strings.Join(near, ", "))
	}
	return fmt.Errorf("%s\n\nRun '%s --help' to see available commands", msg, path)
}

// globals are the flags every command accepts.
type globals struct {
	server string
	quiet  bool
	// path is the full command path, used in error messages.
	path string
}

// newFlags builds a flag set pre-populated with the global flags and wired to
// mcmgr's help output.
func newFlags(cmd *Command, path string) (*flag.FlagSet, *globals) {
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by the caller, not the flag package

	g := &globals{path: path}
	fs.StringVar(&g.server, "server", "", "name of a registered server, or a path to one")
	fs.StringVar(&g.server, "s", "", "shorthand for --server")
	fs.BoolVar(&g.quiet, "quiet", false, "suppress progress output")
	fs.BoolVar(&g.quiet, "q", false, "shorthand for --quiet")

	// The leaf's own flags, when it declares them separately from Run. A
	// fresh flag set each time keeps this from redefining anything.
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}

	fs.Usage = func() { printHelpWithFlags(cmd, path, fs) }
	return fs, g
}

// parse runs the flag set and returns the positional arguments.
//
// The standard flag package stops parsing at the first non-flag argument,
// which would make "mcmgr server create survival --loader fabric" silently
// ignore every flag. Parsing is therefore repeated over what is left after
// each positional, so flags and arguments can be interleaved the way people
// actually type them. An explicit "--" still ends flag parsing, so console
// commands containing dashes can be passed through.
func parse(fs *flag.FlagSet, g *globals, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}

	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				// flag already invoked fs.Usage for us.
				return nil, errHelpShown
			}
			return nil, fmt.Errorf("%v\n\nRun '%s --help' for usage", err, g.path)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}

	ui.Quiet = g.quiet
	return append(positional, tail...), nil
}

// config resolves the server this invocation targets.
func (g *globals) config() (*config.Config, error) {
	return config.Resolve(g.server)
}

// modEnv resolves the server and loads its lockfile.
func (g *globals) modEnv() (*mods.Env, error) {
	cfg, err := g.config()
	if err != nil {
		return nil, err
	}
	return mods.Open(cfg)
}

// --- help rendering ----------------------------------------------------

func printHelp(cmd *Command, path string) { printHelpWithFlags(cmd, path, nil) }

func printHelpWithFlags(cmd *Command, path string, fs *flag.FlagSet) {
	renderHelp(os.Stdout, cmd, path, fs)
}

func renderHelp(w io.Writer, cmd *Command, path string, fs *flag.FlagSet) {
	if cmd.Long != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(cmd.Long))
	} else if cmd.Short != "" {
		fmt.Fprintf(w, "%s\n\n", cmd.Short)
	}

	usage := path
	if cmd.Usage != "" {
		usage += " " + cmd.Usage
	} else if len(cmd.Sub) > 0 {
		usage += " <command>"
	}
	if fs != nil || len(cmd.Sub) == 0 {
		usage += " [flags]"
	}
	fmt.Fprintf(w, "%sUSAGE%s\n  %s\n", ui.Bold, ui.Reset, usage)

	if visible := visibleSubs(cmd); len(visible) > 0 {
		fmt.Fprintf(w, "\n%sCOMMANDS%s\n", ui.Bold, ui.Reset)
		width := 0
		for _, s := range visible {
			if n := len(s.Name); n > width {
				width = n
			}
		}
		for i, g := range groupSubs(visible) {
			indent := "  "
			if g.name != "" {
				if i > 0 {
					fmt.Fprintln(w)
				}
				fmt.Fprintf(w, "  %s%s%s\n", ui.Dim, g.name, ui.Reset)
				indent = "    "
			}
			for _, s := range g.cmds {
				fmt.Fprintf(w, "%s%-*s  %s\n", indent, width, s.Name, s.Short)
			}
		}
	}

	if fs != nil {
		if flagsText := renderFlags(fs); flagsText != "" {
			fmt.Fprintf(w, "\n%sFLAGS%s\n%s", ui.Bold, ui.Reset, flagsText)
		}
	}

	if len(cmd.Examples) > 0 {
		fmt.Fprintf(w, "\n%sEXAMPLES%s\n", ui.Bold, ui.Reset)
		for _, e := range cmd.Examples {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}

	if len(cmd.Sub) > 0 {
		fmt.Fprintf(w, "\nRun '%s <command> --help' for more detail.\n", path)
	}
}

// commandGroup is one heading in a help listing and the commands under it.
type commandGroup struct {
	name string
	cmds []*Command
}

// groupSubs arranges commands under their Group headings, in the order the
// groups first appear. With no groups at all the result is one unnamed group,
// so short trees stay a flat list.
func groupSubs(subs []*Command) []commandGroup {
	grouped := false
	for _, s := range subs {
		if s.Group != "" {
			grouped = true
			break
		}
	}
	if !grouped {
		return []commandGroup{{cmds: subs}}
	}

	var out []commandGroup
	index := map[string]int{}
	var other []*Command
	for _, s := range subs {
		if s.Group == "" {
			other = append(other, s)
			continue
		}
		i, ok := index[s.Group]
		if !ok {
			i = len(out)
			index[s.Group] = i
			out = append(out, commandGroup{name: s.Group})
		}
		out[i].cmds = append(out[i].cmds, s)
	}
	if len(other) > 0 {
		out = append(out, commandGroup{name: "Other", cmds: other})
	}
	return out
}

func visibleSubs(cmd *Command) []*Command {
	var out []*Command
	for _, s := range cmd.Sub {
		if !s.Hidden {
			out = append(out, s)
		}
	}
	return out
}

// renderFlags formats a flag set, pairing each long flag with its one-letter
// shorthand on a single line instead of listing both separately.
func renderFlags(fs *flag.FlagSet) string {
	type entry struct {
		names []string
		usage string
		def   string
	}
	entries := map[string]*entry{}
	var order []string

	// First pass: collect the primary flags. Shorthands are folded in
	// afterwards, because VisitAll walks flags in lexical order and a
	// shorthand such as -q is reached before the --quiet it belongs to.
	fs.VisitAll(func(f *flag.Flag) {
		if strings.HasPrefix(f.Usage, "shorthand for ") {
			return
		}
		entries[f.Name] = &entry{names: []string{f.Name}, usage: f.Usage, def: f.DefValue}
		order = append(order, f.Name)
	})

	fs.VisitAll(func(f *flag.Flag) {
		target, ok := strings.CutPrefix(f.Usage, "shorthand for --")
		if !ok {
			return
		}
		if e, found := entries[target]; found {
			e.names = append(e.names, f.Name)
		}
	})

	sort.Strings(order)

	var lines [][2]string
	width := 0
	for _, k := range order {
		e := entries[k]
		var parts []string
		for _, n := range e.names {
			if len(n) == 1 {
				parts = append(parts, "-"+n)
			} else {
				parts = append(parts, "--"+n)
			}
		}
		left := strings.Join(parts, ", ")
		if e.def != "" && e.def != "false" && e.def != "0" {
			left += " " + e.def
		}
		if len(left) > width {
			width = len(left)
		}
		lines = append(lines, [2]string{left, e.usage})
	}

	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, l[0], l[1])
	}
	return b.String()
}

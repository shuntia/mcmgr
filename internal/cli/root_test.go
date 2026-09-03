package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestShortcutsReachServerCommands covers the top-level spellings of the
// lifecycle commands: 'mcmgr stop' must be exactly 'mcmgr server stop'.
func TestShortcutsReachServerCommands(t *testing.T) {
	root := Root()
	var srv *Command
	for _, s := range root.Sub {
		if s.Name == "server" {
			srv = s
		}
	}
	if srv == nil {
		t.Fatal("no server command")
	}

	for _, name := range shortcutNames {
		var short, target *Command
		for _, s := range root.Sub {
			if s.Name == name {
				short = s
			}
		}
		for _, s := range srv.Sub {
			if s.matches(name) {
				target = s
			}
		}
		if short == nil || target == nil {
			t.Errorf("shortcut %q is missing at the top level or under server", name)
			continue
		}
		if short.Run == nil || len(short.Sub) > 0 {
			t.Errorf("shortcut %q should be a leaf that runs", name)
		}
		if !strings.Contains(short.Short, "server "+target.Name) {
			t.Errorf("shortcut %q should say where it comes from, got %q", name, short.Short)
		}
		// Asking for help must reach the real command's flag set and succeed.
		if err := dispatch(root, []string{name, "--help"}, "mcmgr"); !errors.Is(err, errHelpShown) {
			t.Errorf("mcmgr %s --help: %v", name, err)
		}
	}
}

// TestHelpIsGrouped covers the headings in help listings, and that a command
// with no group still appears rather than vanishing.
func TestHelpIsGrouped(t *testing.T) {
	var out bytes.Buffer
	renderHelp(&out, Root(), "mcmgr", nil)
	text := out.String()

	for _, want := range []string{"Getting started", "Servers", "Mods", "Settings", "Shortcuts", "Other", "version"} {
		if !strings.Contains(text, want) {
			t.Errorf("root help lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "_supervise") {
		t.Error("hidden command leaked into help")
	}

	// A tree with no groups stays flat.
	flat := &Command{Name: "x", Sub: []*Command{{Name: "a", Short: "A"}, {Name: "b", Short: "B"}}}
	out.Reset()
	renderHelp(&out, flat, "x", nil)
	if strings.Contains(out.String(), "Other") {
		t.Errorf("ungrouped tree should not invent headings:\n%s", out.String())
	}
}

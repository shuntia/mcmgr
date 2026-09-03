package cli

import (
	"strings"
	"testing"
)

var completionShells = []string{"bash", "zsh", "fish", "powershell"}

// offers reports whether a script suggests word at the given node.
//
// Each shell spells the same table differently, so the check is per-shell
// rather than a bare substring search, which "run" would satisfy by accident
// almost anywhere.
func offers(shell, script, node, word string) bool {
	switch shell {
	case "bash", "zsh":
		return strings.Contains(script, `"`+node+" "+word+`"`)
	case "powershell":
		return strings.Contains(script, `'`+node+" "+word+`'`)
	case "fish":
		// fish keys off words already typed rather than a command path, so
		// only the offered word itself can be checked.
		return strings.Contains(script, " -a '"+word+"' ")
	}
	return false
}

// TestCompletionCoversTheCommandTree keeps every shell's script in step with
// the tree: the shortcuts people actually type, and the server subcommands
// they reach through.
func TestCompletionCoversTheCommandTree(t *testing.T) {
	serverSubs := []string{
		"run", "launch", "attach", "stop", "restart", "kill",
		"status", "logs", "cmd", "install", "list", "use", "forget",
		"accept-eula", "set-jar",
	}

	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			script, err := completionScript(shell)
			if err != nil {
				t.Fatalf("completionScript(%q): %v", shell, err)
			}
			if strings.TrimSpace(script) == "" {
				t.Fatal("the script is empty")
			}
			if strings.Contains(script, "_supervise") {
				t.Error("the hidden supervisor command leaked into completions")
			}

			for _, name := range shortcutNames {
				if !offers(shell, script, "_root", name) {
					t.Errorf("the top level does not offer the shortcut %q", name)
				}
			}
			for _, name := range serverSubs {
				if !offers(shell, script, "_root_server", name) {
					t.Errorf("mcmgr server does not offer %q", name)
				}
			}
			// Aliases are typed as often as the names they stand for.
			for _, alias := range []string{"srv", "mods", "plugin"} {
				if !offers(shell, script, "_root", alias) {
					t.Errorf("the top level does not offer the alias %q", alias)
				}
			}
		})
	}
}

// TestCompletionListsLeafFlags covers the reason Command.Flags exists: a
// leaf's own options have to be enumerable without running it.
func TestCompletionListsLeafFlags(t *testing.T) {
	bash, err := completionScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"_root_server_run":    {"--foreground", "--java-arg"},
		"_root_server_launch": {"--attach"},
		"_root_server_stop":   {"--timeout", "--no-wait"},
		"_root_server_logs":   {"-n", "--follow"},
		"_root_mod_add":       {"--version", "--no-deps", "--allow-client-only", "--provider"},
		"_root_mod_update":    {"--dry-run"},
		"_root_mod_search":    {"--limit", "--any"},
		"_root_config_set":    {"--force", "--yes"},
		// A shortcut runs the same code, so it must offer the same flags.
		"_root_run": {"--foreground"},
	}
	for node, flags := range cases {
		line := wordsFor(t, bash, node)
		for _, f := range flags {
			if !strings.Contains(line+" ", f+" ") && !strings.HasSuffix(line, f) {
				t.Errorf("%s does not offer %s; got %q", node, f, line)
			}
		}
	}

	// A command that has not moved its flags to Command.Flags still completes,
	// with the global flags every command accepts.
	sync := wordsFor(t, bash, "_root_mod_sync")
	for _, f := range []string{"-s", "--server", "-q", "--quiet"} {
		if !strings.Contains(sync+" ", f+" ") {
			t.Errorf("mod sync should still offer the global flag %s; got %q", f, sync)
		}
	}

	fish, err := completionScript("fish")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fish, "-l foreground") {
		t.Error("the fish script should register --foreground as a long option")
	}
}

// wordsFor pulls one node's suggestion list out of the bash script.
func wordsFor(t *testing.T, script, node string) string {
	t.Helper()
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		prefix := node + ") echo \""
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(line, prefix), "\" ;;")
		}
	}
	t.Fatalf("no completion entry for %s", node)
	return ""
}

func TestCompletionRejectsUnknownShells(t *testing.T) {
	_, err := completionScript("tcsh")
	if err == nil {
		t.Fatal("an unsupported shell should be an error, not an empty script")
	}
	// The error has to say what is supported, since the whole command is one
	// argument.
	for _, want := range completionShells {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should list %q as supported, got: %v", want, err)
		}
	}
}

// TestCompletionIsRegistered keeps the command reachable.
func TestCompletionIsRegistered(t *testing.T) {
	var found *Command
	for _, s := range Root().Sub {
		if s.Name == "completion" {
			found = s
		}
	}
	if found == nil {
		t.Fatal("mcmgr completion is not in the command tree")
	}
	if found.Run == nil {
		t.Error("completion should be a leaf that runs")
	}
}

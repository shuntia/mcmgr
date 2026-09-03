package cli

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

func completionCmd() *Command {
	cmd := &Command{
		Name:  "completion",
		Usage: "<bash|zsh|fish|powershell>",
		Short: "Print a shell completion script",
		Long: "Print a completion script for one shell, generated from the command tree, so\n" +
			"it lists exactly the commands, aliases and flags this binary has.\n\n" +
			"The script is static: it never runs mcmgr to work out what to suggest, so\n" +
			"completing a word costs nothing and works with no server configured.",
		Examples: []string{
			"mcmgr completion bash > /etc/bash_completion.d/mcmgr",
			`mcmgr completion zsh > "${fpath[1]}/_mcmgr"`,
			"mcmgr completion fish > ~/.config/fish/completions/mcmgr.fish",
			"mcmgr completion powershell | Out-String | Invoke-Expression",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr completion")
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}
		if err := requireArgs(rest, 1, "mcmgr completion", "<bash|zsh|fish|powershell>"); err != nil {
			return err
		}
		script, err := completionScript(rest[0])
		if err != nil {
			return err
		}
		fmt.Print(script)
		return nil
	}
	return cmd
}

// completionNode is one command flattened out of the tree, with everything a
// completion script needs to answer for it.
type completionNode struct {
	// id identifies the node inside the generated script, e.g.
	// "_root_server_run".
	id string
	// path is the command path, for comments in the script.
	path string
	// names are the words that select this node from its parent, aliases
	// included. The root has none.
	names []string
	// words are the completions offered at this node: subcommand names and
	// aliases, then flags.
	words []string
	// children maps a typed word to the node it selects.
	children map[string]string
	// subs are the visible child nodes, with their descriptions, in tree
	// order.
	subs []completionSub
}

// completionSub is one child as its parent advertises it.
type completionSub struct {
	names []string
	id    string
	short string
}

// completionTree flattens the command tree, parents before children.
func completionTree() []completionNode {
	var out []completionNode

	var walk func(cmd *Command, id, path string, names []string)
	walk = func(cmd *Command, id, path string, names []string) {
		node := completionNode{id: id, path: path, names: names, children: map[string]string{}}
		visible := visibleSubs(cmd)
		for _, sub := range visible {
			childID := id + "_" + identifier(sub.Name)
			subNames := append([]string{sub.Name}, sub.Aliases...)
			for _, n := range subNames {
				node.words = append(node.words, n)
				node.children[n] = childID
			}
			node.subs = append(node.subs, completionSub{names: subNames, id: childID, short: sub.Short})
		}
		node.words = append(node.words, flagWords(cmd, path)...)
		out = append(out, node)

		for i, sub := range visible {
			walk(sub, node.subs[i].id, path+" "+sub.Name, node.subs[i].names)
		}
	}
	walk(Root(), "_root", "mcmgr", nil)
	return out
}

// flagWords lists a command's flags as they are typed.
//
// Flags are registered by newFlags, which is how a command that declares them
// only inside Run still contributes its global -s/--server and -q/--quiet: the
// leaf's own options appear as well once it moves them to Command.Flags.
func flagWords(cmd *Command, path string) []string {
	fs, _ := newFlags(cmd, path)
	var out []string
	fs.VisitAll(func(f *flag.Flag) {
		if len(f.Name) == 1 {
			out = append(out, "-"+f.Name)
			return
		}
		out = append(out, "--"+f.Name)
	})
	return out
}

// identifier turns a command name into something usable in a shell function
// name or hash key.
func identifier(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, name)
}

// completionScript renders the script for one shell.
func completionScript(shell string) (string, error) {
	tree := completionTree()
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "bash":
		return bashCompletion(tree), nil
	case "zsh":
		return zshCompletion(tree), nil
	case "fish":
		return fishCompletion(tree), nil
	case "powershell", "pwsh":
		return powershellCompletion(tree), nil
	}
	return "", fmt.Errorf("no completion script for %q\n"+
		"       supported shells: bash, zsh, fish, powershell", shell)
}

// childCases renders the "which node does this word select" table shared by the
// bash and zsh scripts, as quoted case patterns.
func childCases(tree []completionNode, indent string) string {
	var b strings.Builder
	for _, node := range tree {
		if len(node.children) == 0 {
			continue
		}
		// Group the words that lead to the same child onto one pattern.
		byChild := map[string][]string{}
		var order []string
		for _, w := range node.words {
			child, ok := node.children[w]
			if !ok {
				continue
			}
			if _, seen := byChild[child]; !seen {
				order = append(order, child)
			}
			byChild[child] = append(byChild[child], w)
		}
		for _, child := range order {
			patterns := make([]string, 0, len(byChild[child]))
			for _, w := range byChild[child] {
				patterns = append(patterns, fmt.Sprintf("%q", node.id+" "+w))
			}
			fmt.Fprintf(&b, "%s%s) echo %s ;;\n", indent, strings.Join(patterns, "|"), child)
		}
	}
	return b.String()
}

// wordCases renders the "what can be typed here" table for bash and zsh.
func wordCases(tree []completionNode, indent string) string {
	var b strings.Builder
	for _, node := range tree {
		fmt.Fprintf(&b, "%s%s) echo %q ;;\n", indent, node.id, strings.Join(node.words, " "))
	}
	return b.String()
}

func bashCompletion(tree []completionNode) string {
	return `# bash completion for mcmgr, generated by 'mcmgr completion bash'.
#
# Install with one of:
#   mcmgr completion bash > /etc/bash_completion.d/mcmgr
#   mcmgr completion bash > ~/.local/share/bash-completion/completions/mcmgr

_mcmgr_child() {
    case "$1 $2" in
` + childCases(tree, "        ") + `        *) echo "" ;;
    esac
}

_mcmgr_words() {
    case "$1" in
` + wordCases(tree, "        ") + `        *) echo "" ;;
    esac
}

_mcmgr() {
    local cur node next w i
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"

    # Walk the words already typed to find which command they select. Flags
    # are skipped, so they may appear anywhere.
    node="_root"
    for ((i = 1; i < COMP_CWORD; i++)); do
        w="${COMP_WORDS[i]}"
        case "$w" in -*) continue ;; esac
        next="$(_mcmgr_child "$node" "$w")"
        if [ -n "$next" ]; then node="$next"; fi
    done

    COMPREPLY=($(compgen -W "$(_mcmgr_words "$node")" -- "$cur"))
}

complete -F _mcmgr mcmgr
`
}

func zshCompletion(tree []completionNode) string {
	return `#compdef mcmgr
# zsh completion for mcmgr, generated by 'mcmgr completion zsh'.
#
# Install with:
#   mcmgr completion zsh > "${fpath[1]}/_mcmgr"
# then start a new shell.

_mcmgr_child() {
    case "$1 $2" in
` + childCases(tree, "        ") + `        *) echo "" ;;
    esac
}

_mcmgr_words() {
    case "$1" in
` + wordCases(tree, "        ") + `        *) echo "" ;;
    esac
}

_mcmgr() {
    local node next w
    local -i i

    # words[1] is "mcmgr" itself, so the typed arguments start at 2.
    node="_root"
    for (( i = 2; i < CURRENT; i++ )); do
        w="${words[i]}"
        [[ "$w" == -* ]] && continue
        next="$(_mcmgr_child "$node" "$w")"
        [[ -n "$next" ]] && node="$next"
    done

    compadd -- ${=$(_mcmgr_words "$node")}
}

_mcmgr "$@"
`
}

// fishQuote wraps a description in single quotes, the way fish expects.
func fishQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

func fishCompletion(tree []completionNode) string {
	var b strings.Builder
	b.WriteString(`# fish completion for mcmgr, generated by 'mcmgr completion fish'.
#
# Install with:
#   mcmgr completion fish > ~/.config/fish/completions/mcmgr.fish

# mcmgr takes no file arguments, so do not offer any.
complete -c mcmgr -f
`)

	// A shortcut such as "mcmgr run" and the "mcmgr server run" it stands for
	// produce the same condition, so identical lines are emitted once.
	seen := map[string]bool{}
	emit := func(line string) {
		if seen[line] {
			return
		}
		seen[line] = true
		b.WriteString(line)
	}

	for _, node := range tree {
		// fish has no notion of a command path, only of words already seen, so
		// each node is conditioned on its own names having been typed.
		condition := "__fish_use_subcommand"
		if len(node.names) > 0 {
			condition = "__fish_seen_subcommand_from " + strings.Join(node.names, " ")
		}
		fmt.Fprintf(&b, "\n# %s\n", node.path)
		for _, sub := range node.subs {
			for _, name := range sub.names {
				emit(fmt.Sprintf("complete -c mcmgr -n '%s' -a %s -d %s\n",
					condition, fishQuote(name), fishQuote(sub.short)))
			}
		}
		for _, word := range node.words {
			switch {
			case strings.HasPrefix(word, "--"):
				emit(fmt.Sprintf("complete -c mcmgr -n '%s' -l %s\n", condition, word[2:]))
			case strings.HasPrefix(word, "-"):
				emit(fmt.Sprintf("complete -c mcmgr -n '%s' -s %s\n", condition, word[1:]))
			}
		}
	}
	return b.String()
}

func powershellCompletion(tree []completionNode) string {
	var words, children strings.Builder
	for _, node := range tree {
		fmt.Fprintf(&words, "        %s = %s\n",
			psQuote(node.id), psQuote(strings.Join(node.words, " ")))

		keys := make([]string, 0, len(node.children))
		for w := range node.children {
			keys = append(keys, w)
		}
		sort.Strings(keys)
		for _, w := range keys {
			fmt.Fprintf(&children, "        %s = %s\n",
				psQuote(node.id+" "+w), psQuote(node.children[w]))
		}
	}

	return `# PowerShell completion for mcmgr, generated by 'mcmgr completion powershell'.
#
# Install by adding this to your profile ($PROFILE):
#   mcmgr completion powershell | Out-String | Invoke-Expression

Register-ArgumentCompleter -Native -CommandName mcmgr -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)

    $words = @{
` + words.String() + `    }
    $children = @{
` + children.String() + `    }

    # Walk the words already typed to find which command they select. Flags
    # are skipped, so they may appear anywhere.
    $node = '_root'
    $elements = $commandAst.CommandElements
    for ($i = 1; $i -lt $elements.Count; $i++) {
        $w = $elements[$i].ToString()
        if ($w.StartsWith('-')) { continue }
        if ($w -eq $wordToComplete) { continue }
        $key = "$node $w"
        if ($children.ContainsKey($key)) { $node = $children[$key] }
    }

    $words[$node] -split ' ' | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`
}

// psQuote renders a PowerShell single-quoted string, in which the only escape
// is a doubled quote.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

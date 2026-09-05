package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// RepairReport says what Repair changed, so the caller can tell the user.
type RepairReport struct {
	// Forgotten maps each registry entry dropped to the directory it named,
	// which no longer holds a server.
	Forgotten map[string]string
	// OldDefault and NewDefault are set when the default server changed.
	// NewDefault is empty when no server is left to be the default.
	OldDefault, NewDefault string
	// DefaultChanged distinguishes "reseated to nothing" from "untouched".
	DefaultChanged bool
	// JavaPathCleared is a global java_path that pointed at a binary which no
	// longer exists, now unset.
	JavaPathCleared string
}

// Empty reports whether the repair found nothing to fix.
func (r RepairReport) Empty() bool {
	return len(r.Forgotten) == 0 && !r.DefaultChanged && r.JavaPathCleared == ""
}

// Lines describes each change in a sentence, for printing one per line.
func (r RepairReport) Lines() []string {
	var out []string
	names := make([]string, 0, len(r.Forgotten))
	for n := range r.Forgotten {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, fmt.Sprintf("forgot %s: %s no longer holds a server", n, r.Forgotten[n]))
	}
	if r.DefaultChanged {
		if r.NewDefault != "" {
			out = append(out, fmt.Sprintf("the default server is now %s", r.NewDefault))
		} else {
			out = append(out, "no default server is left; commands run outside a server directory need --server")
		}
	}
	if r.JavaPathCleared != "" {
		out = append(out, fmt.Sprintf("unset global.java_path: %s no longer exists, so servers use java from PATH",
			r.JavaPathCleared))
	}
	return out
}

// Repair puts the machine-wide state back in order after a server has gone
// away, whether its directory was deleted or it was forgotten:
//
//   - registry entries whose directories no longer hold a manifest are
//     dropped, and a default that named one of them is reseated;
//   - a global java_path whose binary no longer exists is unset, since a
//     runtime that lived under a deleted server would otherwise stop every
//     other server from starting.
//
// It is best effort: whatever could be repaired is reported even when a later
// step fails, and nothing is written when there is nothing to fix.
func Repair() (RepairReport, error) {
	var rep RepairReport

	reg, err := LoadRegistry()
	if err != nil {
		return rep, err
	}
	before := reg.Default
	dirs := map[string]string{}
	for name, dir := range reg.Servers {
		dirs[name] = dir
	}
	gone, err := reg.Prune()
	if err != nil {
		return rep, err
	}
	if len(gone) > 0 {
		rep.Forgotten = map[string]string{}
		for _, n := range gone {
			rep.Forgotten[n] = dirs[n]
		}
	}
	if reg.Default != before {
		rep.DefaultChanged = true
		rep.OldDefault, rep.NewDefault = before, reg.Default
	}

	glob, err := LoadGlobal()
	if err != nil {
		return rep, err
	}
	if glob.JavaPath != "" {
		if _, serr := os.Stat(glob.JavaPath); os.IsNotExist(serr) {
			rep.JavaPathCleared = glob.JavaPath
			glob.JavaPath = ""
			if err := glob.Save(); err != nil {
				return rep, err
			}
			refreshGlobalCache(glob)
		}
	}
	return rep, nil
}

// repairNote runs Repair and renders what it did as continuation lines for an
// error message, so a command that failed because a server is gone can say
// what was tidied up. A failed repair is folded in the same way rather than
// hiding the original problem.
func repairNote() string {
	rep, err := Repair()
	lines := rep.Lines()
	if err != nil {
		lines = append(lines, "could not repair the registry: "+err.Error())
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n       " + strings.Join(lines, "\n       ")
}

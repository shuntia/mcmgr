package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
	"mcmgr/internal/props"
	"mcmgr/internal/ui"
)

// offerVersionMove turns "this mod has no build for your version" into a
// choice. When err is a NoBuildError with a usable alternative, the user is
// offered a move to the nearest Minecraft version the mod does support, by
// the same path as `mcmgr upgrade`: the plan for every other add-on is shown
// and confirmed first. It reports whether the server was moved, in which case
// the caller should retry the install.
//
// A newer version is preferred, because a world can always be brought
// forward. A downgrade is only offered while the server has no world yet: a
// world already played on a newer version does not load on an older one.
//
// It never asks in a script, and never asks when the user said --no-input;
// offer is that decision, made by the caller.
func offerVersionMove(env *mods.Env, err error, ch mods.Channels, offer bool) (moved bool, rerr error) {
	var nb *mods.NoBuildError
	if !offer || !errors.As(err, &nb) {
		return false, nil
	}
	cfg := env.Cfg
	newer, older := nb.Nearest()
	target, downgrade := newer, false
	if target == "" {
		target, downgrade = older, true
	}
	if target == "" {
		return false, nil
	}
	if downgrade && worldExists(cfg) {
		ui.Warn("%s only has builds for older versions (%s); moving %s down is not offered, "+
			"because its world has already been played on %s and would not load",
			nb.Name, strings.Join(nb.Nearby(6), ", "), cfg.Name, cfg.Minecraft)
		return false, nil
	}

	fmt.Println()
	ui.Info("%s%s%s has no build for %s %s, but it does for %s",
		ui.Bold, nb.Name, ui.Reset, cfg.Loader, cfg.Minecraft, strings.Join(nb.Nearby(6), ", "))
	reason := fmt.Sprintf("so that %s can be installed", nb.Name)
	if downgrade {
		reason += ", which means moving to an older version; that is only safe because this\n" +
			"server has no world yet"
	}
	uerr := runUpgrade(cfg, env, target, ch, upgradeOptions{reason: reason})
	switch {
	case uerr == nil:
		return true, nil
	case errors.Is(uerr, errCancelled):
		// Declined: the caller reports the original error, which already
		// names the version to move to by hand.
		return false, nil
	}
	return false, uerr
}

// worldExists reports whether the server's world has been created, which is
// what makes a downgrade unsafe. The world directory is named by
// server.properties, defaulting to "world" as the server itself does.
func worldExists(cfg *config.Config) bool {
	level := "world"
	if p, err := props.Load(cfg.Dir()); err == nil {
		level = p.GetOr("level-name", level)
	}
	_, err := os.Stat(filepath.Join(cfg.Dir(), level, "level.dat"))
	return err == nil
}

// Package warn holds the warnings mcmgr attaches to individual settings.
//
// The point is that a setting which quietly breaks a server later should say
// so at the moment it is changed, in terms of what actually goes wrong, not a
// generic "are you sure?".
package warn

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mcmgr/internal/ui"
)

// Level separates "you should know this" from "this can get your server
// griefed or your world eaten".
type Level int

const (
	// Caution is a consequence worth knowing before continuing.
	Caution Level = iota
	// Danger is a security or data-loss consequence.
	Danger
)

// Warning is one message about a setting.
type Warning struct {
	Level Level
	// Title is a single line naming the consequence.
	Title string
	// Body explains it and, where possible, how to avoid the problem.
	Body string
}

// Context is what the warning rules need to know about the server to decide
// whether a warning applies and how to word it.
type Context struct {
	// Dir is the server directory, used to look for an existing world.
	Dir string
	// LevelName is the world directory from server.properties.
	LevelName string
	// Running is true when the server is up.
	Running bool
	// Old is the current value of the setting, when there is one.
	Old string
	// ModCount is how many add-ons are installed.
	ModCount int
}

// WorldExists reports whether the configured world directory is present, which
// is what makes version and seed changes dangerous rather than academic.
func (c Context) WorldExists() bool {
	level := c.LevelName
	if level == "" {
		level = "world"
	}
	fi, err := os.Stat(filepath.Join(c.Dir, level))
	return err == nil && fi.IsDir()
}

// truthy reports whether a properties value means "on".
func truthy(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// ForProperty returns warnings triggered by setting a server.properties key.
func ForProperty(key, value string, ctx Context) []Warning {
	var out []Warning

	switch key {
	case "white-list":
		if truthy(value) {
			out = append(out, Warning{
				Level: Caution,
				Title: "Only whitelisted players will be able to join",
				Body: "That includes you. Add yourself before or right after the next start:\n" +
					"  running server:  mcmgr server cmd whitelist add <your-username>\n" +
					"  stopped server:  edit whitelist.json in the server directory\n" +
					"Operators listed in ops.json are still subject to the whitelist.",
			})
		}

	case "enforce-whitelist":
		if truthy(value) {
			out = append(out, Warning{
				Level: Caution,
				Title: "Players removed from the whitelist are kicked immediately",
				Body: "Without this, a player already online stays connected until they leave. " +
					"With it, removing someone from the whitelist disconnects them at once.",
			})
		}

	case "online-mode":
		if !truthy(value) {
			out = append(out, Warning{
				Level: Danger,
				Title: "Turning off online-mode lets anyone join as any username",
				Body: "Player identities are no longer verified against Mojang, so:\n" +
					"  - anyone can connect using an operator's username and inherit their permissions\n" +
					"  - player UUIDs change, which can detach existing inventories, homes and\n" +
					"    permissions from their owners\n" +
					"  - the whitelist stops being a real access control, since names can be faked\n" +
					"Only do this on a private network, or behind a proxy that authenticates players\n" +
					"itself. Do not expose an offline-mode server to the internet.",
			})
		}

	case "enable-rcon":
		if truthy(value) {
			out = append(out, Warning{
				Level: Danger,
				Title: "RCON exposes a remote console on a TCP port",
				Body: "Anyone who reaches that port and knows rcon.password gets full console access. " +
					"Set a long rcon.password and keep the port off the public internet.\n" +
					"mcmgr does not need RCON: 'mcmgr server cmd' talks to the server over a local " +
					"socket that is not reachable from the network.",
			})
		}

	case "enable-command-block":
		if truthy(value) {
			out = append(out, Warning{
				Level: Caution,
				Title: "Command blocks can run commands as the server",
				Body: "Any player who can place and edit command blocks, which normally means " +
					"operators and anyone in creative with build access, can run console-level " +
					"commands through them.",
			})
		}

	case "allow-flight":
		if truthy(value) {
			out = append(out, Warning{
				Level: Caution,
				Title: "Flight in survival mode will no longer be kicked for",
				Body: "This is needed by some mods, but it also disables one of the checks against " +
					"movement cheats.",
			})
		}

	case "hardcore":
		if truthy(value) {
			out = append(out, Warning{
				Level: Danger,
				Title: "Hardcore makes death permanent",
				Body: "A player who dies is locked into spectator mode and cannot respawn. " +
					"There is no way to undo this from inside the game; it needs an edit to the " +
					"player data file or an operator command.",
			})
		}

	case "level-seed":
		if value != "" && ctx.Old != value && ctx.WorldExists() {
			out = append(out, Warning{
				Level: Caution,
				Title: "The existing world will not be regenerated",
				Body: "A seed only applies to chunks generated from now on. Changing it on a world " +
					"that already exists leaves the generated area alone and makes new terrain " +
					"beyond it join at a visible seam.\nTo start fresh, change level-name instead, " +
					"or move the world directory aside.",
			})
		}

	case "level-name":
		if ctx.Old != "" && ctx.Old != value {
			out = append(out, Warning{
				Level: Caution,
				Title: "The server will load a different world",
				Body: fmt.Sprintf("%q stays on disk untouched, and %q is created if it does not exist. "+
					"Players, inventories and builds live in the world directory, so this swaps all of them.",
					ctx.Old, value),
			})
		}

	case "difficulty":
		if strings.EqualFold(value, "peaceful") {
			out = append(out, Warning{
				Level: Caution,
				Title: "Peaceful removes all hostile mobs",
				Body: "Existing hostile mobs despawn, mob spawners stop producing them, and hunger " +
					"no longer damages players.",
			})
		}

	case "spawn-protection":
		if value == "0" {
			out = append(out, Warning{
				Level: Caution,
				Title: "Anyone will be able to build at spawn",
				Body: "Spawn protection is the only thing stopping non-operators from breaking " +
					"blocks in the spawn area.",
			})
		}

	case "server-port":
		if ctx.Old != "" && ctx.Old != value {
			out = append(out, Warning{
				Level: Caution,
				Title: "Players will need the new port",
				Body: "Anyone with the old address saved has to update it, and port forwarding or " +
					"firewall rules pointing at " + ctx.Old + " need changing too.",
			})
		}
	}

	return out
}

// ForManifest returns warnings triggered by changing a key in mcmgr.toml.
func ForManifest(key, value string, ctx Context) []Warning {
	var out []Warning

	switch key {
	case "minecraft":
		if ctx.Old != "" && ctx.Old != value && ctx.WorldExists() {
			out = append(out, Warning{
				Level: Danger,
				Title: "Changing the Minecraft version of an existing world is one-way",
				Body: fmt.Sprintf("This world was last opened on %s. Starting it on %s will:\n"+
					"  - upgrade the world format irreversibly, if %s is newer\n"+
					"  - very likely corrupt or refuse to load the world, if %s is older\n"+
					"Minecraft has no supported downgrade path. Back up the world directory before "+
					"starting the server.", ctx.Old, value, value, value),
			})
		}
		if ctx.ModCount > 0 && ctx.Old != value {
			out = append(out, Warning{
				Level: Caution,
				Title: fmt.Sprintf("The %d installed add-on(s) are built for %s", ctx.ModCount, ctx.Old),
				Body: "They will not load on a different version, and some will crash the server " +
					"outright. Run 'mcmgr mod update' after changing the version, and expect some " +
					"mods not to have a build for it yet.",
			})
		}

	case "loader":
		if ctx.Old != "" && ctx.Old != value {
			out = append(out, Warning{
				Level: Danger,
				Title: "Mods are not portable between loaders",
				Body: fmt.Sprintf("Everything installed for %s will fail to load on %s, and the "+
					"add-on directory may change too. Run 'mcmgr server install' to fetch the new "+
					"server binary and 'mcmgr mod sync' to reinstall add-ons afterwards.",
					ctx.Old, value),
			})
		}

	case "stop_timeout_secs":
		if n, err := strconv.Atoi(value); err == nil && n > 0 && n < 20 {
			out = append(out, Warning{
				Level: Caution,
				Title: "A short shutdown timeout can cut off a world save",
				Body: fmt.Sprintf("mcmgr terminates the server if it has not exited %d seconds after "+
					"being asked to stop. A large or heavily modded world often needs longer than "+
					"that to save, and killing it mid-save loses recent chunks.", n),
			})
		}
	}

	return out
}

// EULA is the agreement text shown before recording acceptance. It is never
// accepted implicitly.
var EULA = Warning{
	Level: Caution,
	Title: "Minecraft End User Licence Agreement",
	Body: "A Minecraft server will not start until you confirm you accept Mojang's EULA.\n" +
		"Read it at https://aka.ms/MinecraftEULA\n" +
		"Answering yes writes eula=true to eula.txt, which is you agreeing to it.",
}

// RunningServer warns that a change cannot take effect until a restart.
func RunningServer(what string) Warning {
	return Warning{
		Level: Caution,
		Title: "The server is running",
		Body:  what + " takes effect the next time it starts. Use 'mcmgr server restart' to apply it now.",
	}
}

// PropertiesWhileRunning explains why editing server.properties on a live
// server loses the edit.
var PropertiesWhileRunning = Warning{
	Level: Danger,
	Title: "The server rewrites server.properties when it stops",
	Body: "Editing the file while the server is running means your change is overwritten by " +
		"the server's own copy at shutdown, silently.\nStop the server first with " +
		"'mcmgr server stop', or pass --force if you intend to change it anyway.",
}

// Print writes a warning to stderr in a consistent shape.
func Print(w Warning) {
	label, colour := "caution:", ui.Yellow
	if w.Level == Danger {
		label, colour = "warning:", ui.Red
	}
	fmt.Fprintf(os.Stderr, "\n%s%s%s %s%s%s\n", colour, label, ui.Reset, ui.Bold, w.Title, ui.Reset)
	for _, l := range strings.Split(w.Body, "\n") {
		fmt.Fprintf(os.Stderr, "  %s\n", l)
	}
}

// PrintAll writes every warning and reports whether any was a Danger.
func PrintAll(ws []Warning) bool {
	danger := false
	for _, w := range ws {
		Print(w)
		if w.Level == Danger {
			danger = true
		}
	}
	if len(ws) > 0 {
		fmt.Fprintln(os.Stderr)
	}
	return danger
}

// Summary renders a warning as a compact block for use inside a prompt
// description, where stderr output would be overwritten by the form.
func Summary(w Warning) string {
	label := "Caution"
	if w.Level == Danger {
		label = "Warning"
	}
	return fmt.Sprintf("%s: %s\n\n%s", label, w.Title, w.Body)
}

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
	"mcmgr/internal/prompt"
	"mcmgr/internal/server"
	"mcmgr/internal/ui"
)

func initCmd() *Command {
	cmd := &Command{
		Name:  "init",
		Usage: "[directory]",
		Short: "Adopt an existing server directory",
		Long: "Take over a server that already exists, without moving or reinstalling it.\n\n" +
			"mcmgr inspects the directory to work out the loader, Minecraft version and\n" +
			"server jar, asks about anything it cannot determine, writes " + config.ManifestName + ",\n" +
			"and registers the server so it can be used by name.\n\n" +
			"Add-ons already in the mods directory are identified by file hash against\n" +
			"Modrinth, so they can be updated and reinstalled like ones mcmgr installed.",
		Examples: []string{
			"mcmgr init                    # adopt the current directory",
			"mcmgr init /srv/minecraft",
			"mcmgr init --name survival --no-adopt-mods",
		},
	}
	cmd.Run = func(args []string) error {
		fs, g := newFlags(cmd, "mcmgr init")
		var (
			name       = fs.String("name", "", "name to register the server under")
			mc         = fs.String("mc", "", "Minecraft version, if it cannot be detected")
			loaderName = fs.String("loader", "", "server platform: "+config.JoinLoaders())
			jar        = fs.String("jar", "", "server jar to launch, relative to the directory")
			noAdopt    = fs.Bool("no-adopt-mods", false, "do not try to identify existing mods")
			noRegister = fs.Bool("no-register", false, "do not add the server to the global registry")
			noInput    = fs.Bool("no-input", false, "never prompt; fail if a required value is missing")
			doProbe    = fs.Bool("probe", false, "if the version cannot be read, run the jar in a temporary directory to ask it")
			noProbe    = fs.Bool("no-probe", false, "never run the jar, even as a last resort")
		)
		rest, err := parse(fs, g, args)
		if err != nil {
			return err
		}

		dir := "."
		if len(rest) > 0 {
			dir = rest[0]
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if fi, serr := os.Stat(abs); serr != nil || !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", abs)
		}
		if config.Exists(abs) {
			return fmt.Errorf("%s is already an mcmgr server\n"+
				"       use 'mcmgr config' to change its settings", abs)
		}

		interactive := prompt.Interactive() && !*noInput
		found := server.Inspect(abs)
		reportInspection(abs, found)

		// Nothing static identified this server. As a last resort the jar can
		// be asked directly, by running it in a throwaway directory. This runs
		// before anything is asked, so its answers can fill in the prompts.
		// Probe when something is still missing that a flag has not supplied.
		needsProbe := found.Jar != "" &&
			((found.Minecraft == "" && *mc == "") || (found.Loader == "" && *loaderName == ""))
		if needsProbe && !*noProbe {
			probe := *doProbe
			if !probe && interactive {
				if probe, err = prompt.Confirm("Run the server jar to identify it?",
					"Nothing in this directory says which Minecraft version it is.\n\n"+
						"mcmgr can start the jar in a temporary directory, read the version from\n"+
						"its startup output and stop it again. Your server directory, world and\n"+
						"configuration are not touched.\n\n"+
						"Paper and Fabric fetch the game on first start, so this may use network.",
					true); err != nil {
					return promptErr(err)
				}
			}
			if probe {
				ui.Step("starting %s in a temporary directory to identify it", found.Jar)
				result, perr := server.ProbeJar(filepath.Join(abs, found.Jar), "", 0)
				switch {
				case perr != nil:
					ui.Warn("probe failed: %v", perr)
				default:
					if found.Minecraft == "" && result.Version != "" {
						found.Minecraft = result.Version
						found.MinecraftFrom = found.Jar + " (started to check)"
						ui.Ok("the server reports Minecraft %s", result.Version)
					}
					if found.Loader == "" && result.Loader != "" {
						found.Loader, found.LoaderVer = result.Loader, result.LoaderVer
						found.LoaderFrom = found.Jar + " (started to check)"
						ui.Ok("the server reports %s %s", result.Loader, result.LoaderVer)
					}
				}
			}
		}

		// --- loader -------------------------------------------------------
		loaderValue := *loaderName
		if loaderValue == "" {
			loaderValue = string(found.Loader)
		}
		if loaderValue == "" {
			if !interactive {
				return fmt.Errorf("could not determine the loader; pass --loader %s", config.JoinLoaders())
			}
			if loaderValue, err = prompt.Select("Server platform",
				"mcmgr could not tell which platform this server uses.",
				loaderOptions(), string(config.Vanilla)); err != nil {
				return promptErr(err)
			}
		} else if interactive && *loaderName == "" {
			ok, cerr := prompt.Confirm(fmt.Sprintf("Detected %s. Is that right?", loaderValue), "", true)
			if cerr != nil {
				return promptErr(cerr)
			}
			if !ok {
				if loaderValue, err = prompt.Select("Server platform", "",
					loaderOptions(), loaderValue); err != nil {
					return promptErr(err)
				}
			}
		}
		loader, err := config.ParseLoader(loaderValue)
		if err != nil {
			return err
		}

		// --- Minecraft version --------------------------------------------
		version := *mc
		if version == "" {
			version = found.Minecraft
		}

		if version == "" {
			if !interactive {
				return fmt.Errorf("could not determine the Minecraft version\n" +
					"       pass --mc <version>, or --probe to run the jar and ask it")
			}
			if version, err = prompt.Text("Minecraft version",
				"mcmgr could not read the version from this directory. It decides which mod\n"+
					"builds are compatible, so it needs to match what the server actually runs.",
				"", prompt.ValidateNonEmpty("version")); err != nil {
				return promptErr(err)
			}
		} else if interactive && *mc == "" {
			if version, err = prompt.Text("Minecraft version",
				"Detected from the server directory. Correct it if it is wrong.",
				version, prompt.ValidateNonEmpty("version")); err != nil {
				return promptErr(err)
			}
		}

		// --- name ----------------------------------------------------------
		serverName := *name
		if serverName == "" {
			serverName = strings.ToLower(filepath.Base(abs))
			if prompt.ValidateName(serverName) != nil {
				serverName = "server"
			}
			if interactive {
				if serverName, err = prompt.Text("Server name",
					"Used to refer to this server from anywhere, e.g. 'mcmgr server launch -s <name>'.",
					serverName, prompt.ValidateName); err != nil {
					return promptErr(err)
				}
			}
		}

		// --- jar -------------------------------------------------------------
		jarPath := *jar
		if jarPath == "" {
			jarPath = found.Jar
		}
		if jarPath == "" && len(found.LaunchArgs) == 0 {
			if len(found.Jars) == 0 {
				return fmt.Errorf("no .jar file found in %s\n"+
					"       put the server jar there, or pass --jar <path>", abs)
			}
			if !interactive {
				return fmt.Errorf("could not tell which jar to launch; pass --jar <path>\n"+
					"       candidates: %s", strings.Join(found.Jars, ", "))
			}
			opts := make([]prompt.Option, len(found.Jars))
			for i, j := range found.Jars {
				opts[i] = prompt.Opt(j)
			}
			if jarPath, err = prompt.Select("Server jar",
				"Which file starts the server?", opts, found.Jars[0]); err != nil {
				return promptErr(err)
			}
		}

		// --- write it out ------------------------------------------------------
		cfg, err := config.Init(abs, &config.Config{
			Name:       serverName,
			Minecraft:  version,
			Loader:     loader,
			LoaderVer:  found.LoaderVer,
			Jar:        jarPath,
			LaunchArgs: found.LaunchArgs,
			ServerArgs: []string{"nogui"},
			Java:       config.Java{},
			Mods:       []config.ModSpec{},
		})
		if err != nil {
			return err
		}
		ui.Ok("wrote %s", cfg.Path())

		if !*noRegister {
			reg, rerr := config.LoadRegistry()
			if rerr != nil {
				return rerr
			}
			if rerr := reg.Add(serverName, abs); rerr != nil {
				return rerr
			}
		}

		if loader.SupportsMods() && !*noAdopt {
			if err := adoptMods(cfg); err != nil {
				ui.Warn("could not identify existing add-ons: %v", err)
			}
		}

		ui.Ok("adopted %s as %q", abs, serverName)
		if !server.EULAAccepted(abs) {
			ui.Info("       the Minecraft EULA is not accepted yet: mcmgr server accept-eula -s %s", serverName)
		}
		fmt.Printf("\n%sNext steps%s\n", ui.Bold, ui.Reset)
		fmt.Printf("  mcmgr config -s %s          %s# review settings%s\n", serverName, ui.Dim, ui.Reset)
		fmt.Printf("  mcmgr server launch -s %s\n", serverName)
		return nil
	}
	return cmd
}

// reportInspection shows what was detected and, just as importantly, where
// each value came from, so the user can tell a value read out of a jar from a
// guess made off a file name.
func reportInspection(dir string, d server.Detected) {
	heading("Inspecting %s", dir)

	if d.Loader != "" {
		value := string(d.Loader)
		if d.LoaderVer != "" {
			value += " " + d.LoaderVer
		}
		field("loader", "%s%s", value, source(d.LoaderFrom))
	} else {
		field("loader", "%snot detected%s", ui.Yellow, ui.Reset)
	}

	if d.Minecraft != "" {
		field("minecraft", "%s%s", d.Minecraft, source(d.MinecraftFrom))
	} else {
		field("minecraft", "%snot detected%s", ui.Yellow, ui.Reset)
	}

	if d.Jar != "" {
		field("jar", "%s", d.Jar)
	} else if len(d.LaunchArgs) > 0 {
		field("launch", "%s", strings.Join(d.LaunchArgs, " "))
	}
	if d.World != "" {
		field("world", "%s", d.World)
	}
	if d.AddOns > 0 {
		field("add-ons", "%d file(s) already installed", d.AddOns)
	}
	fmt.Println()
}

// source renders where a detected value came from, dimmed.
func source(from string) string {
	if from == "" {
		return ""
	}
	return fmt.Sprintf("  %sfrom %s%s", ui.Dim, from, ui.Reset)
}

// adoptMods identifies jars already in the content directory by hashing them
// and asking Modrinth what they are, so existing add-ons become manageable
// instead of being reported as unmanaged files forever.
func adoptMods(cfg *config.Config) error {
	env, err := mods.Open(cfg)
	if err != nil {
		return err
	}
	adopted, unknown, err := env.Adopt()
	if err != nil {
		return err
	}
	if adopted == 0 && len(unknown) == 0 {
		return nil
	}
	if adopted > 0 {
		ui.Ok("identified %d existing %s(s) on Modrinth", adopted, cfg.Loader.ContentNoun())
	}
	if len(unknown) > 0 {
		ui.Warn("%d file(s) could not be identified and stay unmanaged: %s",
			len(unknown), strings.Join(unknown, ", "))
		ui.Info("       mcmgr will leave them alone; 'mcmgr mod sync --prune' would delete them")
	}
	return nil
}

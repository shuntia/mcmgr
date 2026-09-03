package cli

import (
	"flag"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
)

// wasSet reports whether a flag was actually given on the command line, as
// opposed to sitting at its zero value.
//
// This is what lets a manifest default and an explicit flag coexist. Without
// it there is no way to tell `--allow-beta=false` from not passing the flag at
// all, so a config default could never be switched off for one command.
func wasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// channelFlags are the release-channel options every command that resolves a
// mod version accepts.
type channelFlags struct {
	beta     *bool
	alpha    *bool
	unstable *bool
}

// addChannelFlags registers the channel options on a flag set.
func addChannelFlags(fs *flag.FlagSet) channelFlags {
	return channelFlags{
		beta:  fs.Bool("allow-beta", false, "accept beta builds as well as full releases"),
		alpha: fs.Bool("allow-alpha", false, "accept alpha builds as well as full releases"),
		// The wording matters: help rendering folds away any flag whose usage
		// starts with "shorthand for ", treating it as a one-letter alias.
		unstable: fs.Bool("allow-unstable", false,
			"accept both beta and alpha builds"),
	}
}

// resolve merges the server's configured channels with whatever was typed.
// Flags are applied after the config, so --allow-beta=false can switch off a
// default for a single command.
func (c channelFlags) resolve(fs *flag.FlagSet, cfg *config.Config) mods.Channels {
	var ch mods.Channels
	if cfg != nil {
		ch = mods.ChannelsFrom(cfg.Defaults)
	}
	if wasSet(fs, "allow-unstable") {
		ch.Beta, ch.Alpha = *c.unstable, *c.unstable
	}
	if wasSet(fs, "allow-beta") {
		ch.Beta = *c.beta
	}
	if wasSet(fs, "allow-alpha") {
		ch.Alpha = *c.alpha
	}
	return ch
}

// boolDefault resolves a boolean option that has a configured default.
func boolDefault(fs *flag.FlagSet, name string, flagValue *bool, configured bool) bool {
	if wasSet(fs, name) {
		return *flagValue
	}
	return configured
}

// stringDefault resolves a string option that has a configured default.
func stringDefault(flagValue string, configured string) string {
	if flagValue != "" {
		return flagValue
	}
	return configured
}

// keys returns the set's members in a stable order, for messages.
func keys(set map[string]bool) []string {
	var out []string
	for _, k := range []string{"beta", "alpha"} {
		if set[k] {
			out = append(out, k)
		}
	}
	for k := range set {
		if k != "beta" && k != "alpha" {
			out = append(out, k)
		}
	}
	return out
}

package mods

import (
	"mcmgr/internal/config"
)

// Recommendation is a well-known server-side add-on worth offering to someone
// setting up a server, with the one-line reason a newcomer needs to decide.
type Recommendation struct {
	// Slug is the Modrinth project slug, which is also what `mod add` takes.
	Slug string
	Name string
	// Why says what it does for a server, in one line.
	Why string
	// Default marks the ones pre-selected in the picker: safe on any server,
	// with no gameplay changes, and what most people would install anyway.
	Default bool
	// loaders are the platforms it runs on. Empty means every modded loader.
	loaders []config.Loader
}

// Provider is where every recommendation comes from. Modrinth works with no
// API key, which is what makes this usable in `mcmgr new` before anything is
// configured.
func (r Recommendation) Provider() string { return ProviderModrinth }

// supports reports whether the recommendation runs on a loader.
func (r Recommendation) supports(l config.Loader) bool {
	if len(r.loaders) == 0 {
		return l.SupportsMods()
	}
	for _, x := range r.loaders {
		if x == l {
			return true
		}
	}
	return false
}

var (
	fabricLike = []config.Loader{config.Fabric, config.Quilt}
	modLoaders = []config.Loader{config.Fabric, config.Quilt, config.NeoForge}
	paperOnly  = []config.Loader{config.Paper}
)

// recommendations is the curated list, in the order shown.
//
// Every entry is strictly server-side: its author marks it as running on the
// server and as not needing every player to install it too, which
// TestRecommendationsExistOnModrinth re-checks against the live API. Mods that
// need a client half, however popular, do not belong here: a server-side list
// is for things that work for whoever connects.
//
// The picks follow what the community lists for server operators agree on:
// the Fabric wiki's established server-side mods, the fabric-serverside-mods
// lists, and the Paper and NeoForge server guides. Kept deliberately short;
// the search step covers everything else.
var recommendations = []Recommendation{
	// --- mod loaders: performance, no gameplay changes ---------------------
	{Slug: "fabric-api", Name: "Fabric API",
		Why:     "the library nearly every Fabric mod depends on",
		Default: true, loaders: fabricLike},
	{Slug: "lithium", Name: "Lithium",
		Why:     "optimises game logic and ticking without changing behaviour",
		Default: true, loaders: modLoaders},
	{Slug: "ferrite-core", Name: "FerriteCore",
		Why:     "cuts the server's memory use",
		Default: true, loaders: modLoaders},
	{Slug: "krypton", Name: "Krypton",
		Why:     "optimises the networking stack",
		Default: true, loaders: fabricLike},
	{Slug: "modernfix", Name: "ModernFix",
		Why:     "faster startup and lower memory, especially with many mods",
		Default: true, loaders: modLoaders},
	{Slug: "noisium", Name: "Noisium",
		Why:     "faster world generation",
		loaders: modLoaders},
	{Slug: "alternate-current", Name: "Alternate Current",
		Why:     "faster redstone with the same behaviour",
		loaders: modLoaders},
	{Slug: "servercore", Name: "ServerCore",
		Why:     "dynamic tweaks that keep TPS up under load",
		loaders: modLoaders},
	{Slug: "ai-improvements", Name: "AI Improvements",
		Why:     "cheaper mob pathfinding",
		loaders: []config.Loader{config.NeoForge}},

	// --- mod loaders: the everyday server plugins, as mods -----------------
	{Slug: "essential-commands", Name: "Essential Commands",
		Why:     "homes, warps, /tpa and the other everyday commands",
		loaders: fabricLike},
	{Slug: "ledger", Name: "Ledger",
		Why:     "logs block changes so griefing can be traced and rolled back",
		loaders: fabricLike},
	{Slug: "flan", Name: "Flan",
		Why:     "land claims players manage themselves",
		loaders: modLoaders},
	{Slug: "universal-graves", Name: "Universal Graves",
		Why:     "a grave keeps a player's items where they died",
		loaders: fabricLike},

	// --- Paper: the plugins almost every server ends up with ---------------
	{Slug: "luckperms", Name: "LuckPerms",
		Why:     "permissions; most other plugins expect one to be installed",
		Default: true, loaders: paperOnly},
	{Slug: "essentialsx", Name: "EssentialsX",
		Why:     "homes, warps, kits, /tp and the other everyday commands",
		Default: true, loaders: paperOnly},
	{Slug: "coreprotect", Name: "CoreProtect",
		Why:     "logs every block change so griefing can be rolled back",
		Default: true, loaders: paperOnly},
	{Slug: "vaultunlocked", Name: "VaultUnlocked",
		Why:     "the bridge plugins use to share economy and permissions",
		loaders: paperOnly},
	{Slug: "worldguard", Name: "WorldGuard",
		Why:     "protected regions and per-area rules",
		loaders: paperOnly},
	{Slug: "griefprevention", Name: "GriefPrevention",
		Why:     "land claims players manage themselves",
		loaders: paperOnly},
	{Slug: "multiverse-core", Name: "Multiverse-Core",
		Why:     "several worlds on one server",
		loaders: paperOnly},
	{Slug: "placeholderapi", Name: "PlaceholderAPI",
		Why:     "the placeholders other plugins use in chat, tab and scoreboards",
		loaders: paperOnly},
	{Slug: "discordsrv", Name: "DiscordSRV",
		Why:     "bridges in-game chat with a Discord channel",
		loaders: paperOnly},
	{Slug: "grimac", Name: "GrimAC",
		Why:     "anti-cheat",
		loaders: paperOnly},

	// --- everything: administration and quality of life --------------------
	// LuckPerms appears twice because it means something different on each
	// platform: on Paper it is the permissions plugin everything else assumes,
	// on a mod loader it is a nice-to-have. Each loader still sees it once.
	{Slug: "worldedit", Name: "WorldEdit",
		Why:     "in-game building and terrain tools",
		loaders: []config.Loader{config.Fabric, config.Quilt, config.NeoForge, config.Paper}},
	{Slug: "luckperms", Name: "LuckPerms",
		Why:     "permissions for commands and mods that support them",
		loaders: modLoaders},
	{Slug: "spark", Name: "spark",
		Why:     "profiler that shows what is causing lag",
		loaders: modLoaders},
	{Slug: "tabtps", Name: "TabTPS",
		Why:     "TPS and tick time in the tab list",
		loaders: []config.Loader{config.Fabric, config.NeoForge, config.Paper}},
	{Slug: "chunky", Name: "Chunky",
		Why: "pre-generates the world so players never wait on chunks"},
	{Slug: "bluemap", Name: "BlueMap",
		Why: "a 3D web map of the world, viewable in a browser"},
	{Slug: "viaversion", Name: "ViaVersion",
		Why:     "lets players on newer Minecraft versions join",
		loaders: []config.Loader{config.Fabric, config.Quilt, config.Paper}},
	{Slug: "geyser", Name: "Geyser",
		Why:     "lets Bedrock Edition players join",
		loaders: []config.Loader{config.Fabric, config.NeoForge, config.Paper}},
}

// Recommended lists the add-ons worth offering for a loader, in display order.
// A loader that cannot load mods gets nothing.
func Recommended(l config.Loader) []Recommendation {
	var out []Recommendation
	for _, r := range recommendations {
		if r.supports(l) {
			out = append(out, r)
		}
	}
	return out
}

// RecommendedDefaults is the pre-selected subset of Recommended: what
// `mcmgr new --recommended` installs without asking.
func RecommendedDefaults(l config.Loader) []Recommendation {
	var out []Recommendation
	for _, r := range Recommended(l) {
		if r.Default {
			out = append(out, r)
		}
	}
	return out
}

# mcmgr

A fast, cross-platform Minecraft server and mod manager in a single binary.

Create a server, install mods from Modrinth or CurseForge, run it in the
background, attach to its console, and stop it — from one command, on Windows,
macOS and Linux.

```
mcmgr new                    # asks for anything it needs, and offers recommended mods
mcmgr mod add fabric-api sodium lithium
mcmgr start                  # in the background
mcmgr attach                 # open its console; Ctrl-D detaches
```

- **Guided when you want it, scriptable when you don't.** `mcmgr new` asks for
  whatever you did not pass as a flag; supply every flag and it runs unattended.
- **Actually cross-platform.** The same commands, the same config file and the
  same background-server behaviour on all three operating systems.
- **Private by default.** A new server has the whitelist on; the setup says how
  to add yourself, and `--whitelist=false` turns it off.
- **A sensible mod list from the start.** Setup offers a short, curated list of
  server-side mods or plugins for the platform you chose, and lets you search
  Modrinth for more, before the server first starts.
- **Mods are never swapped under a live server.** Every command that writes to
  the mods directory refuses to run while the server is up.
- **Dangerous settings explain themselves.** Turning off player verification or
  changing a world's Minecraft version tells you what will break, before it does.

## Install

Requires Go 1.21+ to build, and a JDK to run Minecraft servers.

```bash
go build -o mcmgr .
```

Cross-compile for another platform from any machine:

```bash
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o mcmgr-linux-arm64 .
```

Put the binary somewhere on your `PATH`.

## Quick start

```bash
# Create a server. Anything you leave out is asked for, including which of the
# recommended mods to install; the whitelist is on unless you say otherwise.
mcmgr new survival --loader fabric --mc 1.21.4 --memory 4G

# Install more mods. Required dependencies come along automatically.
mcmgr mod add -s survival sodium lithium

# Start it in the background and open its console.
mcmgr start -s survival
mcmgr attach -s survival               # Ctrl-C or Ctrl-D detaches, server keeps running

# Or keep the console in this terminal, like running the jar by hand.
mcmgr run -s survival                  # Ctrl-C stops it, Ctrl-D detaches

# Later
mcmgr cmd -s survival say Restarting in 5 minutes
mcmgr stop -s survival
```

`start`, `stop`, `restart`, `run`, `attach`, `status`, `logs` and `cmd` are
shortcuts for the same commands under `mcmgr server`.

Inside a server directory the `-s` flag is unnecessary — mcmgr finds the server
by walking up from the working directory.

Already have a server? Adopt it where it stands:

```bash
cd /srv/minecraft && mcmgr init
```

`init` works out the loader, version and jar from the directory, and identifies
the mods already in `mods/` by file hash so they become updatable.

### How the version is detected

The Minecraft version is read from the most trustworthy source available, and
`init` says which one it used, so a value read out of a jar is distinguishable
from a guess:

| Source | Applies to |
| --- | --- |
| `install.properties` inside the jar | Fabric, Quilt — records the exact game and loader version |
| `version.json` inside the jar | vanilla, Paper, Purpur, anything on the bundler or Paperclip |
| the NeoForge version in `libraries/` | NeoForge (21.4.157 means 1.21.4) |
| `version_history.json` | Paper |
| `logs/latest.log` | anything that has run at least once |
| the jar's file name | a jar nobody has renamed |
| **running the jar** | last resort — see below |

The loader is identified the same way, from the jar's `Main-Class`, so a
renamed jar is still recognised:

```
$ mcmgr init
Inspecting /srv/minecraft
  loader      paper       from totally-unhelpful-name.jar (manifest)
  minecraft   1.21.4      from totally-unhelpful-name.jar (version.json)
```

When none of that works — a repackaged or unusual jar with no metadata at all —
mcmgr can ask the server itself, by starting it and reading its startup banner:

```bash
mcmgr init --probe        # or just answer yes when asked
mcmgr init --no-probe     # never do this
```

The jar is started in a **temporary directory**, never the server directory, so
the world, configuration and logs are untouched and everything the probed copy
writes is thrown away. Two things stop it running long: no `eula.txt` exists in
that temporary directory, so the server prints its version and refuses to start;
and the process is killed the moment the version appears. In practice that is
under a second for vanilla and Paper, and a few seconds for Fabric.

It is only offered when nothing cheaper worked, and never runs unasked in a
script — `--probe` is required there.

`mcmgr server status` re-runs the static detection (never the probe, so status
stays instant) and warns when `mcmgr.toml` disagrees with what is actually on
disk — a mismatch quietly breaks mod compatibility otherwise.

## Supported loaders

| Loader     | `mcmgr new` installs it | Add-ons go to |
| ---------- | ----------------------- | ------------- |
| `vanilla`  | yes                     | — (no mods)   |
| `fabric`   | yes                     | `mods/`       |
| `paper`    | yes                     | `plugins/`    |
| `neoforge` | yes (runs its installer)| `mods/`       |
| `quilt`    | no — install it yourself, then `mcmgr server set-jar <jar>` | `mods/` |

## Commands

### Getting started

| Command | What it does |
| --- | --- |
| `new [name] [dir]` | Create a server, asking for anything not given |
| `init [dir]` | Adopt a server that already exists |
| `doctor` | Check a server is ready to start, and say what is not |
| `upgrade [version]` | Move to a new Minecraft version and refetch every mod |
| `venv` | Manage the Java runtime a server uses |

`mcmgr doctor` runs every check a start makes — the manifest, the server jar,
Java and its version, the EULA, whether the manifest still matches what is on
disk, whether the port is free, and whether `mods/` matches the lockfile — and
reports all of them instead of stopping at the first, because fixing one
otherwise only reveals the next. Each line ends in the command that fixes it,
and the exit status is non-zero if anything failed, so it can gate a deploy:

```
$ mcmgr doctor
survival  (stopped)
  ok    config   /srv/minecraft/mcmgr.toml
  fail  server   server jar fabric-server-launch.jar is missing
                 run: mcmgr server install
  ok    java     Java 21 at /opt/jdk21/bin/java
  fail  eula     not accepted; the server refuses to start until it is
                 run: mcmgr server accept-eula
  ok    version  1.21.4 (fabric)
  ok    port     25565 is free
  ok    mods     12 installed, all matching mcmgr.lock.json

error: 2 checks failed; see above
```

### Java

Minecraft raises its Java requirement every few releases. When the Java on your
PATH is too old, the server dies with an `UnsupportedClassVersionError` that
mentions neither Minecraft nor any version number you would recognise:

```
class file version 69.0 ... only recognizes class file versions up to 65.0
```

That is "needs Java 25, got Java 21". mcmgr checks before starting instead:

```
$ mcmgr server launch
error: Minecraft 26.2 needs Java 25, but C:\...\jdk-21\bin\java.exe is Java 21
       install the right one with:  mcmgr venv install
       Minecraft will not start on an older Java; there is no compatibility mode
```

In a terminal it offers to fix it; in a script it stops with that message. The
requirement comes from Mojang's own metadata for the version and is recorded in
`mcmgr.toml`, so the check itself needs no network.

```bash
mcmgr venv                 # what Java does this server use, and is it new enough?
mcmgr venv install         # fetch the release this server needs and use it
mcmgr venv install 21 -g   # make Java 21 the machine-wide default
mcmgr venv use /opt/jdk25/bin/java
mcmgr venv list            # runtimes already downloaded
mcmgr venv remove 21
```

Runtimes are Eclipse Temurin unless you ask for GraalVM, and are unpacked into
mcmgr's cache — **nothing is installed system-wide** and your existing Java is
untouched. They are shared between servers, since a JRE is the same hundred
megabytes for every server that wants it. `--global` changes the scope of the
*setting*, not the download: without it the runtime is recorded for this
server in `java.path`, with it as `global.java_path` for servers that name
none.

#### GraalVM

Oracle GraalVM is the same JDK with the Graal JIT compiler in place of C2,
which some servers run measurably faster on. It is free to use under Oracle's
GraalVM Free Terms and Conditions, and published for Java 17 and newer only,
which covers every Minecraft since 1.18.

```bash
mcmgr new smp --graalvm                              # download it during setup
mcmgr venv install --graalvm                         # or for an existing server
mcmgr config set global.java_distribution graalvm    # and whenever one is needed
mcmgr venv list                                      # both kinds, side by side
mcmgr venv remove graalvm-21
```

In a terminal, `mcmgr new` asks which Java the server should run on: the one
already on the machine when it is new enough, Temurin, or GraalVM, with
anything already downloaded marked as such. `--java-dist temurin` or
`--graalvm` answers without a prompt, `--java <path>` names a JDK you have,
and a script that passes neither gets the old behaviour: java from `PATH`, and
an offer to download at the first launch if that turns out too old. That offer,
like `mcmgr venv install` with no flag, follows `global.java_distribution`.

If a server does fail anyway, mcmgr reads its log and says why:

```
$ mcmgr server run
...
warning: this server needs Java 25, but it was started with Java 21
       install the right one with:  mcmgr venv install 25
       (the "class file version" in the error above is Java 25)
```

The same applies to an unaccepted EULA, a port already in use, and running out
of heap.

### Garbage collector

`mcmgr new` puts a server on **ZGC** unless told otherwise, and `mcmgr doctor`
points out one that is not. Minecraft is latency-bound: a tick has 50 ms, and
a pause from the JVM's default collector (G1) on a multi-gigabyte heap is felt
as lag by every player at once. ZGC keeps pauses under a millisecond at any
heap size, for a little throughput a server does not miss.

The manifest records only the choice, as `gc = "zgc"` under `[java]`. The
flags that make it happen depend on the Java release, and the Java under a
server can change (`mcmgr venv install`), so they are built at every launch
for the Java that actually runs; `mcmgr server status` shows the result:

| Java | Flags |
| --- | --- |
| 8–14 | none; ZGC does not exist there, and `mcmgr new` skips the question |
| 15–20 | `-XX:+UseZGC` |
| 21–22 | `-XX:+UseZGC -XX:+ZGenerational` (generational mode is opt-in here) |
| 23+ | `-XX:+UseZGC` (generational is the default, and the only mode from 24) |

To keep the JVM default pass `--zgc=false`. A collector named in `java.args`
or with `--java-arg` (say `-XX:+UseG1GC`) always wins over `gc`, which makes
`mcmgr server run --java-arg -XX:+UseG1GC` a way to compare for one run. An
existing server switches with a setting, from the next start:

```bash
mcmgr config set java.gc zgc
```

### Servers

The everyday ones also work without the `server` prefix: `mcmgr stop` is
`mcmgr server stop`.

| Command | What it does |
| --- | --- |
| `server run` | Console in this terminal; Ctrl-C stops it cleanly, Ctrl-D detaches |
| `server launch` | Start in the background, detached from this shell |
| `server attach` | Stream the console and send it commands; Ctrl-C or Ctrl-D detaches |
| `server stop` | Send `stop` to the console and wait; only a server still running after `stop_timeout_secs` is killed |
| `server restart` | Stop, then start again in the background |
| `server kill` | Terminate immediately, without saving |
| `server status` | Configuration and current state |
| `server logs [-n N] [-f]` | Captured console output; works when stopped too |
| `server cmd <command>...` | Run one console command and print the reply |
| `server install` | Download or repair the server jar |
| `server list` | Every registered server and whether it is running |
| `server use <name>` | Set the default server |
| `server forget <name>` | Drop from the registry, leaving files alone |
| `server accept-eula` | Record acceptance of the Minecraft EULA |
| `server set-jar <path>` | Point at a jar you installed yourself |

`server run` starts the same detached supervisor as `launch` and attaches to
it, so Ctrl-D hands the server off to the background and closing the terminal
does not kill it. For a service manager such as systemd, which needs the
process to stay in the foreground, pass `server run --foreground`; that is also
what happens automatically when stdin is not a terminal.

### Mods

`mcmgr plugin` is an alias, for Paper servers.

| Command | What it does |
| --- | --- |
| `mod add <mod>...` | Install mods and their required dependencies |
| `mod remove <mod>...` | Uninstall, and drop dependencies nothing needs any more |
| `mod list [--outdated]` | Show installed mods, optionally checking for updates |
| `mod update [mod]...` | Update to the newest compatible versions (`--dry-run` to preview) |
| `mod sync [--prune]` | Make the mods directory match `mcmgr.toml` |
| `mod search <query>` | Search, filtered to this server's version and loader |
| `mod recommend` | Pick from a curated list of server-side mods or plugins, then search for more |
| `mod info <mod>` | Details and recent versions |

### Recommended mods and plugins

Knowing which mods a server *should* have is the part a newcomer cannot search
for, so mcmgr carries a short, curated list per platform. `mcmgr new` offers it
as a tick list once the platform is chosen, and `mcmgr mod recommend` does the
same for a server that already exists:

```
Recommended plugins
  Well-known server-side plugins worth having from the start. The ticked ones
  change no gameplay and are safe on any server.

  > [x] LuckPerms          permissions; most other plugins expect one to be installed
    [x] EssentialsX        homes, warps, kits, /tp and the other everyday commands
    [x] CoreProtect        logs every block change so griefing can be rolled back
    [ ] VaultUnlocked      the bridge plugins use to share economy and permissions
    [ ] WorldGuard         protected regions and per-area rules
    [ ] GriefPrevention    land claims players manage themselves
    [ ] Multiverse-Core    several worlds on one server
    [ ] PlaceholderAPI     the placeholders other plugins use in chat, tab and scoreboards
    [ ] DiscordSRV         bridges in-game chat with a Discord channel
    [ ] GrimAC             anti-cheat
    [ ] WorldEdit          in-game building and terrain tools
    [ ] TabTPS             TPS and tick time in the tab list
    ...
```

Every entry is **strictly server-side**: its author marks it as running on the
server and as not needing every player to install it too. Mods that need a
client half, however popular, are left out, because a server-side list is for
things that work for whoever connects. The test suite re-checks each entry
against Modrinth.

The picks follow what the community lists for server operators agree on: the
Fabric wiki's established server-side mods, the fabric-serverside-mods lists,
and the Paper and NeoForge server guides.

| Platform | Pre-ticked | Also offered |
| --- | --- | --- |
| Fabric, Quilt | Fabric API, Lithium, FerriteCore, Krypton, ModernFix | Noisium, Alternate Current, ServerCore, Essential Commands, Ledger, Flan, Universal Graves, WorldEdit, LuckPerms, spark, TabTPS, Chunky, BlueMap, ViaVersion, Geyser |
| NeoForge | Lithium, FerriteCore, ModernFix | Noisium, Alternate Current, ServerCore, AI Improvements, Flan, WorldEdit, LuckPerms, spark, TabTPS, Chunky, BlueMap, Geyser |
| Paper | LuckPerms, EssentialsX, CoreProtect | VaultUnlocked, WorldGuard, GriefPrevention, Multiverse-Core, PlaceholderAPI, DiscordSRV, GrimAC, WorldEdit, TabTPS, Chunky, BlueMap, ViaVersion, Geyser |

Nothing pre-ticked changes gameplay. spark is not offered on Paper because
recent Paper builds bundle it.

After the list, the same flow searches Modrinth for anything it does not
cover, one query at a time, filtered to the server's version and loader. Pick
a result or "none of these", and leave the query empty to finish. Results that
are not strictly server-side — client-only, or needing every player to install
them — are hidden rather than offered and then refused; `mod search` shows the
same projects with a marker instead.

Add-ons that cannot be installed are reported by name and skipped, so one of
them never costs the rest. The usual reason is a mod with no build for that
Minecraft version, and that case gets an offer instead of a dead end; see
below.

### A mod with no build for your version

When a mod has no build for the server's Minecraft version, mcmgr looks up the
versions it does support and says so, naming the nearest one:

```
$ mcmgr mod add modernfix
error: ModernFix has no release for fabric 1.21.2
       it does have builds for 1.20.4, 1.20.5, 1.20.6, 1.21, 1.21.1, 1.21.4
       move this server with:  mcmgr upgrade 1.21.4   (then re-run this command)
```

In a terminal it offers to make that move on the spot. The move is the same as
`mcmgr upgrade`: the plan for every other installed add-on is shown, the world
warning applies, and nothing changes until you confirm. Once the server is on
the new version the install is retried there. This works from `mod add`,
`mod recommend` and `mcmgr new` alike.

```
$ mcmgr mod add modernfix
ModernFix has no build for fabric 1.21.2, but it does for 1.20.4, 1.20.5, 1.20.6, 1.21, 1.21.1, 1.21.4
-> checking which add-ons have a build for 1.21.4
mid: Minecraft 1.21.2 -> 1.21.4
NAME     INSTALLED               AFTER UPGRADE           STATUS
lithium  mc1.21.3-0.14.6-fabric  mc1.21.4-0.15.3-fabric  will update
  1 ready
Upgrade mid from 1.21.2 to 1.21.4?  [Yes / No]
-> installing the fabric server for 1.21.4
-> refetching add-ons for 1.21.4
+ Lithium mc1.21.3-0.14.6-fabric -> mc1.21.4-0.15.3-fabric
+ mid moved from 1.21.2 to 1.21.4
-> installing ModernFix 5.20.3+mc1.21.4
+ done; run 'mcmgr server restart' to load the changes
```

The nearest **newer** version is preferred, because a world can always be
brought forward. A downgrade is offered only while the server has no world
yet: a world already played on a newer version does not load on an older one,
so in that case the versions are listed and the choice is left to you.

Scripts get the same without a prompt:

```bash
mcmgr new smp --loader fabric --recommended --mod chunky --no-input   # the pre-ticked set, plus chunky
mcmgr mod recommend --list                 # print the list for this server
mcmgr mod recommend --install              # install the pre-ticked set
mcmgr mod recommend --install --all        # install everything on the list
```

### Upgrading

`mcmgr upgrade [version]` moves a server to a new Minecraft version: it
reinstalls the server binary and refetches every add-on at a build that matches.
With no version it goes to the latest release.

The plan is worked out and shown first, because the question worth answering
before an upgrade is whether your mods survive it:

```
$ mcmgr upgrade 1.21.5 --dry-run

srv: Minecraft 1.21.4 -> 1.21.5
NAME        INSTALLED               AFTER UPGRADE           STATUS
fabric-api  0.119.4+1.21.4          0.128.2+1.21.5          will update
lithium     mc1.21.4-0.15.3-fabric  mc1.21.5-0.16.3-fabric  will update
sodium      mc1.21.4-0.6.13-fabric  -                       beta only

  2 ready, 1 with no build for 1.21.5
  beta builds exist for some of these; --allow-beta would use them
  set defaults.allow_beta to make that the default
```

"no build yet" and "beta only" are kept apart deliberately: the first means
waiting for the author, the second means enabling that channel.

A mod with no compatible build is **removed from the mods directory** — one
built for the old version usually stops the server starting — but stays in
`mcmgr.toml`, so `mcmgr mod sync` picks it up once its author catches up.
Pinned mods are never moved. The server binary is installed before the manifest
is written, so a failure there leaves the server running its old version.

### Client-only mods are refused

Modrinth records whether a mod works server-side. One that does not is refused
rather than installed, because it does nothing on a server and can stop it
starting:

```
$ mcmgr mod add sodium
error: Sodium is a client-only mod
       its author marks it as unsupported on servers, so putting it in mods/
       does nothing at best, and can stop the server starting at worst
       install it in your own client's mods folder instead
       or, if you are sure it belongs here: mcmgr mod add sodium --allow-client-only
```

`mod search` marks such projects, `mod list` flags any already installed, and a
*required dependency* that is client-only is installed with a warning rather
than refused — declining it would break the mod that asked for it. CurseForge
does not publish this distinction, so the check only applies to Modrinth.

The opposite case is marked too: a mod whose author says every player must
install it as well shows as *needs every player to install it* in `mod search`.
It is not refused, since it does run on a server, but it is not server-side
either, and the guided flows in `mcmgr new` and `mod recommend` leave it out.

A mod can be named several ways:

```bash
mcmgr mod add sodium                              # slug
mcmgr mod add curseforge:jei                      # provider-qualified
mcmgr mod add https://modrinth.com/mod/lithium    # project URL
mcmgr mod add "performance optimization"          # search term; best match wins
mcmgr mod add sodium --version mc1.21.4-0.6.13-fabric   # pin an exact version
```

Pinned mods are skipped by `mod update` and reported as pinned by `mod list`.

### Settings

| Command | What it does |
| --- | --- |
| `config` | Edit settings in place |
| `config get <key>` | Print one value |
| `config set <key> <value>` | Change one value |
| `config list [--help-text]` | Show everything and where it lives |

`mcmgr config` opens one list and edits it where it sits — no submenu per
setting:

```
Settings for survival   1 unsaved change(s)

  mcmgr.toml
    defaults.allow_beta             true
    loader                          fabric
    memory                          4G
  server.properties
    properties.difficulty           easy
    properties.motd                 A Minecraft Server
  > properties.white-list           true    ← →

  caution: Only whitelisted players will be able to join
    That includes you. Add yourself before or right after the next start:
      running server:  mcmgr server cmd whitelist add <your-username>

  ↑↓ move   ←→ change   type/backspace edit   ctrl+r revert   enter save   esc cancel
```

| Key | What it does |
| --- | --- |
| `←` `→` | Cycle anything with a fixed set of values: true/false, loader, difficulty |
| typing | Edits free text straight into the row; on a fixed set it jumps to the matching option |
| `backspace` | Deletes a character; on a fixed set, steps back a value |
| `ctrl+u` / `ctrl+r` | Clear the field / revert that row |
| `enter` / `esc` | Save everything / discard everything |

Consequences appear as you change a value, not after saving, and an invalid
value blocks the save and jumps the cursor to it. `server.properties` rows are
locked while the server is running, for the reason above.

Under `MCMGR_ACCESSIBLE=1` this falls back to a prompt-per-setting flow that
works with a screen reader and over a pipe.

One keyspace covers all three places settings live:

```bash
mcmgr config set memory 6G                  # mcmgr.toml
mcmgr config set whitelist true             # server.properties
mcmgr config set global.curseforge_key ...  # machine-wide
```

### Options you always pass can be settings instead

The flags people retype have a home in `[defaults]`, and a flag overrides the
setting only when it is actually given — so `--allow-beta=false` still works
against a configured `true`:

```toml
[defaults]
provider = "modrinth"        # --provider
allow_beta = true            # --allow-beta
allow_alpha = false          # --allow-alpha
allow_client_only = false    # --allow-client-only
prune_unmanaged = false      # mod sync --prune
```

```bash
mcmgr config set beta true            # bare names work: beta, alpha, provider, prune
mcmgr config set defaults.provider curseforge
```

Three machine-wide ones pre-fill `mcmgr new`, for people who always reach for
the same choices:

```bash
mcmgr config set global.default_loader fabric
mcmgr config set global.default_memory 4G
mcmgr config set global.java_distribution graalvm   # what 'venv install' downloads
```

### Release channels

Beta and alpha are **independent** switches, not a ladder — a server can take
betas without alphas. Full releases are always eligible.

```bash
mcmgr mod add sodium --allow-beta      # this once
mcmgr config set beta true             # from now on
mcmgr mod add sodium --allow-beta=false  # ignore the setting this once
```

`--allow-unstable` remains a shorthand for both. Opting into a channel means
wanting builds from it, so an enabled beta that is newer than the latest full
release is chosen over it.

Errors name the channel that is actually blocking, and the setting that would
unblock it for good:

```
$ mcmgr mod add sodium
error: Sodium: only beta builds are available for fabric 1.21.5
       pass --allow-beta to install one, or set defaults.allow_beta in mcmgr.toml to stop asking
```

Server settings can be written `properties.white-list` in full, but the bare
Minecraft name works too, and common spellings are accepted (`whitelist`,
`port`, `online-mode`). `mcmgr config list` shows every key grouped by file.

## Shell completion

`mcmgr completion <shell>` prints a completion script generated from the command
tree itself, so it offers exactly the commands, aliases and flags this binary
has. The script is static: completing a word never runs mcmgr, so it costs
nothing and works with no server configured.

```bash
mcmgr completion bash > ~/.local/share/bash-completion/completions/mcmgr
mcmgr completion zsh > "${fpath[1]}/_mcmgr"
mcmgr completion fish > ~/.config/fish/completions/mcmgr.fish
mcmgr completion powershell | Out-String | Invoke-Expression   # add to $PROFILE
```

Then start a new shell.

## Guided setup

`mcmgr new` and `mcmgr init` prompt only for what they still need, so flags and
prompts mix freely: pass `--loader fabric` and you will not be asked about the
loader. Navigate with the arrow keys, type to fill in text, press enter to
accept.

`mcmgr new` asks, in order: name, directory, platform, Minecraft version,
memory, which Java to run on (the one already installed, Temurin or GraalVM;
see [Java](#java)), whether to run on ZGC (recommended, see [Garbage
collector](#garbage-collector)), port, whitelist (on by default), online mode,
the EULA, and then which recommended mods or plugins to install and whether to
search for more. Passing `--recommended` or `--mod` answers the last of those
without a prompt.

To run either unattended, pass every value as a flag, or add `--no-input` to
turn a missing value into an error rather than a prompt. Prompting is also
skipped automatically when stdin is not a terminal, so scripts never hang.

Set `MCMGR_ACCESSIBLE=1` for plain line-based prompts instead of the
full-screen ones — for screen readers, terminals without cursor addressing, and
for driving the prompts from a script.

## Warnings

Settings that can lock you out or damage a world say so before they apply:

```
$ mcmgr config set online-mode false

warning: Turning off online-mode lets anyone join as any username
  Player identities are no longer verified against Mojang, so:
    - anyone can connect using an operator's username and inherit their permissions
    - player UUIDs change, which can detach existing inventories, homes and
      permissions from their owners
    - the whitelist stops being a real access control, since names can be faked
  Only do this on a private network, or behind a proxy that authenticates players
  itself. Do not expose an offline-mode server to the internet.

error: this change needs confirmation, but there is no terminal to ask on
       re-run with --yes if you are sure
```

Warnings cover, among others: enabling the whitelist (you are not on it yet),
disabling the whitelist (anyone who finds the address can join), disabling
player verification, enabling RCON, hardcore mode, changing a world's Minecraft
version or loader, changing the seed or world name of an existing world, and
shutdown timeouts too short to finish a save. Severe ones require a
confirmation, or `--yes` in a script; the interactive editor names each setting
the confirmation is about.

Invalid values are refused where they are typed: a port outside 1-65535, a view
distance the server would clamp, text where a number belongs. In the editor the
value turns red with the reason underneath, and enter will not save until it is
fixed.

The Minecraft EULA is never accepted implicitly: `mcmgr new` asks, and
`mcmgr server accept-eula` is the only other way to record it.

## Mods are locked while the server runs

Replacing jars under a running JVM is a good way to corrupt a world or end up
with a mod list that does not match what is actually loaded. So `mod add`,
`mod remove`, `mod update`, `mod sync` and `server install` all check first:

```
$ mcmgr mod add create
error: server "survival" is running (pid 4812, up 3h 12m)
       stop it first:  mcmgr server stop
       or override at your own risk with --force (changes apply on next restart)
```

`server.properties` is protected the same way, for a different reason: the
server rewrites that file when it stops, so an edit made while it is running
would be silently discarded.

A server counts as running only if the recorded supervisor process still exists
*and* its control port answers with the right token, so a crashed server leaves
no stale lock — the next command cleans it up automatically.

## Which server a command acts on

In order:

1. `--server <name>`, naming a registered server or a path to one
2. the nearest `mcmgr.toml` at or above the working directory
3. the default server from the registry (`mcmgr server use <name>`)

A server deleted by hand leaves its registration behind, and would otherwise
make every command run outside a server directory fail on the same missing
file. The registry repairs itself instead: the entry is forgotten, the default
moves to another registered server, and a `global.java_path` whose binary went
with the deleted directory is unset so the other servers can still start. The
command that found the problem reports what was fixed and stops there, rather
than quietly acting on whichever server became the default, so a `stop` meant
for one server never lands on another. `mcmgr server forget` does the same
tidying, and `mcmgr server list` marks a directory that no longer holds a
server as `gone`.

## Files

| File | Where | What |
| --- | --- | --- |
| `mcmgr.toml` | server directory | The manifest: version, loader, JVM settings, wanted mods. Edit it by hand and run `mcmgr mod sync`. |
| `mcmgr.lock.json` | server directory | Exactly which mod versions are installed, with checksums. Generated, never hand-edited. Commit it for reproducible installs. |
| `server.properties` | server directory | Minecraft's own settings. mcmgr preserves its comments and key order. |
| `.mcmgr/` | server directory | Runtime state and captured console logs. Not worth committing. |
| `.mcmgr.toml.lock` and friends | next to each file above | Empty lock files that serialise concurrent mcmgr invocations. Left in place on purpose; safe to ignore and not worth committing. |
| `config.toml`, `servers.json` | user config dir | Machine-wide settings and the registry of known servers. |

A manifest looks like this:

```toml
# mcmgr server manifest. Safe to edit by hand.
# After changing the mods list, run: mcmgr mod sync

name = "survival"
minecraft = "1.21.4"
loader = "fabric"
loader_version = "0.19.5"
jar = "fabric-server-launch.jar"
server_args = ["nogui"]
stop_timeout_secs = 90
restart_on_crash = false
idle_stop = false

[java]
args = ["-Xmx4G"]
gc = "zgc"

[defaults]
allow_beta = false

[[mods]]
id = "fabric-api"
provider = "modrinth"
version = "*"
name = "Fabric API"
```

Committing `mcmgr.toml` and `mcmgr.lock.json` and running `mcmgr mod sync` on
another machine reproduces the same mod set exactly.

A server still holding the older `mcmgr.json` is converted to TOML the first
time mcmgr reads it; the original is kept as `mcmgr.json.bak`.

## CurseForge

Modrinth works with no setup. CurseForge requires a personal API key from
<https://console.curseforge.com/>:

```bash
mcmgr config set global.curseforge_key <key>
```

or set `MCMGR_CURSEFORGE_API_KEY` in the environment. Search covers both
providers with `mcmgr mod search <query> --provider all`.

## How the background server works

`server launch` (and `server run`, unless `--foreground` is given) re-executes
mcmgr as a detached **supervisor** process that owns the JVM's stdin and stdout
and listens on a loopback TCP port. Every other mcmgr invocation, including the
console `server run` shows, is a short-lived client of that port. That is why
Ctrl-D can detach from it and closing the terminal does not take the server
with it.

That is what makes `attach`, `stop` and the running check behave identically
everywhere: there is no reliance on job control, named pipes, screen or tmux.
The port is bound to `127.0.0.1` only, and requests carry a random token stored
in `.mcmgr/state.json` with owner-only permissions, so other local users cannot
drive the console.

Console output is captured to `.mcmgr/console.log`, which is why `server logs`
works on a stopped server, and the previous run is kept as `console.log.1`.

### Stopping is always the `stop` command

The JVM is never sent a signal to shut it down. `server stop`, Ctrl-C in
`server run`, and a SIGTERM or SIGHUP delivered to the supervisor (a service
manager stopping it, a `kill`, the terminal closing) all become the same thing:
the word `stop` written to the server console, followed by waiting for the
server to save and exit on its own. The JVM runs in its own process group so a
terminal's Ctrl-C cannot reach it directly, and the supervisor stays up until
the save is done.

Only a server that is still running `stop_timeout_secs` after being asked
(default 90) is killed, and `server kill` does so immediately on request. A
second Ctrl-C or signal escalates to that kill as well, for a server that is
wedged. Under systemd, set `KillMode=mixed` or `KillMode=process` so that
only the supervisor receives the stop signal.

### Restarting after a crash

Off by default, because a server that dies on startup should say so rather than
loop. Turn it on and the supervisor brings the JVM back whenever it exits for a
reason mcmgr did not ask for:

```bash
mcmgr config set restart-on-crash true
mcmgr config set restart_max_retries 5     # default 3; -1 for no limit
```

```
[mcmgr] server exited with code 1 after 3s; restarting in 5s (attempt 1)
...
[mcmgr] server crashed again after 3 restart(s); giving up
       (raise restart_max_retries, or fix what is killing it)
```

Only an unexpected exit counts. `server stop`, `server kill`, Ctrl-C and a
SIGTERM to the supervisor all mark it as stopping first, so a shutdown you asked
for is never undone — including one that arrives while a restart is waiting out
its backoff. An exit code of zero is the server doing as it was told, and is
left alone too.

Consecutive restarts back off, five seconds longer each time up to thirty, and
are capped by `restart_max_retries` so a server that cannot start is not
restarted forever. A run that lasts two minutes counts as healthy and earns the
full allowance back, so a server that falls over after a week still gets its
restarts. `mcmgr server status` shows the policy, and how many restarts the
current run has already used.

### Stopping when nobody is on

A server nobody is playing on still holds its memory and burns CPU ticking
empty chunks. With `idle_stop` on, mcmgr sits in front of it, stops it once
it has been empty for a while, and starts it again the moment a player
connects:

```bash
mcmgr config set idle_stop true
mcmgr config set idle_timeout_secs 600     # default 300
mcmgr restart
```

The supervisor takes the address in `server.properties` (`server-ip` and
`server-port`) for itself and runs the JVM on a private port behind it, passed
as `--port`, so `server.properties` keeps naming the port players use. Every
connection goes through mcmgr:

- **A player joining a sleeping server** starts it and waits on the "Joining
  world" screen until it is ready, then is let in. A server that takes longer
  than 25 seconds tells them to try again in a moment, since clients give up
  on a silent server at about thirty.
- **The server list** keeps showing a sleeping server, with its last real
  version and icon, nobody online, and a line under the MOTD saying a join
  will start it.
- **Once it is up**, everything is passed straight through, encrypted and
  compressed as usual; mcmgr only counts connections.

The server is stopped with its own `stop` command, like any other stop, once
nobody has been connected through mcmgr for `idle_timeout_secs`, and only
after asking the server itself, so a Bedrock player on Geyser keeps it up too.
The supervisor stays running, so `server stop` still shuts everything down,
`mcmgr start` wakes a sleeping server by hand, and `server status` and
`server list` show it as `asleep`. A JVM that exits for any other reason, say
a `stop` typed in the console, or a crash that `restart_on_crash` gave up on,
also goes to sleep until the next player.

Things to know:

- The server sees every player as connecting from `127.0.0.1`, so IP bans
  and per-IP logs stop meaning anything. `prevent-proxy-connections` must stay
  off, or logins fail; the console warns if it is on.
- Paper is told to listen on loopback only. Other servers bind the private
  port on `server-ip`, all interfaces if that is empty, so keep it firewalled
  like any other port you do not publish.
- Mods and plugins stay locked while the server sleeps, since a player can
  wake it at any moment. Stop it to change them.
- The protocol handling comes from
  [go-mc](https://github.com/Tnze/go-mc).

## Environment variables

| Variable | Effect |
| --- | --- |
| `MCMGR_HOME` | Override the config directory (registry, global settings) |
| `MCMGR_CACHE` | Override the cache directory |
| `MCMGR_CURSEFORGE_API_KEY` | CurseForge API key |
| `MCMGR_ACCESSIBLE` | Use plain line-based prompts |
| `MCMGR_NO_INPUT` | Never prompt; fail instead |
| `NO_COLOR` | Disable coloured output |

## Notes and limits

- Minecraft servers need a JVM. mcmgr uses `java` from `PATH` unless
  `java.path` is set in the manifest, `global.java_path` is set, or a runtime
  was installed with `mcmgr venv install`.
- `server kill` terminates the supervisor and the JVM. A SIGTERM to the
  supervisor is a clean stop, not a kill; only a SIGKILL takes it down without
  stopping the server, in which case the JVM outlives it. The next mcmgr
  command detects the stale state and `server kill` cleans up the leftover
  process.
- Quilt servers are managed for mods but must be installed with Quilt's own
  installer first.
- `mcmgr init` identifies existing mods against Modrinth only. CurseForge has
  no equivalent public hash lookup, so mods from there stay unmanaged until
  added by name.
- Some CurseForge authors disable third-party API downloads. mcmgr falls back
  to the CDN path, which does not always work; those mods must be installed by
  hand.

## Dependencies

Three direct dependencies, all for things worth not hand-rolling:

- `github.com/BurntSushi/toml` — full-spec TOML, so a hand-edited `mcmgr.toml`
  parses the way the user expects
- `github.com/charmbracelet/huh` — the prompts, including raw terminal handling
  across Windows consoles and Unix terminals (this pulls in bubbletea and
  lipgloss, which is most of the binary size)
- `github.com/Tnze/go-mc` — the Minecraft protocol, for the handshakes and
  server-list pings `idle_stop` reads and answers

Everything else — HTTP, JSON, process supervision, the control protocol — is
the standard library. Startup stays around 35 ms because none of the prompt
machinery runs unless a prompt is actually shown.

## Tests

```bash
go test ./...
```

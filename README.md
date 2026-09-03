# mcmgr

A fast, cross-platform Minecraft server and mod manager in a single binary.

Create a server, install mods from Modrinth or CurseForge, run it in the
background, attach to its console, and stop it — from one command, on Windows,
macOS and Linux.

```
mcmgr new                    # asks for anything it needs
mcmgr mod add fabric-api sodium lithium
mcmgr start                  # in the background
mcmgr attach                 # open its console; Ctrl-D detaches
```

- **Guided when you want it, scriptable when you don't.** `mcmgr new` asks for
  whatever you did not pass as a flag; supply every flag and it runs unattended.
- **Actually cross-platform.** The same commands, the same config file and the
  same background-server behaviour on all three operating systems.
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
# Create a server. Anything you leave out is asked for.
mcmgr new survival --loader fabric --mc 1.21.4 --memory 4G

# Install mods. Required dependencies come along automatically.
mcmgr mod add -s survival fabric-api sodium lithium

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
| `upgrade [version]` | Move to a new Minecraft version and refetch every mod |
| `venv` | Manage the Java runtime a server uses |

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

Runtimes are Eclipse Temurin, unpacked into mcmgr's cache — **nothing is
installed system-wide** and your existing Java is untouched. They are shared
between servers, since a JRE is the same hundred megabytes for every server
that wants it. `--global` changes the scope of the *setting*, not the download:
without it the runtime is recorded for this server in `java.path`, with it as
`global.java_path` for servers that name none.

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
| `mod info <mod>` | Details and recent versions |

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

Two machine-wide ones pre-fill `mcmgr new`, for people who always reach for the
same choices:

```bash
mcmgr config set global.default_loader fabric
mcmgr config set global.default_memory 4G
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

## Guided setup

`mcmgr new` and `mcmgr init` prompt only for what they still need, so flags and
prompts mix freely: pass `--loader fabric` and you will not be asked about the
loader. Navigate with the arrow keys, type to fill in text, press enter to
accept.

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

## Files

| File | Where | What |
| --- | --- | --- |
| `mcmgr.toml` | server directory | The manifest: version, loader, JVM settings, wanted mods. Edit it by hand and run `mcmgr mod sync`. |
| `mcmgr.lock.json` | server directory | Exactly which mod versions are installed, with checksums. Generated, never hand-edited. Commit it for reproducible installs. |
| `server.properties` | server directory | Minecraft's own settings. mcmgr preserves its comments and key order. |
| `.mcmgr/` | server directory | Runtime state and captured console logs. Not worth committing. |
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

[java]
args = ["-Xmx4G"]

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

Two direct dependencies, both for things worth not hand-rolling:

- `github.com/BurntSushi/toml` — full-spec TOML, so a hand-edited `mcmgr.toml`
  parses the way the user expects
- `github.com/charmbracelet/huh` — the prompts, including raw terminal handling
  across Windows consoles and Unix terminals (this pulls in bubbletea and
  lipgloss, which is most of the binary size)

Everything else — HTTP, JSON, process supervision, the control protocol — is
the standard library. Startup stays around 35 ms because none of the prompt
machinery runs unless a prompt is actually shown.

## Tests

```bash
go test ./...
```

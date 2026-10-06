# Pier

**Start, stop and watch every local project service with one command.**

[中文](README.md) | English

Pier keeps a manifest of your services — which directory, what kind of project, how to start it,
which port it takes — and gives both you and your AI coding agent one lightweight entry point to
bring them up, read their logs and stop them. A Go CLI called `pier`, plus a GUI (one build each
for macOS, Windows and Linux). No runtime dependencies.

![Pier main window](docs/images/overview.png)

## Name and mark

**Pier** — the platform jutting out over the water: boats pull up, unload, and leave again. The
services that dock here are your local projects, and you can see at a glance which ones are still
running. But a pier is not part of the boats, and your services are not part of Pier — they run in
their own sessions, so closing Pier or the terminal leaves them running and still writing logs.

The icon is that pier: a deck, two piles under it, a waterline, and the rightmost pile rising past
the deck as a lamp post with a **green light** on top — the same green as the "service is running"
dot in the app. The background is a vertical gradient of the primary colour (`#3F6BFF` → `#1433D6`).
Deliberately not the letter P: a white P on blue reads as a parking sign.

The icon is **drawn in code** ([`tools/mkicon`](tools/mkicon)) rather than shipped as a bitmap, so
it can follow the palette; a bitmap would need a design tool, and a binary blob in the source hides
what changed. The sidebar mark (`Mark` in `gui/app.js`) is the same geometry in the same 200×200
viewBox — change one and you change both.

## Why

A growing share of code is written by AI, and every change wants a restart to be seen. Opening an
IDE for that is heavy; retyping a long start command every time is annoying; and when an agent
restarts services over and over, the IDE is overkill — all it wanted was "run this service again".

Real projects are rarely a single service either: a few backend modules, a dev server, a couple of
simulators — one wanting JDK 21, another JDK 8, all needing distinct ports. That information
already exists in your IDE run configurations, but it only lives inside the IDE.

Pier takes it out and turns it into a manifest plus a small always-available entry point:

- **For people**: double-click the app for a panel with start/stop, logs, and who is holding a port.
- **For agents**: `pier up <name>` and `pier logs <name>` are enough — no need to learn how the
  project boots first.

Services run as **independent processes**, not as children of Pier. Quit Pier, quit the terminal —
they keep running and keep writing logs. The next time Pier opens, it reclaims them by PID /
process group.

## Features

### Processes

- `pier up`, `pier down`, `pier restart`, with or without service names.
- Stop works at any stage: queued starts are cancelled, compiling processes are killed as a group,
  health waits are interrupted. An interrupted start never writes its state back.
- Services start in their own session (`setsid`), and the log file descriptor is inherited by the
  child — **quitting Pier never takes running services down**.
- Services can declare `depends_on` and `restart`: start follows the dependency order, stop reverses
  it, and a dependency cycle is reported while loading the manifest instead of being left to the
  sort; `restart: on-failure` means "it disappeared without going through Stop, so bring it back",
  capped at three times in ten minutes, with the panel saying how many restarts there have been.
- Dependencies can carry a condition: `depends_on: [mysql:healthy]` holds this service back until
  mysql's probe passes. It still starts when that never happens — the state says
  "mysql never became ready", which never blocks anything else. Bare names only order the start.
- Readiness probes come in three forms: an HTTP address, `tcp://host:port` (for databases, caches
  and queues that have no HTTP interface), and `cmd: <command>` (exit code 0 means ready).
  A probe is a **readiness signal, not a verdict**: a service that never answers it still runs.
- Services you rarely need can be marked `manual: true` ("All start/stop → Skip" in the form):
  a bare `pier up` / `pier down`, and the panel's "Start all / Stop all", leave them alone —
  naming them or starting their group still reaches them. Manifests tend to carry a few mock,
  capture or frontend-only services you would otherwise have to pick out by hand every time.
- A service you keep editing can say `watch: true` ("Rerun on change" in the form): Pier watches its
  directory and runs an ordinary restart once changes settle (400ms). `true` picks a per-kind default
  (Go watches `*.go` and `go.mod`, Java watches `src/main/**` and `pom.xml`); you can also name a list
  of patterns yourself. Build-product directories are never watched (`node_modules`, `target`, `dist`,
  `.git`, plus Pier's own logs and bin) — otherwise a compile would drop an artifact, restart the
  service, and loop forever. Such a restart does not spend the "automatic restart" quota, and never
  starts a service that is not already running.
- Groups are not just a panel concept: `pier up @frontend` starts exactly what the frontend page
  shows (`@Ungrouped` works too). A group is an explicit list of services, so it includes the
  ones marked `manual`.
- "Who holds this port" is resolved by **process group**, not by PID alone: the port is usually
  held by a child process, and comparing PIDs alone points at the wrong thing.
- Resource usage counts only Pier and the services it started (summed per process group), never the
  whole machine.

### Ports and origin

- `pier ports` lists what is listening right now: who opened each port, in which directory, and
  whether the manifest already claims it. The one the manifest does not know about can be adopted
  in one step — building the service from the running process beats retyping it from memory.
- "Who holds this port" walks the parent chain to report the **origin**: which editor, which
  terminal, or the Pier panel itself. When it cannot tell, it leaves the line empty rather than
  pointing somewhere wrong.
- Picking a port shows the free ones and the occupied ones side by side — whether a port is taken
  is only knowable at start time, so both lists need to be visible while choosing.
- When something else already holds the port in the manifest, you can **swap it for this run only**:
  "start on another port" in the service row's ⋯ menu, or `pier up api --port 0` on the command line
  (0 picks a free one, or name a port). The swap lasts for that run and is never written back to the
  manifest — the UI keeps saying "the manifest says 8080, this run uses 8081" and the log header
  records the port actually used. Once the process holding the original port is gone, the next start
  goes back to the number in the manifest; while it is still there, a restart keeps the swapped port
  rather than running into the occupied one again — otherwise the swap would be worth nothing.

### Local HTTP API

- `pier api` serves an HTTP API on `127.0.0.1:7717`: read state, start and stop services, read logs,
  with an OpenAPI 3.1 document at `GET /openapi.json` so other tools can drive it. Print the token
  with `pier api --show-token`, replace it with `--rotate`.
- Two constraints are hard: it **binds to loopback only** (the API starts and stops processes;
  binding `0.0.0.0` would hand "what runs on this machine" to the whole LAN), and **every API call
  needs the token** — not to stop local users (they would just run `pier down`), but to stop any web
  page you happen to have open: its JS can reach `127.0.0.1`, but a cross-origin request with a
  custom header needs a preflight, and this server never answers CORS.

### MCP

- `pier mcp` speaks MCP (Model Context Protocol) over stdin/stdout for clients such as
  Claude Code: seven tools — `list_services` / `service_status` / `start_service` /
  `stop_service` / `restart_service` / `read_logs` / `wait_ready` — driving the same
  services as the UI and `pier api`.
- **stdio, no port, no token**: the process using it is already on this machine and that
  pipe is the one the client handed us; every extra port is one more thing to protect.
  In a client config it is a single command:

  ```json
  { "command": "pier", "args": ["mcp"] }
  ```

- "Did not become ready" is an **answer** from `wait_ready`, not a failed call: which
  services missed and where each one stalled (no probe configured / not running / probe
  still closed at the deadline) come back in the reply, each with the step that follows.
- A manifest that fails to load does not stop it: `list_services` reports the reason
  instead of exiting, which on the client side would only look like "cannot connect".

### Toolchains

- Service kinds: Go, Java (Maven multi-module), Node, Python, and arbitrary shell commands.
- Toolchain resolution in four steps: **per-service → project-declared → global default → whatever
  is installed**, with the reason recorded at every step (the GUI and `pier doctor` share one
  implementation). If the project asks for a version you don't have, Pier still picks a usable one
  and says so.
- Installed SDKs are discovered by scanning known install locations, not through `PATH`: an app
  launched from Finder only has `/usr/bin:/bin`, so `lookPath` would report nothing. For Go, the
  toolchain Go itself downloads into `~/go/pkg/mod/golang.org/toolchain@*` counts too.
- The "SDKs" page lets you add any directory by hand and set a global default per language.

### Environment variables

- A top-level **`env`** block in the manifest goes to every service; a service's own value for the
  same name wins. One shared database address or registry config is one place to change, with no
  service left behind pointing at the old one.
- A **`.env`** in a service's directory is injected automatically, with dotenv's usual semantics:
  fill in only what is missing (anything already in the process environment is left alone), one
  layer of matching quotes stripped, `#` starts a comment but only on its own line (a `#` inside a
  value stays). Only this one file is read — Pier does not guess between `.env.local`,
  `.env.development` and friends. A file that can't be parsed stops the start and names the file
  and line.
- Values may reference **`${DB_HOST}`** from the environment as computed so far, and **`${PORT}`**
  for the service's own port — the `PORT` injected at start is the same value a child process
  reads. Values without a `$` are never rewritten; `$${X}` means a literal `${X}`.
- **An unknown name is an error, not an empty value**: an empty string would look fine and connect
  to nothing. The start log lists which variables were injected (names only). The GUI edits the
  shared block under Preferences → Data.

### Logs

- One directory per service, one file per day: `logs/<service>/<date>.log`, kept for 14 days.
- A run crossing midnight keeps writing the same file — one run, one log.
- Files are appended to; "this run only" is provided by a start marker plus trimming.
- The GUI can browse past days, follow new output, search within the log (⌘F) with highlights,
  copy what is shown or export it to a file (the drawer only reads the tail, so what you copy is
  what you see), toggle wrapping, and render ANSI colors with the terminal palette.
- Cleaning skips services that are currently running: the log fd belongs to that independent
  process, so deleting the file would only unlink it and free the space on exit.

### When something breaks

- The tail of the log is matched against known failures: when one is recognized you get **a reason
  and a next step** ("dependencies missing" / "run the install step in this service's directory"),
  along with the original line — "failed to start" on its own would just send you digging through
  the log, and the port number or module name only exists in that line. The GUI shows it under the
  failing row; `pier up` / `pier status` print it too.
- A system notification goes out when a service **exits unexpectedly, exhausts its automatic
  restarts, or fails to come back up**, carrying the service name, the diagnosed reason, and
  "restarted automatically N times". Nothing else notifies: not updates, not "checked and
  found nothing".
- Notifications can be turned off under Preferences → General (on by default). If one can't be
  delivered, nothing happens — a revoked notification permission or a Linux box without
  `notify-send` is not a Pier failure.

### Updates

- Pier checks GitHub Releases on startup (and every 6 hours after that); a new version puts a dot
  on the sidebar row — an auto-updater's worst failure is having checked and told nobody.
- Updating is **download, then restart into it**: the app downloads and verifies the SHA-256, and
  "Restart and install" hands over to a helper that waits for Pier to exit, swaps the files and
  brings it back. A failed download, a checksum mismatch, too little disk or a read-only install
  location all refuse to install with the reason stated — never a half-applied update. The swap is
  written to `logs/update/<date>.log`, and a failure shows up in the app on the next launch.
- When the install location can't be recognised — you built Pier from source, or installed it with
  `go install` — Pier says how that copy should be upgraded instead of guessing. Guessing wrong
  once would wreck someone's install directory.
- "Skip this version" stops the prompt for that release until a newer one appears.
- The CLI does the same: `pier update --check` only reports (exit code 10 means an update is
  available, so scripts can just read the code), `pier update` downloads and swaps. While the GUI
  is running the CLI refuses to touch anything — quit the GUI first, or use "Restart and install"
  there.

### GUI

- Two sidebar sections: **Services** (all / per group) and **Settings** (SDKs, logs); below them a
  single **Preferences** row (updates, appearance and the manifest) showing the current version,
  replaced by a primary-coloured dot and the new version number when an update exists.
- Reorder services by dragging, rename them (log directory moves along), duplicate one to edit.
- The two overview tiles (services / Pier itself) each carry a recent-usage curve, one row for
  CPU and one for memory: a full-height row only means "highest in this window" — the absolute
  numbers stay in the tile.
- Groups fold on the "all services" page, so dozens of services are not one long scroll; a group
  page never folds (it holds a single group), and neither does a filtered list — search means
  "find me X", and folding the matching group away would hide it.
- Search matches name, directory, port, note and group; the filter offers "all / running /
  stopped / needs attention". Bulk start/stop follows the current page, and reopening Pier
  returns to the page you left, with the window where you left it.
  Services you seldom need can be flipped to "All start/stop → Skip" in the form; they carry a
  "skipped" tag in the list, "Start all / Stop all" leaves them alone, and the overview says how
  many there are.
- Log drawer, port occupancy (who holds it, kill it from there), and a defined way out when a
  health probe never passes.
- Preferences has three sections: general (check / download / skip, the automatic-check and
  notification switches), appearance (light / dark / follow-system), and data. The data section
  says which manifest is in hand — paste it out as YAML for `pier.yaml`, save it to a file, or
  open another manifest read-only and switch back to local data at any time — edits the shared
  environment variables, and cleans up process records that no longer match reality.

The log drawer, SDK management, preferences and the dark theme (the rest are in
[docs/images](docs/images/README.md)):

![Log drawer](docs/images/logs.png)

![SDK management](docs/images/sdk.png)

![Preferences](docs/images/settings.png)

![Dark theme](docs/images/dark.png)

### Importing existing setups

- `pier import` reads IDEA's `.idea/workspace.xml` and converts existing run configurations into
  Pier service definitions — the working directory, module and name are already correct there, and
  copying them into YAML by hand is easy to get wrong.
- `pier detect` scans a directory and reports what it finds, with suggested kinds and ports.
- `pier add` puts what it found straight into the manifest, with no copy-paste in between — run
  `pier add` inside a project and it works it out; when it cannot, `--kind` and `--run` fill the gap.

### Editing the manifest from the CLI

`add` / `edit` / `rm` / `group` write to `services.json` in the data directory, through the same
validation the GUI uses (`internal/manage`): port clashes, missing directories and deleting a
running service are all caught there, and two copies of those rules would eventually disagree.
Pointing `--config` at a YAML file is refused: by convention Pier does not rewrite a file you
wrote by hand (`pier status --config` and the other read-only commands still work).

Reopen the window to see the change: the GUI reads the manifest once, at startup.

## Tech stack

| | |
| --- | --- |
| Language | Go 1.24, compiled to a single binary with no runtime dependencies |
| CLI | Hand-drawn tables; the interactive panel uses [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) |
| GUI | The system WebView ([webview_go](https://github.com/webview/webview_go): WKWebView on macOS, WebView2 on Windows, WebKitGTK on Linux) + React 18 + [antd](https://ant.design/) 5 + [htm](https://github.com/developit/htm) |
| Frontend build | **None.** The libraries are UMD builds, inlined into a single HTML document together with the styles and application code. No npm, no bundler, no CDN (offline, proxies or a blocked CDN would all leave a blank window with no way for the user to tell why) |
| Config | YAML ([yaml.v3](https://github.com/go-yaml/yaml)); the copy the GUI edits lives in the data directory |
| Data | JSON and log files under `~/.pier/` — no database, no background daemon |
| Platforms | macOS 11+ / Windows 10+ / Linux (the GUI needs GTK3 and WebKit2GTK 4.0); `./package.sh` builds all three at once |

## Install

### CLI

```bash
git clone https://github.com/zhengshangjinx/pier.git
cd pier
go build -o pier .
./pier --help
```

Put `pier` on your `PATH` and it works from any project directory. Or install it directly:

```bash
go install github.com/zhengshangjinx/pier@latest
```

### GUI

```bash
./package.sh            # one package per platform, all of it under dist/
```

| Platform | Artifact | How to install |
| --- | --- | --- |
| macOS | `Pier-<version>-macos-universal.dmg` / `.zip` | Open the `.dmg` and drag Pier into Applications |
| Windows | `Pier-<version>-windows-amd64.zip` | Unzip and double-click `install.cmd` |
| Linux | `Pier-<version>-linux-amd64.tar.gz` / `-arm64.tar.gz` | Unzip and run `sh install.sh` |

All three end up looking the same: **one Pier in your launcher, one `pier` in your terminal**, and
none of them needs admin rights. Windows installs under `%LOCALAPPDATA%\Programs\Pier`, Linux under
`~/.local`; on macOS the `.app` *is* the install — both the GUI `pier-gui` and the CLI `pier` live
inside it, and the `/Applications` alias in the `.dmg` makes the drag the whole install.

After that you never have to come back for another archive: Pier checks for updates itself
(see [Updates](#updates)). The one exception is **0.2.0 and older** — those builds predate the
updater, so getting off one takes a manual install; there is no gap after that.

On macOS the CLI sits inside the `.app`, so one symlink gives you the command:

```bash
ln -s /Applications/Pier.app/Contents/MacOS/pier /usr/local/bin/pier   # needs write access there
```

If you would rather not touch `/usr/local/bin`, put
`alias pier=/Applications/Pier.app/Contents/MacOS/pier` in `~/.zshrc` instead.

Runtime dependencies: nothing extra on macOS; the Windows GUI needs the WebView2 runtime that ships
with Windows 11 (and usually comes along with Edge on 10); the Linux GUI needs GTK3 and WebKit2GTK
4.0. The CLI needs no graphics libraries on any of them.

For headless machines and CI, `Pier-<version>-macos-universal.tar.gz` is the CLI on its own. The
Windows and Linux archives always carry both — skip `install.cmd` / `install.sh` and run
`bin\pier.exe` directly if you prefer.

For a single local build: `./build-app.sh` produces `build/Pier.app` on macOS (the CLI is inside it
too; all it needs is Go and the Xcode command line tools — `sips`, `iconutil` and `codesign` ship
with macOS; the bundle is ad-hoc signed, which is enough for local use), and
`go build -o pier-gui ./gui` does it on Windows and Linux.

## Quick start

1. Teach Pier about your projects: open `Pier.app` and add services under "All services", or have
   the command line do it — `cd` into the project and run `pier add`, which identifies the project
   and files it away, or point it somewhere: `pier add ~/work/api`. It can also read the run
   configurations of an existing IDEA project (`pier import <project dir>`), or just tell you what
   it sees without writing anything (`pier detect <project dir>`).
2. Run them:

   ```bash
   pier up              # everything (skips the ones marked manual)
   pier up api web      # just these two
   pier up @frontend    # one group, matching its page in the panel
   pier status
   pier logs api -f
   pier down
   ```

3. To use a hand-written manifest instead (kept in your repo, committable, not in the data dir):

   ```bash
   pier status --config ./pier.yaml
   pier up --config ./pier.yaml
   ```

## Commands

| Command | Description |
| --- | --- |
| `pier doctor` | Check that each language toolchain resolves, and report which one was chosen and why |
| `pier detect [dir]` | Scan a directory, identify project types and suggest start commands |
| `pier import [dir]` | Read `.idea` run configurations and convert them into Pier services |
| `pier add [dir]` | Identify the project in a directory and add it; no directory means the current one, `--kind` / `--run` / `--port` override what it finds |
| `pier edit <service> <flag>...` | Change a service; anything you leave out stays as it was (`--port` also rewrites the port inside its probe) |
| `pier rm <service>...` | Delete services from the manifest; definitions only, not one file in the project directory |
| `pier group [add\|rename\|rm]` | List the groups, and create, rename or delete one |
| `pier up [service...]` | Start services; no name means "all" (skipping the ones marked `manual`), and an `@` prefix starts a group; `--port 0` starts the named one on another port, this run only |
| `pier down [service...]` | Stop services |
| `pier restart [service...]` | Restart services |
| `pier wait <service...>` | Wait until those services are ready (`--timeout 30s`, default 180s) |
| `pier status [--json]` | List every service and its state |
| `pier ports [--json]` | List what is listening on this machine, who started it, and from which directory |
| `pier logs <service>... [-f] [--tail N]` | Show logs, several services at once if you like; `-f` to follow (one service), `--tail` for the line count |
| `pier logs --size [--json]` | Show how much disk the logs take |
| `pier logs --clean [service] [--all]` | Remove logs older than 14 days; `--all` clears everything |
| `pier ui` | Open the interactive terminal panel |
| `pier api` | Serve a loopback-only HTTP API (`--show-token` / `--rotate` / `--port N` / `--addr <host>`) |
| `pier mcp` | Speak MCP over stdin/stdout, handing start/stop and logs to an AI client |
| `pier version` | Print the version (`pier --version` means the same) |
| `pier update --check` | Check for a newer release without installing: exit code 0 up to date, 10 update available, 1 check failed |
| `pier update` | Download, verify and swap in the latest release |

Every subcommand accepts `--config <manifest>` to use a YAML manifest instead (read-only).
It goes **after** the command: a leading `pier --config …` is read as a command that does not exist.
`-h` / `--help` works on every subcommand too (`pier logs -h`), wherever it sits.

### Machine-readable output

`--json` is accepted by `status` / `ports` / `doctor` / `logs --size`, and only there;
anywhere else it is rejected outright rather than silently ignored. The fields are the ones the
GUI reads (`StateOut` and friends in `internal/panel`), whose key names are a public contract
that only ever grows. Stdout carries nothing but JSON — progress and notices go to stderr —
so `pier status --json | jq` always works.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Done. For `pier update --check`: already up to date |
| 1 | Not done: a service failed to start / a probe never passed / a port was taken so the service was skipped / logs were not cleaned / the manifest could not be read / unknown argument |
| 2 | Unknown command (usually a typo) |
| 10 | `pier update --check` only: a newer release exists |

For `pier up`, 0 means **every service this run was asked to start is running**: services skipped because
their port was taken do not count as success and are listed in their own section at the end
(add `--port 0` for that service to start it on a free port instead). Services marked
`manual: true` are not counted — a bare `pier up` is not supposed to touch them in the first place.
`pier down` is the one exception — "not started by Pier" and "already exited" are notices with
exit code 0, since running `down` twice is supposed to be safe. `pier doctor` always exits 0:
a missing toolchain only affects the services that need it, so a script that wants to judge
should read `missing` from `pier doctor --json`.

## Manifest

```yaml
# pier.yaml
toolchain:
  java: /opt/homebrew/opt/openjdk@21   # global default per language

env:                                   # shared by every service; a service's own value wins
  DB_HOST: 127.0.0.1
  DB_PORT: "5432"

services:
  - name: api
    dir: ./server            # relative to the manifest, or absolute (no ~)
    kind: java               # go / java / node / python / shell; inferred from the directory if empty
    module: shop-admin       # Maven submodule
    port: 8080
    health: http://localhost:8080/actuator/health
    env:
      # Values may reference the environment as computed so far (the shared block, .env
      # and the toolchain are all in it) or another variable in the same block;
      # ${PORT} is this service's own port. The shared block cannot see a service's own
      # variables — it is resolved first.
      JDBC_URL: jdbc:postgresql://${DB_HOST}:${DB_PORT}/shop
      SPRING_PROFILES_ACTIVE: local
      SERVER_PORT: "${PORT}"

  - name: web
    dir: ./web
    kind: node
    script: dev              # package.json script, default "dev"
    port: 5173
    group: frontend
    depends_on: [api:healthy] # wait for api's probe; a bare name only orders the start
    restart: on-failure      # bring it back when it disappears without going through Stop
    watch: true              # rerun on change; a list narrows it to the files you name
  - name: mock
    dir: ./mock
    manual: true             # out of "all start/stop"; named or grouped starts still reach it
```

The two top-level keys apply to every service: `toolchain` sets a global default per language,
`env` holds variables shared by all services (a service's own value wins).

| Field | Meaning |
| --- | --- |
| `name` | Service name, also the argument to `up` / `down` / `logs` |
| `dir` | Working directory, relative to the manifest (no `~`) |
| `group` | Sidebar group; empty means "Ungrouped" |
| `note` | Free-form note shown in the panel |
| `kind` | `go` / `java` / `node` / `python` / `shell`; inferred when empty |
| `run` / `build` | Explicit run and build commands; inferred from the kind when empty |
| `module` | Maven submodule for Java services |
| `script` | package.json script for Node services |
| `port` | Port, used for status display and occupancy checks |
| `health` | Readiness probe, in one of three forms (below). It is a **readiness signal, not a verdict**: a service that never answers it is still running, and the state falls back with an explanation once the probe times out |
| `env` | Extra environment variables. Values may use `${NAME}` (from the environment as computed so far, including `.env` and the shared block) and `${PORT}` (this service's own port); an unknown name refuses the start instead of expanding to nothing |
| `toolchain` | Per-service toolchain override, taking precedence over the top level |
| `depends_on` | Services that must come first (a list). A bare name orders the start only: it starts first and stops last, and a service still starts when its dependency failed. `name:healthy` also waits, see below |
| `restart` | Takes one value, `on-failure`: bring the process back when it disappears without going through Stop. Empty means no automatic restart |
| `watch` | `true`, or a list of patterns (e.g. `["src/**", "pom.xml"]`): rerun on change. `true` picks a per-kind default (an unrecognised kind watches the whole directory); a list narrows it to what you name, see below |
| `manual` | True keeps it out of "all start/stop": a bare `pier up` / `pier down`, and the panel's "Start all / Stop all", skip it; naming it or starting its group still reaches it. Start and stop are symmetric — excluding only the start side would let `pier restart` stop it and never bring it back |

The three forms of `health`:

- `http://localhost:8080/actuator/health` — a GET; 2xx / 3xx means ready (`https` works the same).
- `tcp://localhost:3306` — the connection succeeds. Databases, caches, registries and queues have
  no HTTP interface, and "can it be connected to yet" is exactly what you want to know locally.
- `cmd: pg_isready -h localhost` — a command; exit code 0 means ready. `redis-cli ping` or a small
  script of your own both work. The command goes through the system shell (like `run`, so pipes and
  `&&` are fine), runs in the service's `dir`, and its output is discarded.

A condition in `depends_on` (`name:healthy`) waits for that dependency's probe before starting this
service's process, for up to three minutes. Writing a condition requires the dependency to have a
`health` (there would be nothing to probe), otherwise loading the manifest fails. **Not becoming
ready never blocks the start**: the service comes up anyway, the state says "mysql never became
ready", and `pier up` lists it in its own closing section — it is already running, and sitting
there doing nothing is harder to explain than starting and saying so. Waiting does not hold up the
pipeline either: services queued behind a waiting one still start.

`watch` looks at the service's own directory, and takes effect once it is saved back into the
manifest. `true` asks for the per-kind default; a list narrows it to what you name — **a pattern
without `/` matches at any depth** (`*.go` also matches `sub/dir/a.go`), one with `/` is relative to
the service directory, and a trailing `/` means "everything under that directory" (`src/` equals
`src/**`). Build-product and VCS directories are never watched and are not configurable:
`node_modules`, `target`, `dist`, `build`, `.git`, `__pycache__` and the like (the full list is in
`internal/watch`), together with Pier's own data directory — without those, an artifact landing in
the service directory would restart the service, which drops another artifact, forever. Only a
service that is **already running** is restarted by a file change; a stopped one is not brought up.
That restart does not spend the "automatic restart" quota (three times in ten minutes) either:
editing a file five times is normal, and sharing the ledger would let a few edits exhaust the quota
and leave a real crash unrecovered. A watch restart that fails to come up (a compile error in what
you just saved, say) still sends a notification, like any other automatic restart.

Java builds always pass `-DskipDocker=true -Ddocker.skip=true -Ddockerfile.skip=true -Djib.skip=true`
— local runs don't build images.

## Data directory

```
~/.pier/
├── services.json   services, groups and shared environment variables (what the GUI edits)
├── settings.json   UI preferences (theme, automatic update checks, skipped versions),
│                   manually added SDKs, global defaults
├── state.json      process state (PID / PGID) used to reclaim services
├── update.json     what the last check found (release notes, asset name, ETag)
├── restart.lock    exclusive lock for the auto-restart sweep, so only one host sweeps
├── restarts.json   auto-restart ledger (how often each service was rescued); survives a restart
├── gui.lock        held while the GUI runs; the CLI reads it before replacing files
├── logs/<service>/<date>.log
├── logs/update/<date>.log
└── cache/
    ├── bin/        hard links used to name Node processes
    └── update/     downloaded assets, the extracted new version, and the helper's result
```

Missing files are created empty. `PIER_HOME` relocates the whole directory (the tests always set
it, so they never touch real data).

## Development

```bash
go vet ./... && go test ./... -count=1     # checks
./build-app.sh                             # build build/Pier.app
./package.sh                               # per-platform packages under dist/
tools/shoot/shoot.py /tmp/a.png 1382 880   # render the UI to a PNG (demo data)
tools/smoke-update.sh cli                  # fake install in a temp dir, then pier update on it
```

[`AGENTS.md`](AGENTS.md) (Chinese) documents the project's conventions and the reasoning behind
them — UI rules, log rules, how process names are rewritten, and why some simpler-looking
approaches don't work. Worth reading before changing code.

## License

[MIT](LICENSE). Third-party libraries and their licenses are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

# Idle Lineage Launcher

Wails v3 + React TypeScript desktop launcher for
[shines871/idle-lineage-class](https://github.com/shines871/idle-lineage-class).
It is a launcher only: the game is not bundled in the app or embedded in an
iframe.

At startup, the launcher checks the local working tree at
`game/shines871` inside the platform application-data directory. If no valid
installation exists, it displays **尚未下載遊戲** and performs no clone or
other network operation. Downloading begins only after the user clicks the
download button; the Go backend then uses `go-git` to shallow-clone the official
`main` branch.

When an installation already exists, startup schedules a background update
check. Manual and startup checks force-fetch the configured official `main`
reference without modifying game files, then compare only the local and remote
HEAD hashes. Local commits, rewritten remote history, shallow history, detached
HEAD, and working-tree changes do not block this comparison.

Updates run only when requested by the user. The launcher fetches `main` again,
hard-resets the managed branch and working tree to that exact commit, discards
all tracked changes, and removes untracked and ignored paths that are not part
of the official tree. Cleanup still attempts to remove Finder metadata, but an
untracked regular file named exactly `.DS_Store` is tolerated at any depth if
Finder recreates it. All other non-official paths are removed, so the game
directory remains a launcher-managed cache, not a place for user files.

For each runtime repository, the launcher best-effort maintains `.DS_Store` in
the local `.git/info/exclude`. This rule is never committed or pushed and does
not modify the upstream `.gitignore`; the launcher applies the same exception
during its own validation. If Git metadata is unusable, startup reports an error
and offers the existing retry-download flow; it never turns a check into an
update. Only after the user explicitly requests an update may synchronization
fall back to downloading the latest official version into staging and swapping
it in after validation. Network failures occur before working-tree game content
is changed; other storage or permission failures are reported with a retryable
message.

Git `HEAD` remains the installed-version source of truth, so no separate
active-version manifest is written. Transfers publish localized progress plus
a one-second heartbeat to the UI, while detailed Git lifecycle and sideband
information is retained in structured launcher logs.

The launch button validates `game/shines871/index.html` and passes that file to
the operating system through Wails `app.Browser.OpenFile`. This does not force
Safari, Edge, or another built-in browser: the app associated with `.html` files
opens it. For example, if `.html` files default to Chrome, the game opens in
Chrome; a non-browser association may open a different app instead. A successful
call means the operating system accepted the open request, not that the browser
finished loading the page. Native opener errors are shown in the launcher.

## Development

Requirements: Go 1.25+, Node.js 22+, Wails v3.0.0-alpha.97, and Task.

```sh
npm install --prefix frontend
wails3 dev
```

Development builds fetch
`7e30bc454196683129b8a883a2a1e6011f35bcc6` directly by exact SHA for the first
install, making the normal fetch/reset update flow easy to test without first
downloading the current `main` tip. If the Git server does not support exact-SHA
fetches, the installer falls back to fetching the full `main` history.
Production builds continue to shallow-clone the `main` tip.

Run all automated tests:

```sh
task test
```

Build the current platform:

```sh
task build
```

## Release artifacts

The v1 release tasks intentionally produce ad-hoc or unsigned artifacts:

```sh
task release

# Or build one platform only:
task release:macos
task release:windows
```

The combined `task release` command must run on macOS. It creates separate
arm64 and amd64 DMGs containing the app and an Applications shortcut, followed
by one cross-compiled Windows amd64 portable executable. Outputs are written to
`release/0.1.0/` and no game content is included. Override the output version
with, for example, `task release VERSION=0.2.0`.

## Runtime data

- macOS: `~/Library/Application Support/IdleLineageLauncher/`
- Windows: `%LOCALAPPDATA%/IdleLineageLauncher/`

The relevant subdirectories are:

- `game/shines871/`: the launcher-managed active Git working tree; the entry
  point is `game/shines871/index.html`. Updates discard local changes and delete
  non-official files except untracked regular `.DS_Store` Finder metadata. Do
  not store user files here.
- `game/staging/`: temporary clone data used while installing.
- `webview/`: the launcher UI's Windows WebView2 profile. The externally opened
  game does not use this profile.

An old `game/src` directory is neither migrated nor treated as the active
installation. It is left untouched, and the launcher offers a new download into
`game/shines871`.

The game runs from a local `file://` URL in the selected browser. An already
open game tab is not automatically reloaded after an update; refresh it or
launch the game again to use the updated files. Saves belong to that browser's
local file storage and profile. Another browser or browser profile may therefore
not see the same save, and clearing that browser's site/local data may remove it.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for third-party notices.

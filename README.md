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

When an installation already exists, startup schedules a background fetch.
Manual update checks also fetch `origin/main` without modifying the working
tree, and the UI reports an update only when the local revision is behind.
Updates run only when requested by the user and use a fast-forward pull. Git
`HEAD` is the installed-version source of truth, so no separate active-version
manifest is written. Clone, fetch, and pull publish their Git sideband progress
plus a one-second heartbeat to the UI. Backend lifecycle and Git stages are
emitted as structured logs to the launcher process output.

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
install, making the normal fetch/pull update flow easy to test without first
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
task release:macos
task release:windows
```

macOS creates separate arm64 and amd64 DMGs containing the app and an
Applications shortcut. Windows creates one amd64 portable executable. Outputs
are written to `release/0.1.0/` and no game content is included.

## Runtime data

- macOS: `~/Library/Application Support/IdleLineageLauncher/`
- Windows: `%LOCALAPPDATA%/IdleLineageLauncher/`

The relevant subdirectories are:

- `game/shines871/`: the active Git working tree; the entry point is
  `game/shines871/index.html`.
- `game/staging/`: temporary clone data used while installing.
- `webview/`: the launcher UI's Windows WebView2 profile. The externally opened
  game does not use this profile.

An old `game/src` directory is neither migrated nor treated as the active
installation. It is left untouched, and the launcher offers a new download into
`game/shines871`.

The game runs from a local `file://` URL in the selected browser. An already
open game tab is not automatically reloaded after a pull; refresh it or launch
the game again to use the updated files. Saves belong to that browser's local
file storage and profile. Another browser or browser profile may therefore not
see the same save, and clearing that browser's site/local data may remove it.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for third-party notices.

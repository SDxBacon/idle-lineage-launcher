# Idle Lineage Launcher

Wails v3 + React TypeScript desktop launcher for
[shines871/idle-lineage-class](https://github.com/shines871/idle-lineage-class).

The launcher does not bundle game files. On first launch, after explicit user
confirmation, the Go backend uses `go-git` to shallow-clone the official
`main` branch into the platform application-data directory. Later checks fetch
the remote branch and report an update only when the local revision is behind;
updates use a fast-forward pull so unchanged assets are not downloaded again.
The working tree is kept under `game/src`; its Git `HEAD` is the installed
version source of truth, so no separate active-version manifest is written.
Clone, fetch, and pull publish their Git sideband progress plus a one-second
heartbeat to the UI. Backend lifecycle and Git stages are emitted as structured
logs to the launcher process output.

## Development

Requirements: Go 1.25+, Node.js 22+, Wails v3.0.0-alpha.97, and Task.

```sh
npm install --prefix frontend
wails3 dev
```

Development builds intentionally check out
`7e30bc454196683129b8a883a2a1e6011f35bcc6` after the first clone, making the
normal fetch/pull update flow easy to test. Production builds keep the cloned
`main` tip.

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

The game working tree, install staging area, and Windows WebView2 profile use
separate subdirectories. Installations made by an older launcher remain
playable and are replaced by a shallow Git clone the first time they update.
All native windows share the same asset origin and browser profile, so the
game's existing `localStorage` remains shared.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for third-party notices.

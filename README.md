# Idle Lineage Launcher

Wails v3 + React TypeScript desktop launcher for
[shines871/idle-lineage-class](https://github.com/shines871/idle-lineage-class).

The launcher does not bundle game files. On first launch, after explicit user
confirmation, the Go backend resolves the official `main` commit and streams
its codeload archive into the platform application-data directory. The single
installed game tree is kept under `game/src` and its commit is
recorded by `game/active.json`.

## Development

Requirements: Go 1.25+, Node.js 22+, Wails v3.0.0-alpha.97, and Task.

```sh
npm install --prefix frontend
wails3 dev
```

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

The game versions, install staging area, and Windows WebView2 profile use
separate subdirectories. All native windows share the same asset origin and
browser profile, so the game's existing `localStorage` remains shared.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for third-party notices.

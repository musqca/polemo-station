# Polemo Station

Polemo Station is the self-hosted station service for the Polemo music ecosystem. It provides a local media library, device synchronization, playback-related services, metadata and artwork workflows, and a web interface served by the Station binary.

[中文说明](README.zh-CN.md)

## Status

Polemo Station is under active development. APIs, configuration keys, and build workflows may change while the Polemo clients and server are evolving.

## Features

- Local music library management backed by SQLite
- Device pairing, authorization, synchronization, and file transfer
- Metadata, lyrics, and artwork scraping integrations
- Browser-based Station web interface
- Optional FFmpeg-backed transcoding
- mDNS discovery on supported networks

## Requirements

- Go 1.27 or newer
- FFmpeg in `PATH` when transcoding is required, or configure `stationFFmpegPath`
- Node.js and pnpm only when rebuilding the embedded web frontend

Go dependencies are downloaded automatically by the Go toolchain.

## Quick start

Clone the repository and start the API with the development configuration:

```bash
git clone https://github.com/musqca/polemo-station.git
cd polemo-station
go run . --config config-dev.yaml api
```

The default development port is `20156`. The service stores its SQLite database and library data in the application data directory managed by the Polemo runtime.

For a non-development deployment, create your own configuration file and set at least:

```yaml
port: 20156
stationId: "my-station"
stationPairingToken: ""
stationPublicBaseUrl: "http://127.0.0.1:20156"
stationFFmpegPath: ""
stationMediaRoots:
  - "/path/to/your/music"
```

Do not reuse the example development secrets in a production environment.

## Commands

Show the available commands:

```bash
go run . --help
```

Start the API:

```bash
go run . --config config.yaml api
```

Reset Station superadmin authorization for the configured data directory:

```bash
go run . --config config.yaml reset-superadmin
```

The `dal` command is intended for database model and DAL generation during development:

```bash
go run . --config config.yaml dal
```

## Development

Run the test suite:

```bash
go test ./...
```

Build a local binary:

```bash
go build -o dist/polemo-station .
```

The web frontend is embedded from `internal/frontend/build`. To refresh the embedded frontend and build release binaries, make sure the sibling [`polemo-station-web`](https://github.com/musqca/polemo-station-web) checkout is available, then run:

```bash
make frontend
make darwin
make linux
make windows
```

You can build the configured release targets together with:

```bash
make pkg
```

## Repository layout

```text
cmd/                 CLI commands and API startup
internal/config.go   Station configuration defaults and types
internal/router/     HTTP and WebSocket routes
internal/station/    Station services and synchronization logic
internal/frontend/   Embedded web frontend assets
doc/                 Design and future-work notes
```

## Related projects

- [Polemo Station Web](https://github.com/musqca/polemo-station-web)
- [Polemo Server](https://github.com/musqca/polemo-server)
- [Polemo App](https://github.com/musqca/polemo-app)

# <img src="web/logo.png" alt="" width="32"> PrintSpy
![Build Status](https://img.shields.io/github/actions/workflow/status/ccmpbll/printspy/build.yaml) ![Docker Image Size](https://img.shields.io/docker/image-size/ccmpbll/printspy/latest) ![Docker Pulls](https://img.shields.io/docker/pulls/ccmpbll/printspy.svg) ![License](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)

A self-hosted dashboard for monitoring multiple 3D printers from a single web interface.

> **Early Development** — PrintSpy is brand new and under active development. Things will break, APIs will change, and features are still being added. Feedback and contributions are welcome, but expect rough edges.

## What it does

PrintSpy connects to your 3D printers and displays their status on a single dashboard. Each printer gets a row showing:

- Live webcam feed or periodic snapshots (toggleable per printer)
- GCode thumbnail for the current print
- Print progress, elapsed time, remaining time, and ETA
- Hotend, bed, and chamber temperatures (chamber shown when detected)
- Layer progress (when DisplayLayerProgress plugin is installed)
- Direct link to each printer's native web interface

Status updates are pushed in real-time via Server-Sent Events (SSE). Printers are managed through the settings page — no config files required. Just run the container, open the browser, and add your printers.

## Features

- **Real-time updates** via SSE — no manual refresh needed
- **Auto-detection** of camera stack (mjpg-streamer / camera-streamer) and printer name
- **Plugin detection** — queries installed OctoPrint plugins to enable features like layer progress
- **Snapshot/live toggle** — choose between periodic snapshots or live MJPEG stream per printer
- **Printer reordering** — arrange printers in any order from the settings page
- **Responsive layout** — works on desktop, tablet, and mobile
- **Dark mode** — follows system preference
- **Error reporting** — connection status banner, OctoPrint error passthrough, camera status differentiation
- **Multi-arch** — runs on x86 and ARM (Raspberry Pi)

## Supported platforms

- **OctoPrint** — fully supported today

PrintSpy uses a plugin architecture, so adding support for new printer platforms is straightforward.

## Quick start

```bash
docker run -d \
  --name printspy \
  -p 8080:8080 \
  -v printspy-data:/data \
  ccmpbll/printspy:latest
```

Open `http://localhost:8080`, click the settings gear, and add your first printer. You'll need the printer's URL and OctoPrint API key.

### Docker Compose

```yaml
services:
  printspy:
    image: ccmpbll/printspy:latest
    ports:
      - "8080:8080"
    volumes:
      - printspy-data:/data
    restart: unless-stopped

volumes:
  printspy-data:
```

## Configuration

All printer management is done through the web UI — open the settings page to add, edit, reorder, and remove printers. No config files needed.

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PRINTSPY_PORT` | `8080` | HTTP server port |
| `PRINTSPY_DATA_DIR` | `/data` | SQLite database location |

## Getting your OctoPrint API key

1. Open your OctoPrint web interface
2. Go to **Settings** (wrench icon) → **API**
3. Copy the **Global API Key**, or create a new one under **Application Keys**

## Tech stack

- **Go** backend — single binary, low resource usage
- **SQLite** — no external database needed
- **Vanilla HTML/CSS/JS** frontend — no build step, no framework
- **Docker** — multi-arch container (amd64 + arm64)

## Building from source

```bash
# Requires Go 1.24+ and CGO (for SQLite)
make build

# Or with Docker
make docker
```

## Contributing

PrintSpy is in early development. If you'd like to contribute:

- Open an issue to discuss before submitting large changes
- Bug reports with `docker logs` output are especially helpful
- Plugin implementations for PrusaLink or Moonraker/Klipper are welcome

## License

AGPL-3.0 — see [LICENSE](LICENSE) for details.

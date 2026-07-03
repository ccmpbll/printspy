# <img src="web/logo.png" alt="" width="64"> PrintSpy
![Build Status](https://img.shields.io/github/actions/workflow/status/ccmpbll/printspy/build.yaml) ![Docker Image Size](https://img.shields.io/docker/image-size/ccmpbll/printspy/latest) ![Docker Pulls](https://img.shields.io/docker/pulls/ccmpbll/printspy.svg) ![License](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)

A self-hosted dashboard for monitoring multiple 3D printers — OctoPrint and PrusaLink — from a single web interface.

> **Early Development** — expect rough edges, breaking changes, and evolving APIs. Feedback and contributions welcome.

## What it does

Each printer gets a row: webcam/snapshot, GCode thumbnail, progress/ETA, temps, layer progress (OctoPrint + DisplayLayerProgress), and smart plug power state/control (Tasmota or PSU Control). Updates push live via SSE. Everything's configured through the settings page — no config files, no restart needed.

## Features

- Real-time SSE updates, no manual refresh
- Auto-detects camera stack, printer name, and installed plugins
- Smart plug power control + energy monitoring (Tasmota, PSU Control)
- Print control (pause/resume/cancel) and one-click reprint from recent files
- Config backup/restore as YAML
- Snapshot/live toggle, printer reordering, dark mode, responsive layout
- Multi-arch (x86 + ARM)

## Supported platforms

- **OctoPrint** — fully supported
- **PrusaLink** — experimental (Only tested on MK4S and Core One)

Plugin architecture — new platforms are straightforward to add.

## Quick start

```bash
docker run -d \
  --name printspy \
  -p 8080:8080 \
  -v printspy-data:/data \
  ccmpbll/printspy:latest
```

Open `http://localhost:8080` — first run prompts you to create a login. Once in, click the settings gear and add your first printer. You'll need the printer's URL and OctoPrint API key.

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

### Login

First run redirects to a setup page to create the first account. Add or remove additional users from Settings → Users. No roles or permission tiers — every account has full access.

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
# Tested with Go 1.26, requires CGO (for SQLite)
make build

# Or with Docker
make docker
```

## Contributing

PrintSpy is in early development. If you'd like to contribute:

- Open an issue to discuss before submitting large changes
- Bug reports with `docker logs` output are especially helpful
- Plugin implementations for Moonraker/Klipper are welcome

## License

AGPL-3.0 — see [LICENSE](LICENSE) for details.

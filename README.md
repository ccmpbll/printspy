# <img src="web/logo.png" alt="" width="64"> PrintSpy
![Build Status](https://img.shields.io/github/actions/workflow/status/ccmpbll/printspy/build.yaml) ![Docker Image Size](https://img.shields.io/docker/image-size/ccmpbll/printspy/latest) ![Docker Pulls](https://img.shields.io/docker/pulls/ccmpbll/printspy.svg) ![License](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)

A self-hosted dashboard for monitoring multiple 3D printers — OctoPrint and PrusaLink — from a single web interface.

This has been created mostly for my own use. If someone else finds it useful, all the better. If you would like to contribute, please be my guest. I only have Prusa printers and can only test with those. 

> **Early Development** — expect rough edges, breaking changes, and evolving APIs. Feedback and contributions welcome.

## What it does

Each printer gets a row: webcam/snapshot, progress/ETA, temps, and smart plug power state/control. Updates push live via SSE. Everything's configured through the settings page — no config files, no restart needed.

## Supported platforms

- **OctoPrint** — fully supported
- **PrusaLink** — experimental (only tested on MK4S and Core One)

Plugin architecture — new platforms are straightforward to add.

## Features

### Core (both platforms)

- Real-time dashboard updates via SSE, no manual refresh
- Print control — pause, resume, cancel
- File Manager per printer — browse every file on its storage, one-click reprint or delete, thumbnail previews (success/failure badges per file: native on OctoPrint, backfilled from print history on PrusaLink)
- Print history per printer, with configurable retention (days, 0 = keep forever)
- Pushover notifications — print started/checkpoints/complete/failed/error, each independently configurable: priority, sound, image source (camera/thumbnail/none), and custom title/message templates
- Smart plug power control via a directly-configured Tasmota device, independent of any platform plugin, assignable to any printer
- Auto-off after idle timeout and thermal runaway protection (second layer on top of firmware protection) for printers with an assigned smart plug
- Camera feed via [printspy-cam](https://github.com/ccmpbll/printspy-cam) (ESP32-CAM firmware), assignable to any printer — snapshot/live/plate-thumbnail toggle buttons under each card's image
- Multi-user login with per-account passwords, no roles/tiers
- Config backup/restore as YAML
- Printer reordering, optional free-text "model" field, dark mode, responsive layout

### OctoPrint

- Auto-detects camera stack (MJPEG or camera-streamer), printer name, and installed plugins
- Native smart plug power control + energy monitoring, auto-detected via the Tasmota or PSU Control plugin
- Layer progress display (needs the DisplayLayerProgress plugin)
- Live webcam streaming, with a snapshot/live toggle

### PrusaLink

- Snapshot only — no live stream from the printer's own camera (assign a printspy-cam for a live feed)
- No native power control over the API — use a directly-configured smart plug instead
- Keepalive ping — works around printers whose wifi drops off after sitting idle
- File upload from the dashboard, with an optional "print immediately"
- Slicer print-host target — point PrusaSlicer/OrcaSlicer's "Send to printer" (PrusaLink mode) at PrintSpy, pinned to one specific printer; "Upload" relays automatically once the printer's online, "Upload and Print" also powers it on first if it's off — no manual step either way
- Print history enriched with real per-print metadata — material, filament used/cost, layer height, duration vs. estimate, and per-tool breakdown for multi-material prints — parsed directly from the file's own slicer metadata, since none of this is exposed over PrusaLink's API

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

### Smart plugs

OctoPrint printers with the Tasmota or PSU Control plugin installed get power control automatically — nothing to configure. For everything else (PrusaLink, Klipper, or an OctoPrint printer without the plugin), add a Tasmota device directly under Settings → Smart Plugs and assign it to a printer. Plugs are managed independently of printers, so deleting a printer unassigns its plug instead of deleting it.

### Cameras

Any printer type can get a webcam feed by assigning a [printspy-cam](https://github.com/ccmpbll/printspy-cam) device under Settings → Cameras — useful for PrusaLink, which has no webcam support of its own. Assigning a camera overrides whatever webcam a printer's own plugin would otherwise show. Cameras are managed independently of printers, so deleting a printer unassigns its camera instead of deleting it.

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
- **Docker** — amd64 container

## Building from source

```bash
# Requires Go 1.25+ and CGO (for SQLite)
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

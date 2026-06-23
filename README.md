# PrintSpy
![Build Status](https://img.shields.io/github/actions/workflow/status/ccmpbll/printspy/build.yaml) ![Docker Image Size](https://img.shields.io/docker/image-size/ccmpbll/printspy/latest) ![Docker Pulls](https://img.shields.io/docker/pulls/ccmpbll/printspy.svg) ![License](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)

A self-hosted dashboard for monitoring multiple 3D printers from a single web interface.

> **Early Development** — PrintSpy is brand new and under active development. Things will break, APIs will change, and features are still being added. Feedback and contributions are welcome, but expect rough edges.

## What it does

PrintSpy connects to your 3D printers and displays their status on a single dashboard. Each printer gets a row showing:

- Live webcam feed or periodic snapshots (toggleable per printer)
- GCode thumbnail for the current print
- Print progress, elapsed time, remaining time, and ETA
- Hotend and bed temperatures
- Layer count and filament usage
- Direct link to each printer's native web interface

Printers are configured through the web UI — no config files required. Just run the container and start adding printers.

## Supported platforms

- **OctoPrint** — fully supported today
- **PrusaLink** — planned
- **Klipper/Moonraker** — planned, but I don't have any printers running Klipper...

PrintSpy uses a plugin architecture, so adding support for new printer platforms is straightforward.

## Quick start

```bash
docker run -d \
  --name printspy \
  -p 8080:8080 \
  -v printspy-data:/data \
  ccmpbll/printspy:latest
```

Open `http://localhost:8080` and click **Add printer** to get started. You'll need your printer's URL and API key.

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

### Web UI (recommended)

All printer management is done through the dashboard. Add, edit, and remove printers without touching any files.

### YAML config (optional)

Power users can define printers in a config file. Mount it into the container and printers will be seeded into the database on first run:

```yaml
server:
  port: 8080
  data_dir: /data

printers:
  - name: "My Printer"
    type: octoprint
    url: "http://192.168.1.40"
    api_key: "YOUR_OCTOPRINT_API_KEY"
    poll_interval: 10
```

```bash
docker run -d \
  --name printspy \
  -p 8080:8080 \
  -v printspy-data:/data \
  -v ./config.yaml:/etc/printspy/config.yaml \
  ccmpbll/printspy:latest
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PRINTSPY_PORT` | `8080` | HTTP server port |
| `PRINTSPY_DATA_DIR` | `/data` | SQLite database location |
| `PRINTSPY_CONFIG` | — | Path to YAML config file |

## Getting your OctoPrint API key

1. Open your OctoPrint web interface
2. Go to **Settings** (wrench icon) → **API**
3. Copy the **Global API Key**, or create a new one under **Application Keys**

## Tech stack

- **Go** backend — single binary, low resource usage
- **SQLite** — no external database needed
- **Vanilla HTML/CSS/JS** frontend — no build step, no framework
- **Docker** — single container, ~20MB image

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

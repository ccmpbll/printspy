# UI refresh mockup

Static, clickable mockup of the proposed UI. Not wired to the app; all data is made up.
It reuses the real `web/style.css` and `web/logo.png`.

```bash
python3 -m http.server 8791 --bind 127.0.0.1
# then open http://127.0.0.1:8791/docs/mockups/ui-refresh/
```

## Tabs

- **Current**: today's dashboard, for comparison.
- **A · Refined cards**: the chosen direction.
- **B · Camera tiles (parked)**: a camera-first grid. Not part of this change; kept here as a possible secondary view later.
- **Files**, **History**, **Settings**: full pages instead of the modals (Settings is reached from the user menu).
- **States**: every card state the main fleet doesn't show (attention, disconnected, busy, waiting ingest banner).
- **Login**: login, wrong password, rate limited and first-run setup.

The top bar also flips demo states (busy or idle fleet, Files/History as buttons or in the menu, connection lost, no printers).

## What A changes

- Fleet strip with state filters and "Next finish"
- Pill buttons throughout (32px); Files and History as header buttons, user menu (Settings, Log out, version) on the right like Ulmo
- Aligned stat and temperature grid; temperature bars fill toward target
- Plug switches, with the printer plug locked while printing; slim idle and offline cards
- Settings as a full page with side tabs; edit pages for printers, plugs, cameras and slicer targets; per-event notification Customize panel with a live preview
- Files and History per printer; upload dialog with a drop zone; Raw API window

## Proposals, not today's behaviour

The fleet strip and "Next finish", the "Start print" button on idle cards (opens Files), search/sort/list-grid on Files, search and a result filter on History, the upload drop zone and moving bar, the notification preview, and offline cards always collapsing to a slim row.

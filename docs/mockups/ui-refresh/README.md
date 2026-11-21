# UI refresh mockup

Static mockup of the proposed dashboard. Not wired to the app; data is made up.
It reuses the real `web/style.css` and `web/logo.png`.

```bash
python3 -m http.server 8791 --bind 127.0.0.1
# then open http://127.0.0.1:8791/docs/mockups/ui-refresh/
```

Tabs: **Current** (today's look), **A · Refined cards** (the chosen direction), **B · Camera tiles**, and **Settings**
(also reachable from the user menu).

Decisions in A: fleet strip with state filters, pill buttons everywhere (32px), Files/History as header buttons,
user menu (Settings, Log out, version) on the right like Ulmo, aligned stat and temperature grid, target-based temperature bars,
plug switches with the printer plug locked while printing, slim idle/offline cards, error card, slicer-ingest banner.

Settings: a full page with side tabs (Printers, Plugs & cameras, General, Notifications, Integrations, Account & backup)
instead of the long modal. Each printer, smart plug, camera and slicer target has an edit page (Edit / + Add). Notifications
has a per-event Customize panel (title, message with placeholders, sound, image source, high priority) with a live preview.

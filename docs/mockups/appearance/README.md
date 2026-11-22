# Appearance mockup (page background and progress bars)

The mockup used to design the per-user Appearance settings (shipped in PR #67), kept as it was when the design was settled on 2026-11-22. It is the UI refresh mockup plus two extra rows under the top switcher. Static; nothing is wired up.

```bash
python3 -m http.server 8791 --bind 127.0.0.1
# then open http://127.0.0.1:8791/docs/mockups/appearance/
```

- **Background:** the patterns that were tried, with a strength slider and a fade-out switch. `bg.js` builds each tile; the app's version of this code is `web/background.js`. Soft glow and Dots + glow are only in the mockup (not wanted in the app); Lightning and Gyroid waves were tried and removed from it.
- **Progress bar:** colour swatches and a custom picker, Solid / Gradient / Striped, stripe strength, and keep-amber-when-paused. In the app the style is set separately for the print progress bar and the other bars; this mockup applies one style to the print bar only.

It uses the real `web/style.css`, so it can drift from how it looked at the time as the app's stylesheet changes. Don't edit this folder.

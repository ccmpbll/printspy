// Per-user page background: a faint pattern behind the dashboard. The choice is stored
// per user on the server (/api/me/preferences) and cached in localStorage so the page
// does not flash the plain background before /api/me answers. Chosen in
// Settings > Account & backup > Appearance.

const BG_PATTERNS = [
    ['none', 'None'], ['dots', 'Dots'], ['grid', 'Build plate grid'], ['grid2', 'Grid, major lines'],
    ['layers', 'Layer lines'], ['hatch', 'Diagonal hatch'], ['hex', 'Hexagons'],
    ['tri', 'Triangles (infill)'], ['plus', 'Plus marks'],
];
const BG_DEFAULT = {pattern: 'none', strength: 6, fade: true};
const BG_MIN = 2, BG_MAX = 16;

let bgPref = bgSanitize(bgCached());

function bgSanitize(p) {
    if (!p || !BG_PATTERNS.some(([k]) => k === p.pattern)) return {...BG_DEFAULT};
    const s = Math.round(Number(p.strength));
    return {pattern: p.pattern, strength: s >= BG_MIN && s <= BG_MAX ? s : BG_DEFAULT.strength, fade: p.fade !== false};
}

function bgCached() {
    try { return JSON.parse(localStorage.getItem('bg-pref')); } catch (e) { return null; }
}

// CSS for one pattern at strength a (0..1), drawn in the theme's text colour.
function bgCSS(pattern, a) {
    const rgb = matchMedia('(prefers-color-scheme: dark)').matches ? '255,255,255' : '0,0,0';
    const c = k => `rgba(${rgb},${+(a * k).toFixed(3)})`;
    const svg = (w, h, body) => `url("data:image/svg+xml,${encodeURIComponent(`<svg xmlns='http://www.w3.org/2000/svg' width='${w}' height='${h}'>${body}</svg>`)}")`;
    switch (pattern) {
        case 'dots': return {image: `radial-gradient(${c(1)} 1.2px, transparent 1.6px)`, size: '22px 22px'};
        case 'grid': return {image: `linear-gradient(${c(1)} 1px, transparent 1px), linear-gradient(90deg, ${c(1)} 1px, transparent 1px)`, size: '28px 28px'};
        case 'grid2': return {image: `linear-gradient(${c(1.6)} 1px, transparent 1px), linear-gradient(90deg, ${c(1.6)} 1px, transparent 1px), linear-gradient(${c(.7)} 1px, transparent 1px), linear-gradient(90deg, ${c(.7)} 1px, transparent 1px)`, size: '140px 140px, 140px 140px, 28px 28px, 28px 28px'};
        case 'layers': return {image: `repeating-linear-gradient(0deg, ${c(1)} 0, ${c(1)} 1px, transparent 1px, transparent 6px)`, size: 'auto'};
        case 'hatch': return {image: `repeating-linear-gradient(45deg, ${c(1)} 0, ${c(1)} 1px, transparent 1px, transparent 10px)`, size: 'auto'};
        case 'hex': return {image: svg(56, 100, `<g fill='none' stroke='${c(1.4)}' stroke-width='1'><path d='M28 66L0 50L0 16L28 0L56 16L56 50L28 66L28 100'/><path d='M28 0L28 -34L0 -50L-28 -34M28 0L28 -34L56 -50L84 -34'/><path d='M28 100L0 116M28 100L56 116'/></g>`), size: '56px 100px'};
        case 'tri': return {image: svg(60, 52, `<g fill='none' stroke='${c(1.3)}' stroke-width='1'><path d='M0 52L30 0L60 52Z M30 0L60 52M0 52H60'/></g>`), size: '60px 52px'};
        case 'plus': return {image: svg(32, 32, `<g stroke='${c(2)}' stroke-width='1'><path d='M16 12V20M12 16H20'/></g>`), size: '32px 32px'};
    }
    return null;
}

// Draw the pattern on a fixed layer behind the page (so it does not scroll and the fade is
// relative to the screen).
function applyBackground(pref) {
    bgPref = bgSanitize(pref);
    let layer = document.getElementById('bg-layer');
    const css = bgPref.pattern === 'none' ? null : bgCSS(bgPref.pattern, bgPref.strength / 100);
    if (!css) { if (layer) layer.remove(); return; }
    if (!layer) {
        layer = document.createElement('div');
        layer.id = 'bg-layer';
        layer.setAttribute('aria-hidden', 'true');
        document.body.prepend(layer);
    }
    layer.style.backgroundImage = css.image;
    layer.style.backgroundSize = css.size;
    layer.classList.toggle('fade', bgPref.fade);
}

function cacheBackground(pref) {
    try { localStorage.setItem('bg-pref', JSON.stringify(bgSanitize(pref))); } catch (e) {}
}

applyBackground(bgPref);
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => applyBackground(bgPref));

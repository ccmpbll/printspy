// Per-user page background: a faint pattern behind the dashboard. The choice is stored
// per user on the server (/api/me/preferences) and cached in localStorage so the page
// does not flash the plain background before /api/me answers. Chosen in
// Settings > Account & backup > Appearance. The same preferences hold the bar style.

const BG_PATTERNS = [
    ['none', 'None'], ['dots', 'Dots'], ['grid', 'Build plate grid'], ['grid2', 'Grid, major lines'],
    ['layers', 'Layer lines'], ['hatch', 'Diagonal hatch'], ['hex', 'Honeycomb'],
    ['tri', 'Triangles'], ['gyroid', 'Gyroid'], ['cubic', 'Cubic'], ['concentric', 'Concentric'],
    ['hilbert', 'Hilbert curve'], ['plus', 'Plus marks'],
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

// Infill-style tiles that are calculated rather than drawn by hand. Built on first use and kept.
const bgTiles = {};
function bgTile(name, make) { return bgTiles[name] || (bgTiles[name] = make()); }

// Gyroid: the contour of sin x cos y + sin y cos z + sin z cos x = 0 at a fixed height z (a layer
// slice of the gyroid), found with marching squares over one period so the tile repeats without a seam.
function gyroidSlice(size, n, z) {
    const f = (x, y) => Math.sin(x) * Math.cos(y) + Math.sin(y) * Math.cos(z) + Math.sin(z) * Math.cos(x);
    const step = (2 * Math.PI) / n, k = size / n;
    const val = (i, j) => f(i * step, j * step);
    const lerp = (a, b, va, vb) => a + (b - a) * (va / (va - vb));
    const seg = {1: 'lt', 2: 'tr', 3: 'lr', 4: 'rb', 6: 'tb', 7: 'lb', 8: 'bl', 9: 'tb', 11: 'rb', 12: 'rl', 13: 'tr', 14: 'lt'};
    const two = {5: ['lt', 'rb'], 10: ['tr', 'bl']};
    let d = '';
    for (let i = 0; i < n; i++) for (let j = 0; j < n; j++) {
        const v = [val(i, j), val(i + 1, j), val(i + 1, j + 1), val(i, j + 1)];
        const idx = (v[0] > 0 ? 1 : 0) | (v[1] > 0 ? 2 : 0) | (v[2] > 0 ? 4 : 0) | (v[3] > 0 ? 8 : 0);
        if (idx === 0 || idx === 15) continue;
        const x0 = i * k, y0 = j * k, e = {
            t: [lerp(x0, x0 + k, v[0], v[1]), y0], r: [x0 + k, lerp(y0, y0 + k, v[1], v[2])],
            b: [lerp(x0, x0 + k, v[3], v[2]), y0 + k], l: [x0, lerp(y0, y0 + k, v[0], v[3])],
        };
        const draw = p => { const a = e[p[0]], b = e[p[1]]; d += `M${a[0].toFixed(1)} ${a[1].toFixed(1)}L${b[0].toFixed(1)} ${b[1].toFixed(1)}`; };
        if (two[idx]) two[idx].forEach(draw); else draw(seg[idx]);
    }
    return d;
}

// Cubic: an isometric cube tiling, i.e. hexagons with a Y inside each. The hexagon is L high on
// its vertical sides and w wide; w is a whole number of pixels (56 against a regular 55.43) so the
// tile joins land on pixel boundaries and show no seam.
function cubicPath(L, w) {
    const f = v => v.toFixed(2), P = p => f(p[0]) + ' ' + f(p[1]);
    let d = '';
    for (let j = -1; j <= 3; j++) for (let i = -1; i <= 2; i++) {
        const cx = i * w + (((j % 2) + 2) % 2 ? w / 2 : 0), cy = 1.5 * L * j;
        const top = [cx, cy - L], ur = [cx + w / 2, cy - L / 2], lr = [cx + w / 2, cy + L / 2], bot = [cx, cy + L], ll = [cx - w / 2, cy + L / 2], ul = [cx - w / 2, cy - L / 2];
        d += `M${P(top)}L${P(ur)}L${P(lr)}L${P(bot)}L${P(ll)}L${P(ul)}Z`;
        d += `M${P(ul)}L${f(cx)} ${f(cy)}L${P(ur)}M${f(cx)} ${f(cy)}L${P(bot)}`;
    }
    return d;
}

// Hilbert curve of a given order inside a size x size box.
function hilbertPath(order, size) {
    const n = 1 << order, k = size / n, pts = [];
    for (let i = 0; i < n * n; i++) {
        let x = 0, y = 0, t = i;
        for (let s = 1; s < n; s *= 2) {
            const rx = 1 & (t >> 1), ry = 1 & (t ^ rx);
            if (ry === 0) { if (rx === 1) { x = s - 1 - x; y = s - 1 - y; } [x, y] = [y, x]; }
            x += s * rx; y += s * ry; t >>= 2;
        }
        pts.push([(x + .5) * k, (y + .5) * k]);
    }
    return 'M' + pts.map(p => p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join('L');
}

// Nested rounded squares filling a size x size tile.
function nestedSquares(size, gap) {
    let d = '';
    for (let o = gap / 2; o < size / 2; o += gap) d += `<rect x='${o}' y='${o}' width='${size - 2 * o}' height='${size - 2 * o}' rx='${Math.max(0, 4 - o / 4)}'/>`;
    return d;
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
        case 'gyroid': {
            // two slices a quarter turn apart, so the waves cross like the top of a printed gyroid
            const a1 = bgTile('gyroid-a', () => gyroidSlice(100, 50, 0.4)), a2 = bgTile('gyroid-b', () => gyroidSlice(100, 50, 1.97));
            return {image: svg(100, 100, `<g fill='none' stroke='${c(1.5)}' stroke-width='1.2' stroke-linecap='round'><path d='${a1}'/><path d='${a2}'/></g>`), size: '100px 100px'};
        }
        case 'cubic': return {image: svg(56, 96, `<path d='${bgTile('cubic', () => cubicPath(32, 56))}' fill='none' stroke='${c(1.4)}' stroke-width='1'/>`), size: '56px 96px'};
        case 'concentric': return {image: svg(48, 48, `<g fill='none' stroke='${c(1.5)}' stroke-width='1'>${nestedSquares(48, 8)}</g>`), size: '48px 48px'};
        case 'hilbert': return {image: svg(56, 56, `<path d='${bgTile('hilbert', () => hilbertPath(3, 56))}' fill='none' stroke='${c(1.6)}' stroke-width='1.2' stroke-linejoin='round'/>`), size: '56px 56px'};
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

// ---- Progress bars: colour of the print-progress bar, and a style for it and for the other bars ----

const BAR_SWATCHES = [
    ['Green', ''], ['Blue', '#3b82f6'], ['Cyan', '#06b6d4'], ['Teal', '#14b8a6'], ['Purple', '#a855f7'],
    ['Pink', '#ec4899'], ['Orange', '#f97316'], ['Red', '#ef4444'], ['White', '#e5e7eb'],
];
const BAR_STYLES = [['solid', 'Solid'], ['gradient', 'Gradient'], ['striped', 'Striped']];
const BAR_DEFAULT = {color: '', style: 'solid', other_style: 'solid', stripe: 4, keep_amber: true};
const STRIPE_MIN = 1, STRIPE_MAX = 8;

let barPref = barSanitize(barCached());

function barSanitize(p) {
    if (!p) return {...BAR_DEFAULT};
    const stripe = Math.round(Number(p.stripe));
    return {
        color: /^#[0-9a-f]{6}$/i.test(p.color || '') ? p.color.toLowerCase() : '',
        style: BAR_STYLES.some(([k]) => k === p.style) ? p.style : BAR_DEFAULT.style,
        other_style: BAR_STYLES.some(([k]) => k === p.other_style) ? p.other_style : BAR_DEFAULT.other_style,
        stripe: stripe >= STRIPE_MIN && stripe <= STRIPE_MAX ? stripe : BAR_DEFAULT.stripe,
        keep_amber: p.keep_amber !== false,
    };
}

function barCached() {
    try { return JSON.parse(localStorage.getItem('bar-pref')); } catch (e) { return null; }
}

function cacheBars(pref) {
    try { localStorage.setItem('bar-pref', JSON.stringify(barSanitize(pref))); } catch (e) {}
}

function applyBars(pref) {
    barPref = barSanitize(pref);
    const root = document.documentElement;
    if (barPref.color) root.style.setProperty('--prog', barPref.color); else root.style.removeProperty('--prog');
    // the darker stripe is the bar's own colour mixed with black: 1 = faint ... 8 = bold
    root.style.setProperty('--stripe-pct', `${barPref.stripe * 7.5}%`);
    for (const [group, style] of [['print', barPref.style], ['other', barPref.other_style]]) {
        root.classList.toggle(`bar-${group}-gradient`, style === 'gradient');
        root.classList.toggle(`bar-${group}-striped`, style === 'striped');
    }
    root.classList.toggle('prog-paused-too', !barPref.keep_amber);
}

applyBars(barPref);

// Another tab changed the look: follow it.
window.addEventListener('storage', e => {
    if (e.key === 'bg-pref') applyBackground(bgCached());
    if (e.key === 'bar-pref') applyBars(barCached());
});

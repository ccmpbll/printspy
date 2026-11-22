/* Background pattern picker for the mockup. Patterns are drawn from the theme's text colour
   at a low alpha, so they follow light/dark. Nothing here touches the real app. */
(function () {
  const dark = matchMedia('(prefers-color-scheme: dark)').matches;
  const rgb = dark ? '255,255,255' : '0,0,0';
  const c = a => `rgba(${rgb},${a})`;
  const svg = (w, h, body) => `url("data:image/svg+xml,${encodeURIComponent(`<svg xmlns='http://www.w3.org/2000/svg' width='${w}' height='${h}'>${body}</svg>`)}")`;


  // ---- generated infill tiles (computed once; all tile seamlessly or sit inside the tile) ----
  // Gyroid: contour of sin x cos y + sin y cos z + sin z cos x = 0 at a fixed z (the gyroid's layer slice),
  // found with marching squares over one period, so the tile repeats without a seam.
  function gyroidPath(size, n, z = 0.9) {
    const f = (x, y) => Math.sin(x) * Math.cos(y) + Math.sin(y) * Math.cos(z) + Math.sin(z) * Math.cos(x);
    const step = (2 * Math.PI) / n, k = size / n, pts = [];
    const val = (i, j) => f(i * step, j * step);
    const lerp = (a, b, va, vb) => a + (b - a) * (va / (va - vb));
    let d = '';
    for (let i = 0; i < n; i++) for (let j = 0; j < n; j++) {
      const v = [val(i, j), val(i + 1, j), val(i + 1, j + 1), val(i, j + 1)];
      const idx = (v[0] > 0 ? 1 : 0) | (v[1] > 0 ? 2 : 0) | (v[2] > 0 ? 4 : 0) | (v[3] > 0 ? 8 : 0);
      if (idx === 0 || idx === 15) continue;
      const x0 = i * k, y0 = j * k, e = {
        t: [lerp(x0, x0 + k, v[0], v[1]), y0], r: [x0 + k, lerp(y0, y0 + k, v[1], v[2])],
        b: [lerp(x0, x0 + k, v[3], v[2]), y0 + k], l: [x0, lerp(y0, y0 + k, v[0], v[3])],
      };
      const seg = {1: 'lt', 2: 'tr', 3: 'lr', 4: 'rb', 6: 'tb', 7: 'lb', 8: 'bl', 9: 'tb', 11: 'rb', 12: 'rl', 13: 'tr', 14: 'lt'};
      const two = {5: ['lt', 'rb'], 10: ['tr', 'bl']};
      const draw = p => { const a = e[p[0]], b = e[p[1]]; d += `M${a[0].toFixed(1)} ${a[1].toFixed(1)}L${b[0].toFixed(1)} ${b[1].toFixed(1)}`; };
      if (two[idx]) two[idx].forEach(draw); else draw(seg[idx]);
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
  // Lightning: a small branching tree growing up from the bottom of the tile.
  function treePath(size) {
    let d = '';
    const branch = (x, y, len, ang, depth) => {
      const x2 = x + Math.sin(ang) * len, y2 = y - Math.cos(ang) * len;
      d += `M${x.toFixed(1)} ${y.toFixed(1)}L${x2.toFixed(1)} ${y2.toFixed(1)}`;
      if (depth === 0) return;
      branch(x2, y2, len * .74, ang - .62, depth - 1);
      branch(x2, y2, len * .74, ang + .5, depth - 1);
      if (depth > 2) branch(x + (x2 - x) * .55, y + (y2 - y) * .55, len * .5, ang + (depth % 2 ? .95 : -.95), depth - 2);
    };
    branch(size / 2, size - 2, size * .26, 0.05, 5);
    return d;
  }
  // Cubic: isometric cube tiling (hexagons with a Y inside each), L = hexagon side.
  function cubicPath(L) {
    const w = L * Math.sqrt(3), f = v => v.toFixed(2), P = p => f(p[0]) + ' ' + f(p[1]); let d = '';
    for (let j = -1; j <= 3; j++) for (let i = -1; i <= 2; i++) {
      const cx = i * w + (((j % 2) + 2) % 2 ? w / 2 : 0), cy = 1.5 * L * j;
      const top = [cx, cy - L], ur = [cx + w / 2, cy - L / 2], lr = [cx + w / 2, cy + L / 2], bot = [cx, cy + L], ll = [cx - w / 2, cy + L / 2], ul = [cx - w / 2, cy - L / 2];
      d += `M${P(top)}L${P(ur)}L${P(lr)}L${P(bot)}L${P(ll)}L${P(ul)}Z`;
      d += `M${P(ul)}L${f(cx)} ${f(cy)}L${P(ur)}M${f(cx)} ${f(cy)}L${P(bot)}`;
    }
    return d;
  }
  window.__gyroid = gyroidPath; const GYR_A = gyroidPath(100, 50, 0.4), GYR_B = gyroidPath(100, 50, 1.97), HIL = hilbertPath(3, 56), TREE = treePath(120), CUBE = cubicPath(32), CUBE_W = 32 * Math.sqrt(3);
  const nest = (size, gap) => { let d = ''; for (let o = gap / 2; o < size / 2; o += gap) d += `<rect x='${o}' y='${o}' width='${size - 2 * o}' height='${size - 2 * o}' rx='${Math.max(0, 4 - o / 4)}'/>`; return d; };

  const PATTERNS = {
    cur:   {label: 'Current (flat)', make: () => ({})},
    dots:  {label: 'Dots', make: a => ({image: `radial-gradient(${c(a)} 1.2px, transparent 1.6px)`, size: '22px 22px'})},
    grid:  {label: 'Build plate grid', make: a => ({image: `linear-gradient(${c(a)} 1px, transparent 1px), linear-gradient(90deg, ${c(a)} 1px, transparent 1px)`, size: '28px 28px'})},
    grid2: {label: 'Grid, major lines', make: a => ({image: `linear-gradient(${c(a * 1.6)} 1px, transparent 1px), linear-gradient(90deg, ${c(a * 1.6)} 1px, transparent 1px), linear-gradient(${c(a * .7)} 1px, transparent 1px), linear-gradient(90deg, ${c(a * .7)} 1px, transparent 1px)`, size: '140px 140px, 140px 140px, 28px 28px, 28px 28px'})},
    layers:{label: 'Layer lines', make: a => ({image: `repeating-linear-gradient(0deg, ${c(a)} 0, ${c(a)} 1px, transparent 1px, transparent 6px)`, size: 'auto'})},
    hatch: {label: 'Diagonal hatch', make: a => ({image: `repeating-linear-gradient(45deg, ${c(a)} 0, ${c(a)} 1px, transparent 1px, transparent 10px)`, size: 'auto'})},
    hex:   {label: 'Honeycomb', make: a => ({image: svg(56, 100, `<g fill='none' stroke='${c(a * 1.4)}' stroke-width='1'><path d='M28 66L0 50L0 16L28 0L56 16L56 50L28 66L28 100'/><path d='M28 0L28 -34L0 -50L-28 -34M28 0L28 -34L56 -50L84 -34'/><path d='M28 100L0 116M28 100L56 116'/></g>`), size: '56px 100px'})},
    tri:   {label: 'Triangles', make: a => ({image: svg(60, 52, `<g fill='none' stroke='${c(a * 1.3)}' stroke-width='1'><path d='M0 52L30 0L60 52Z M30 0L60 52M0 52H60'/></g>`), size: '60px 52px'})},
    gyroid:{label: 'Gyroid', make: a => ({image: svg(100, 100, `<g fill='none' stroke='${c(a * 1.5)}' stroke-width='1.2' stroke-linecap='round'><path d='${GYR_A}'/><path d='${GYR_B}'/></g>`), size: '100px 100px'})},
    cubic: {label: 'Cubic', make: a => ({image: svg(CUBE_W, 96, `<path d='${CUBE}' fill='none' stroke='${c(a * 1.4)}' stroke-width='1'/>`), size: `${CUBE_W.toFixed(2)}px 96px`})},
    concentric:{label: 'Concentric', make: a => ({image: svg(48, 48, `<g fill='none' stroke='${c(a * 1.5)}' stroke-width='1'>${nest(48, 8)}</g>`), size: '48px 48px'})},
    hilbert:{label: 'Hilbert curve', make: a => ({image: svg(56, 56, `<path d='${HIL}' fill='none' stroke='${c(a * 1.6)}' stroke-width='1.2' stroke-linejoin='round'/>`), size: '56px 56px'})},
    plus:  {label: 'Plus marks', make: a => ({image: svg(32, 32, `<g stroke='${c(a * 2)}' stroke-width='1'><path d='M16 12V20M12 16H20'/></g>`), size: '32px 32px'})},
    glow:  {label: 'Soft glow (no pattern)', make: a => ({image: `radial-gradient(1200px 500px at 50% -80px, rgba(96,165,250,${a * 3}), transparent 70%)`, size: 'auto'})},
    dotsglow:{label: 'Dots + glow', make: a => ({image: `radial-gradient(1200px 500px at 50% -80px, rgba(96,165,250,${a * 3}), transparent 70%), radial-gradient(${c(a)} 1.2px, transparent 1.6px)`, size: 'auto, 22px 22px'})},
  };

  let cur = 'grid', alpha = 0.06, fade = true;
  const bar = document.createElement('div');
  bar.className = 'bg-bar';
  bar.innerHTML = `<b>Background</b><span class="mock-seg" id="bgseg">${Object.entries(PATTERNS).map(([k, p]) => `<button data-p="${k}">${p.label}</button>`).join('')}</span>
    <label>Strength <input type="range" id="bgalpha" min="2" max="16" value="6"> <output id="bgout">6</output></label>
    <label><input type="checkbox" id="bgfade" checked> Fade out toward the bottom</label>`;
  const sw = document.querySelector('.mock-switch');
  sw.parentNode.insertBefore(bar, sw.nextSibling);

  function apply() {
    const p = PATTERNS[cur].make(alpha);
    const b = document.body.style;
    b.backgroundImage = p.image || 'none';
    b.backgroundSize = p.size || 'auto';
    b.backgroundRepeat = 'repeat';
    // fade: mask the pattern layer by drawing it on a pseudo layer instead of the body
    document.documentElement.classList.toggle('bg-fade', fade && cur !== 'cur' && cur !== 'glow');
    document.querySelectorAll('#bgseg button').forEach(x => x.classList.toggle('on', x.dataset.p === cur));
    document.getElementById('bgout').textContent = Math.round(alpha * 100);
  }
  bar.addEventListener('click', e => { const k = e.target.dataset && e.target.dataset.p; if (k) { cur = k; apply(); } });
  document.getElementById('bgalpha').oninput = e => { alpha = e.target.value / 100; apply(); };
  document.getElementById('bgfade').onchange = e => { fade = e.target.checked; apply(); };
  apply();

  /* ---- Progress bar colour ---- */
  const SWATCHES = [['Green (current)', ''], ['Blue', '#3b82f6'], ['Cyan', '#06b6d4'], ['Teal', '#14b8a6'], ['Purple', '#a855f7'], ['Pink', '#ec4899'], ['Orange', '#f97316'], ['Red', '#ef4444'], ['White', '#e5e7eb']];
  let color = '', style = 'solid', keepAmber = true, stripe = 4; // stripe 1..8 -> how much darker the second stripe is
  const pbar = document.createElement('div');
  pbar.className = 'bg-bar';
  pbar.innerHTML = `<b>Progress bar</b><span class="mock-seg" id="pcolors">${SWATCHES.map(([l, v]) => `<button data-c="${v}" title="${l}"><i class="sw-dot" style="background:${v || 'var(--success)'}"></i>${l.replace(' (current)', '')}</button>`).join('')}</span>
    <label>Custom <input type="color" id="pcustom" value="#3b82f6"></label>
    <span class="mock-seg" id="pstyle"><button data-s="solid">Solid</button><button data-s="gradient">Gradient</button><button data-s="striped">Striped</button></span>
    <label id="pstripewrap">Stripe strength <input type="range" id="pstripe" min="1" max="8" value="4"> <output id="pstripeout">4</output></label>
    <label><input type="checkbox" id="pamber" checked> Keep amber when paused</label>`;
  bar.parentNode.insertBefore(pbar, bar.nextSibling);

  function shade(hex, amt) { // lighten (+) or darken (-) a #rrggbb colour
    const n = parseInt(hex.slice(1), 16), f = v => Math.max(0, Math.min(255, Math.round(v + (amt > 0 ? (255 - v) * amt : v * amt))));
    return `rgb(${f(n >> 16)},${f((n >> 8) & 255)},${f(n & 255)})`;
  }
  function applyProg() {
    const r = document.documentElement.style;
    // "Green (current)" is the theme's own success colour: striped / gradient need its real value.
    const base = color || getComputedStyle(document.documentElement).getPropertyValue('--success').trim();
    const active = !!color || style !== 'solid';
    if (!active || !/^#[0-9a-f]{6}$/i.test(base)) {
      r.removeProperty('--prog-bg');
    } else {
      const n = parseInt(base.slice(1), 16), lum = (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
      // two clearly different tones: the base against a noticeably darker one (white: grey)
      const alt = shade(base, -(stripe * 0.075) * (lum > 0.85 ? 0.85 : 1)); // 1 = faint ... 8 = bold
      r.setProperty('--prog-bg', style === 'gradient' ? `linear-gradient(90deg, ${shade(base, -.25)}, ${base} 60%, ${shade(base, lum > 0.7 ? 0 : .25)})`
        : style === 'striped' ? `repeating-linear-gradient(135deg, ${base} 0 10px, ${alt} 10px 20px)` : base);
    }
    document.documentElement.classList.toggle('prog-custom', active);
    document.documentElement.classList.toggle('prog-paused-too', !keepAmber);
    document.documentElement.classList.toggle('prog-striped', active && style === 'striped');
    document.querySelectorAll('#pcolors button').forEach(b => b.classList.toggle('on', b.dataset.c === color));
    document.querySelectorAll('#pstyle button').forEach(b => b.classList.toggle('on', b.dataset.s === style));
    document.getElementById('pstripewrap').style.display = style === 'striped' ? '' : 'none';
  }
  pbar.addEventListener('click', e => {
    const b = e.target.closest('button'); if (!b) return;
    if (b.dataset.c !== undefined) color = b.dataset.c;
    if (b.dataset.s) style = b.dataset.s;
    applyProg();
  });
  document.getElementById('pcustom').oninput = e => { color = e.target.value; applyProg(); };
  document.getElementById('pstripe').oninput = e => { stripe = +e.target.value; document.getElementById('pstripeout').textContent = stripe; applyProg(); };
  document.getElementById('pamber').onchange = e => { keepAmber = e.target.checked; applyProg(); };
  applyProg();
})();

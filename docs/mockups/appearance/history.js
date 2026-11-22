/* History page mockup: per-printer print history as a full page. Static data, read-only like today. */
let HP = 'Core One', hFilter = 'all', hQ = '', hPage = 0;
const H_PAGE = 6; // real app uses 20 per page; smaller here so paging is visible

const H = (id, n, result, date, dur, est, tools, o = {}) => ({ id, n, result, date, dur, est, tools, layer: '0.2', fill: '15%', g: null, cost: null, changes: 0, img: id % 2 ? 'plate-coreone.png' : 'plate-mk4s.png', ...o });

const HIST = {
  'Core One': [
    H(1, 'benchy_0.4n_0.2mm_PETG_COREONE_38m.bgcode', 'completed', 'Nov 18, 2026', 2310, 2280, [['PETG', 1]], { g: 14, cost: 0.35 }),
    H(2, 'phone_stand_v3_0.4n_0.2mm_PLA_COREONE_2h05m.bgcode', 'completed', 'Nov 17, 2026', 7620, 7500, [['PLA', 1]], { g: 62, cost: 1.4 }),
    H(3, 'gear_set_0.4n_0.15mm_PETG_COREONE_4h20m.bgcode', 'failed', 'Nov 15, 2026', 5400, 15600, [['PETG', 1]], { layer: '0.15', fill: '20%', g: 38 }),
    H(4, 'bracket_x4_0.4n_0.2mm_PETG_COREONE_1h12m.bgcode', 'cancelled', 'Nov 14, 2026', 900, 4320, [['PETG', 1]], { g: 6, noimg: true }),
    H(5, 'vase_spiral_0.4n_0.2mm_PETG_COREONE_1h33m.bgcode', 'completed', 'Nov 12, 2026', 5640, 5580, [['PETG', 1]], { fill: '0%', g: 41, cost: 0.98 }),
    H(6, 'vase_spiral_0.4n_0.2mm_PETG_COREONE_1h33m.bgcode', 'completed', 'Nov 10, 2026', 5610, 5580, [['PETG', 1]], { fill: '0%', g: 41, cost: 0.98 }),
    H(7, 'calibration_cube_0.4n_0.2mm_PLA_COREONE_12m.bgcode', 'completed', 'Nov 9, 2026', 760, 720, [['PLA', 1]], { g: 4 }),
    H(8, 'cable_clip_x12_0.4n_0.2mm_PLA_COREONE_58m.bgcode', 'completed', 'Nov 6, 2026', 3540, 3480, [['PLA', 1]], { g: 22, cost: 0.51 }),
    H(9, 'drawer_organizer_0.6n_0.3mm_PLA_COREONE_6h41m.bgcode', 'failed', 'Nov 4, 2026', 11200, 24060, [['PLA', 1]], { layer: '0.3', g: 96, cost: 2.2 })
  ],
  'MK4S': [
    H(21, 'multicolor_logo_0.4n_0.2mm_MK4SMMU3_3h02m.bgcode', 'completed', 'Nov 19, 2026', 11340, 10920, [['PLA', 1], ['PETG', 3], ['PLA', 4]], { g: 58, cost: 1.5, changes: 41 }),
    H(22, 'planter_set_0.4n_0.2mm_PLA_MK4SMMU3_5h10m.bgcode', 'completed', 'Nov 16, 2026', 18720, 18600, [['PLA', 1], ['PLA', 2]], { g: 142, cost: 3.2, changes: 6 }),
    H(23, 'flexi_dragon_0.4n_0.2mm_TPU_MK4SMMU3_2h48m.bgcode', 'failed', 'Nov 14, 2026', 3200, 10080, [['TPU', 5]], { fill: '10%', g: 21 }),
    H(24, 'chess_pieces_0.4n_0.15mm_PLA_MK4SMMU3_8h22m.bgcode', 'cancelled', 'Nov 11, 2026', 1800, 30120, [['PLA', 1], ['PETG', 2]], { layer: '0.15', changes: 3 }),
    H(25, 'planter_set_0.4n_0.2mm_PLA_MK4SMMU3_5h10m.bgcode', 'completed', 'Nov 8, 2026', 18800, 18600, [['PLA', 1], ['PLA', 2]], { g: 142, cost: 3.2, changes: 6 }),
    H(26, 'benchy_0.4n_0.2mm_PLA_MK4S_40m.bgcode', 'completed', 'Nov 2, 2026', 2460, 2400, [['PLA', 1]], { g: 15 }),
    H(27, 'test_tower_0.2mm_PLA_MK4S_20m.bgcode', 'completed', 'Oct 28, 2026', 1260, 1200, [['PLA', 1]], { g: 7 })
  ],
  _default: [
    H(41, 'benchy_0.2mm_PLA.gcode', 'completed', 'Nov 19, 2026', 2700, 2640, [['PLA', 1]], { g: 15, noimg: true }),
    H(42, 'bracket_v2_0.2mm_PETG.gcode', 'completed', 'Nov 17, 2026', 4100, 4000, [['PETG', 1]], { g: 28, noimg: true }),
    H(43, 'test_tower_0.2mm_PLA.gcode', 'failed', 'Nov 13, 2026', 900, 3900, [['PLA', 1]], { g: 5, noimg: true })
  ]
};
const histFor = (n) => HIST[n] || HIST._default;
const fmtDur = (s) => { if (!s) return '0m'; const h = Math.floor(s / 3600), m = Math.round((s % 3600) / 60); return h > 0 ? `${h}h${m}m` : `${m}m`; };

function hStatus(h) {
  return h.result === 'failed' ? '<span class="fchip bad">Failed</span>' : h.result === 'cancelled' ? '<span class="fchip">Cancelled</span>' : '<span class="fchip ok">Completed</span>';
}
function hDetails(h) {
  const bits = [`Layer ${h.layer}mm`, `Fill ${h.fill}`];
  if (h.g) bits.push(`Filament ${h.g}g${h.cost ? ` ($${h.cost.toFixed(2)})` : ''}`);
  if (h.changes) bits.push(`${h.changes} tool changes`);
  return bits.join(' · ');
}
function hRow(h) {
  return `<div class="f-row h-row">
    <div class="f-thumb">${h.noimg ? '<div class="nothumb">No preview</div>' : `<img src="${h.img}" alt="">`}</div>
    <div class="f-info">
      <span class="f-name" title="${h.n}">${h.n}</span>
      <span class="f-meta">${hStatus(h)}<span class="f-date">${h.date}</span></span>
      <span class="f-tools">${h.tools.map(matChip).join('')}<span class="h-det">${hDetails(h)}</span></span>
    </div>
    <div class="h-dur"><b>${fmtDur(h.dur)}</b><small>est. ${fmtDur(h.est)}</small></div>
  </div>`;
}

function hVisible() {
  return histFor(HP).filter(h => (hFilter === 'all' || h.result === hFilter) && (!hQ || h.n.toLowerCase().includes(hQ.toLowerCase())));
}
function hSummary() {
  const a = histFor(HP), done = a.filter(h => h.result === 'completed').length, secs = a.reduce((s, h) => s + h.dur, 0);
  return { count: a.length, hours: (secs / 3600).toFixed(1), rate: a.length ? Math.round(done / a.length * 100) : 0 };
}
function hBodyHTML() {
  if (printerState(HP) === 'offline') { /* history is stored in PrintSpy, so it still works offline */ }
  const v = hVisible(), pages = Math.max(1, Math.ceil(v.length / H_PAGE));
  if (hPage >= pages) hPage = pages - 1;
  const cnt = document.getElementById('hist-count'); if (cnt) cnt.textContent = v.length + (v.length === 1 ? ' print' : ' prints');
  if (!v.length) return `<div class="f-empty"><b>${hQ || hFilter !== 'all' ? 'No prints match' : 'No print history yet'}</b><span>${hQ || hFilter !== 'all' ? 'Try clearing the search or filter.' : 'Finished prints show up here.'}</span></div>`;
  const slice = v.slice(hPage * H_PAGE, hPage * H_PAGE + H_PAGE);
  return `<div class="f-list">${slice.map(hRow).join('')}</div>
    <div class="h-pager"><button class="btn btn-sm" ${hPage === 0 ? 'disabled' : ''} onclick="hPage--;render()">&#8592; Prev</button><span>Page ${hPage + 1} of ${pages}</span><button class="btn btn-sm" ${hPage >= pages - 1 ? 'disabled' : ''} onclick="hPage++;render()">Next &#8594;</button></div>`;
}

function historyHTML() {
  const printers = DATA.map(p => `<button class="fleet-chip ${p.state} ${p.name === HP ? 'on' : ''}" onclick="HP='${p.name}';hPage=0;render()"><span class="dot"></span>${p.name}</button>`).join('');
  const seg = (cur, key, opts) => `<div class="seg-pills compact">${opts.map(([v, l]) => `<button class="webcam-mode-btn ${cur === v ? 'active' : ''}" onclick="${key}='${v}';hPage=0;render()">${l}</button>`).join('')}</div>`;
  const s = hSummary(), n = hVisible().length;
  return `<div class="set-title"><h2><button class="crumb" onclick="go('a')">Dashboard</button> <span class="sep">&rsaquo;</span> History</h2></div>
    <div class="fleet">${printers}</div>
    <div class="h-stats">
      <div class="h-stat"><span>Prints</span><b>${s.count}</b></div>
      <div class="h-stat"><span>Time printed</span><b>${s.hours}<small>h</small></b></div>
      <div class="h-stat"><span>Success rate</span><b>${s.rate}<small>%</small></b><i class="h-bar"><u style="width:${s.rate}%"></u></i></div>
    </div>
    <div class="f-toolbar">
      <div class="inp search"><span class="mag">&#9906;</span><input type="text" placeholder="Search history" value="${hQ.replace(/"/g, '&quot;')}" oninput="hQ=this.value;hPage=0;document.getElementById('hist-body').innerHTML=hBodyHTML()"></div>
      ${seg(hFilter, 'hFilter', [['all', 'All'], ['completed', 'Completed'], ['failed', 'Failed'], ['cancelled', 'Cancelled']])}
      <button class="btn btn-sm btn-dash" style="margin-left:auto" onclick="go('a')">${ICON_LEFT} Dashboard</button>
    </div>
    <div class="f-count" id="hist-count">${n} ${n === 1 ? 'print' : 'prints'}</div>
    <div id="hist-body">${hBodyHTML()}</div>`;
}
function openHistory(name) { HP = name; hQ = ''; hFilter = 'all'; hPage = 0; go('history'); }

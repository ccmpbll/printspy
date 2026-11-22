/* Files page mockup: per-printer file manager as a full page. Static data, nothing is wired up. */
let FP = 'Core One', fFilter = 'all', fSort = 'new', fQ = '', fLayout = 'list';

const F = (id, n, ts, d, printed, last, tools, o = {}) => ({ id, n, ts, d, printed, last, tools, store: 'local', img: id % 2 ? 'plate-coreone.png' : 'plate-mk4s.png', ...o });

const FILES = {
  'Core One': [
    F(1, 'allparts_m_joints-v1_0.4n_0.15mm_PETG_COREONE_1h14m.bgcode', 21.5, 'Today, 11:52 AM', 0, null, [['PETG', 1]], { now: true }),
    F(2, 'benchy_0.4n_0.2mm_PETG_COREONE_38m.bgcode', 18.4, 'Nov 18, 6:12 PM', 3, true, [['PETG', 1]]),
    F(3, 'phone_stand_v3_0.4n_0.2mm_PLA_COREONE_2h05m.bgcode', 17.2, 'Nov 17, 9:40 AM', 1, true, [['PLA', 1]]),
    F(4, 'gear_set_0.4n_0.15mm_PETG_COREONE_4h20m.bgcode', 15.8, 'Nov 15, 2:03 PM', 1, false, [['PETG', 1]]),
    F(5, 'cable_clip_x12_0.4n_0.2mm_PLA_COREONE_58m.bgcode', 20.1, 'Nov 20, 4:27 PM', 0, null, [['PLA', 1]]),
    F(6, 'drawer_organizer_0.6n_0.3mm_PLA_COREONE_6h41m.bgcode', 19.3, 'Nov 19, 8:15 PM', 0, null, [['PLA', 1]]),
    F(7, 'calibration_cube_0.4n_0.2mm_PLA_COREONE_12m.bgcode', 9.6, 'Nov 9, 7:30 PM', 5, true, [['PLA', 1]]),
    F(8, 'vase_spiral_0.4n_0.2mm_PETG_COREONE_1h33m.bgcode', 12.1, 'Nov 12, 1:48 PM', 2, true, [['PETG', 1]])
  ],
  'MK4S': [
    F(11, 'allparts_m_body-v1_0.4n_0.15mm_PLA_MK4SMMU3_2h16m.bgcode', 21.4, 'Today, 10:46 AM', 0, null, [['PLA', 1]], { now: true }),
    F(12, 'multicolor_logo_0.4n_0.2mm_MK4SMMU3_3h02m.bgcode', 20.2, 'Nov 20, 7:05 PM', 0, null, [['PLA', 1], ['PETG', 3], ['PLA', 4]]),
    F(13, 'planter_set_0.4n_0.2mm_PLA_MK4SMMU3_5h10m.bgcode', 16.5, 'Nov 16, 3:30 PM', 2, true, [['PLA', 1], ['PLA', 2]]),
    F(14, 'flexi_dragon_0.4n_0.2mm_TPU_MK4SMMU3_2h48m.bgcode', 14.0, 'Nov 14, 11:10 AM', 1, false, [['TPU', 5]]),
    F(15, 'chess_pieces_0.4n_0.15mm_PLA_MK4SMMU3_8h22m.bgcode', 11.3, 'Nov 11, 9:00 AM', 0, null, [['PLA', 1], ['PETG', 2]])
  ],
  _default: [
    F(21, 'benchy_0.2mm_PLA.gcode', 19.1, 'Nov 19, 5:20 PM', 4, true, [['PLA', 1]]),
    F(22, 'bracket_v2_0.2mm_PETG.gcode', 17.7, 'Nov 17, 1:02 PM', 0, null, [['PETG', 1]]),
    F(23, 'test_tower_0.2mm_PLA.gcode', 13.2, 'Nov 13, 8:44 PM', 1, false, [['PLA', 1]])
  ]
};
const filesFor = (name) => FILES[name] || FILES._default;
const printerState = (name) => (DATA.find(p => p.name === name) || {}).state;

/* ---- bits ---- */
const matChip = ([m, t]) => `<span class="mat" title="Tool ${t}">T${t} ${m}</span>`;
function fStatus(f) {
  if (f.now) return '<span class="fchip now">Printing now</span>';
  if (f.last === false) return '<span class="fchip bad">Failed</span>';
  if (f.printed > 0) return `<span class="fchip ok">${f.printed}x printed</span>`;
  return '<span class="fchip new">New</span>';
}
const thumbHTML = (f) => f.img ? `<img src="${f.img}" alt="">` : '<div class="ph"></div>';

function fActions(f) {
  const lbl = f.printed > 0 ? '&#8634; Reprint' : '<span class="icon-play"></span> Print';
  return `<div class="f-actions">
    <button class="btn btn-sm btn-acc" ${f.now ? 'disabled title="Printing now"' : ''} onclick="fConfirm(this,'print',${f.id})">${lbl}</button>
    <button class="btn btn-sm">&#8595; Download</button>
    <button class="btn btn-sm btn-danger" ${f.now ? 'disabled' : ''} onclick="fConfirm(this,'delete',${f.id})">Delete</button>
  </div>`;
}
function fRow(f) {
  return `<div class="f-row">
    <div class="f-thumb">${thumbHTML(f)}</div>
    <div class="f-info">
      <span class="f-name" title="${f.n}">${f.n}</span>
      <span class="f-meta">${fStatus(f)}<span class="f-date">${f.d}</span></span>
      <span class="f-tools">${f.tools.map(matChip).join('')}</span>
    </div>
    ${fActions(f)}
  </div>`;
}
function fCard(f) {
  return `<div class="f-card">
    <div class="f-cardimg">${thumbHTML(f)}<span class="f-cardstat">${fStatus(f)}</span></div>
    <div class="f-cardbody">
      <span class="f-name" title="${f.n}">${f.n}</span>
      <span class="f-meta"><span class="f-date">${f.d}</span></span>
      <span class="f-tools">${f.tools.map(matChip).join('')}</span>
    </div>
    ${fActions(f)}
  </div>`;
}

function fVisible() {
  let a = filesFor(FP).filter(f =>
    (fFilter === 'all' || (fFilter === 'new' && !f.printed && f.last !== false && !f.now) || (fFilter === 'printed' && f.printed > 0 && f.last !== false) || (fFilter === 'failed' && f.last === false)) &&
    (!fQ || f.n.toLowerCase().includes(fQ.toLowerCase())));
  a = a.slice().sort((x, y) => fSort === 'name' ? x.n.localeCompare(y.n) : fSort === 'printed' ? y.printed - x.printed : y.ts - x.ts);
  return a;
}
function fListHTML() {
  const st = printerState(FP);
  if (st === 'offline') { const c0 = document.getElementById('files-count'); if (c0) c0.textContent = ''; return `<div class="f-empty"><b>Can't reach ${FP}</b><span>Files are read from the printer, so it has to be online. Last seen 14 minutes ago.</span><button class="btn btn-sm" onclick="render()">Try again</button></div>`; }
  const v = fVisible();
  const cnt = document.getElementById('files-count'); if (cnt) cnt.textContent = v.length + (v.length === 1 ? ' file' : ' files');
  if (!v.length) return `<div class="f-empty"><b>${fQ ? 'No files match "' + fQ.replace(/</g, '&lt;') + '"' : 'No files here'}</b><span>${fQ || fFilter !== 'all' ? 'Try clearing the search or filters.' : 'Upload a file to get started.'}</span></div>`;
  return fLayout === 'grid' ? `<div class="f-grid">${v.map(fCard).join('')}</div>` : `<div class="f-list">${v.map(fRow).join('')}</div>`;
}

/* ---- page ---- */
function filesHTML() {
  const printers = DATA.map(p => `<button class="fleet-chip ${p.state} ${p.name === FP ? 'on' : ''}" onclick="FP='${p.name}';render()"><span class="dot"></span>${p.name}</button>`).join('');
  const seg = (cur, key, opts) => `<div class="seg-pills compact">${opts.map(([v, l]) => `<button class="webcam-mode-btn ${cur === v ? 'active' : ''}" onclick="${key}='${v}';render()">${l}</button>`).join('')}</div>`;
  const n = fVisible().length, off = printerState(FP) === 'offline';
  return `<div class="set-title"><h2><button class="crumb" onclick="go('a')">Dashboard</button> <span class="sep">&rsaquo;</span> Files</h2></div>
    <div class="fleet">${printers}</div>
    <div class="f-toolbar">
      <div class="inp search"><span class="mag">&#9906;</span><input type="text" placeholder="Search files" value="${fQ.replace(/"/g, '&quot;')}" oninput="fQ=this.value;document.getElementById('files-body').innerHTML=fListHTML()"></div>
      ${seg(fFilter, 'fFilter', [['all', 'All'], ['new', 'New'], ['printed', 'Printed'], ['failed', 'Failed']])}
      <div class="inp sel mini"><select onchange="fSort=this.value;render()"><option value="new" ${fSort === 'new' ? 'selected' : ''}>Newest first</option><option value="name" ${fSort === 'name' ? 'selected' : ''}>Name</option><option value="printed" ${fSort === 'printed' ? 'selected' : ''}>Most printed</option></select></div>
      ${seg(fLayout, 'fLayout', [['list', 'List'], ['grid', 'Grid']])}
      ${(typeof PTYPES !== 'undefined' && PTYPES[FP] === 'octoprint') ? `<button class="btn btn-sm btn-plain" style="margin-left:auto" disabled title="Uploads are only supported for PrusaLink printers">${ICON_UP} Upload to ${FP}</button>` : `<button class="btn btn-sm btn-plain" style="margin-left:auto" onclick="openUpload('${FP}')">${ICON_UP} Upload to ${FP}</button>`}
      <button class="btn btn-sm btn-dash" onclick="go('a')">${ICON_LEFT} Dashboard</button>
    </div>
    <div class="f-count" id="files-count">${off ? '' : n + (n === 1 ? ' file' : ' files')}</div>
    <div id="files-body">${fListHTML()}</div>`;
}
function openFiles(name) { FP = name; fQ = ''; fFilter = 'all'; go('files'); }

function fConfirm(btn, kind, id) {
  if (btn.dataset.armed) {
    delete btn.dataset.armed;
    if (kind === 'delete') { const a = filesFor(FP); const i = a.findIndex(f => f.id === id); if (i >= 0) a.splice(i, 1); render(); }
    else { btn.innerHTML = 'Sent &#10003;'; btn.disabled = true; }
    return;
  }
  btn.dataset.armed = 1; btn.dataset.orig = btn.innerHTML; btn.textContent = 'Sure?';
  setTimeout(() => { if (btn.isConnected && btn.dataset.armed) { delete btn.dataset.armed; btn.innerHTML = btn.dataset.orig; } }, 3000);
}

/* Settings page mockup: full page instead of the long modal. Static, nothing is wired up. */
let setTab = 'printers';

const SET_TABS = [
  ['printers', 'Printers', ''],
  ['devices', 'Plugs & cameras', ''],
  ['general', 'General', ''],
  ['notify', 'Notifications', ''],
  ['integrations', 'Integrations', ''],
  ['account', 'Account & backup', '']
];

/* ---- small building blocks ---- */
const fld = (label, value, o = {}) => `
  <div class="fld ${o.wide ? 'wide' : ''}">
    <label>${label}</label>
    <div class="inp ${o.unit ? 'has-unit' : ''}"><input type="${o.type || 'text'}" value="${value}" placeholder="${o.ph || ''}" ${o.id ? `id="${o.id}"` : ''} ${o.oninput ? `oninput="${o.oninput}"` : ''}>${o.unit ? `<span class="unit">${o.unit}</span>` : ''}</div>
    ${o.hint ? `<small>${o.hint}</small>` : ''}
  </div>`;
const swrow = (title, hint, on, extra = '') => `
  <div class="swrow"><span class="swtext"><b>${title}</b>${hint ? `<small>${hint}</small>` : ''}</span>${extra}<span class="sw ${on ? 'on' : ''}" onclick="this.classList.toggle('on')"></span></div>`;
const card = (title, sub, body, foot = '') => `
  <div class="set-card"><div class="set-card-head"><h3>${title}</h3>${sub ? `<p>${sub}</p>` : ''}</div>${body}${foot ? `<div class="set-foot">${foot}</div>` : ''}</div>`;
const pillBtn = (label, cls = '') => `<button class="btn btn-sm ${cls}">${label}</button>`;
const SAVE = pillBtn('Save', 'btn-primary');

function rowItem(name, meta, chip = '', dot = '', kind = '', idx = 0) {
  return `<div class="set-row">
    ${dot ? `<span class="dotst ${dot}"></span>` : ''}
    <div class="set-row-info"><span class="set-row-name">${name}${chip ? `<span class="chip">${chip}</span>` : ''}</span><span class="set-row-meta">${meta}</span></div>
    <div class="set-row-actions"><button class="btn btn-sm" onclick="openEdit('${kind}',${idx})">&#9998; Edit</button><button class="btn btn-sm btn-danger" onclick="confirmBtn(this)">Delete</button></div>
  </div>`;
}

/* ---- tabs ---- */
function toggleMaint(i) {
  const p = DATA[i];
  if (p.state === 'maintenance') { p.state = p.prevState || 'idle'; }
  else { p.prevState = p.state; p.state = 'maintenance'; }
  render();
}
function tabPrinters() {
  if (demo === 'empty') return card('Printers', 'Order here is the order on the dashboard.', '<div class="set-empty">No printers configured yet.</div>', `<button class="btn btn-sm btn-primary" onclick="openEdit('printer',-1)">+ Add printer</button>`);
  const types = { 'Core One': 'PrusaLink', 'MK4S': 'PrusaLink', 'Ender 3': 'OctoPrint', 'Mini+': 'PrusaLink', 'XL': 'PrusaLink', 'Voron 2.4': 'OctoPrint', 'MK3S': 'PrusaLink' };
  const rows = DATA.map((p, i) => {
    const kind = types[p.name] || 'PrusaLink', pl = kind === 'PrusaLink', maint = p.state === 'maintenance';
    return `<div class="set-row">
      <span class="reorder"><button ${i === 0 ? 'disabled' : ''} title="Move up">&#9650;</button><button ${i === DATA.length - 1 ? 'disabled' : ''} title="Move down">&#9660;</button></span>
      <span class="dotst ${p.state}"></span>
      <div class="set-row-info"><span class="set-row-name">${p.name}<span class="chip">${kind}</span></span><span class="set-row-meta">${p.model} &middot; http://192.168.1.${40 + i}</span></div>
      <div class="set-row-actions">
        <button class="btn btn-sm" onclick="openEdit('printer',${i})">&#9998; Edit</button>
        ${pl ? `<button class="btn btn-sm" onclick="openRawApi('${p.name}')" title="Raw API debug view">Raw API</button>` : `<button class="btn btn-sm raw-ph" tabindex="-1" aria-hidden="true">Raw API</button>`}
        <a class="btn btn-sm" href="#" onclick="event.preventDefault()" title="Open ${kind}">${kind} &#8599;</a>
        <button class="btn btn-sm btn-maintenance ${maint ? 'active' : ''}" onclick="toggleMaint(${i})" title="${maint ? 'End maintenance' : 'Mark as in maintenance'}">Maintenance</button>
        <button class="btn btn-sm btn-danger" onclick="confirmBtn(this)">Delete</button>
      </div>
    </div>`;
  }).join('');
  return card('Printers', 'Order here is the order on the dashboard.', `<div class="set-list">${rows}</div>`, `<button class="btn btn-sm btn-primary" onclick="openEdit('printer',-1)">+ Add printer</button>`);
}

function tabDevices() {
  const addBtn = (kind, label) => `<button class="btn btn-sm btn-primary" onclick="openEdit('${kind}',-1)">${label}</button>`;
  return card('Smart plugs (Tasmota)', 'Talk directly to a Tasmota device, independent of any auto-detected plugins. Add a plug, then assign it to any printer.',
      `<div class="set-list">${PLUGS.map((p, i) => rowItem(p.label, `${p.mode === 'mqtt' ? 'MQTT: ' + p.topic : p.ip}:${p.idx} &middot; ${p.printer || 'Unassigned'}${p.hide ? ' &middot; label hidden' : ''}`, '', '', 'plug', i)).join('')}</div>`, addBtn('plug', '+ Add plug'))
    + card('Cameras (printspy-cam)', 'Standalone printspy-cam device, independent of printer type. Add one, then assign it to any printer.',
      `<div class="set-list">${CAMS.map((c, i) => rowItem(c.name, `${c.url} &middot; ${c.printer || 'Unassigned'}`, '', '', 'camera', i)).join('')}</div>`, addBtn('camera', '+ Add camera'))
    + card('Slicer targets', 'Lets PrusaSlicer/OrcaSlicer\'s "Send to printer" (Physical Printer, PrusaLink mode) stage a file here for a model bucket. Dispatch a staged job to a specific printer from its dashboard card.',
      `<div class="set-list">${TARGS.map((t, i) => rowItem(t.label, `Printer: ${t.printer} &middot; Host: https://printspy.example/ingest/${t.label}`, '', '', 'target', i)).join('')}</div>`, addBtn('target', '+ Add slicer target'));
}

function tabGeneral() {
  return card('Dashboard', '', `<div class="grid2">
        ${fld('Snapshot refresh interval', '10', { unit: 'seconds', hint: 'How often webcam snapshots refresh. Lower values use more bandwidth.' })}
      </div>
      ${swrow('Hide camera while a printer is unreachable', 'Removes the image box and mode buttons from a card only while that printer is offline or disconnected. Applies to all printers.', false)}`, SAVE)
    + card('Polling & control', '', `<div class="grid2">
        ${fld('Poll interval', '', { unit: 'seconds', ph: 'Per-printer default', hint: 'Global override. Leave empty to use per-printer settings.' })}
        ${fld('Print control timeout', '15', { unit: 'seconds', hint: 'How long to wait for the printer to confirm Pause/Resume/Cancel before falling back to the next poll.' })}
        ${fld('PrusaLink keepalive ping', '', { unit: 'seconds', ph: '0 = disabled', hint: 'ICMP ping to stop an idle PrusaLink wifi interface from dropping off the network.' })}
      </div>`, SAVE)
    + card('Power safety', 'Applies to printers with a directly-assigned smart plug.', `<div class="grid2">
        ${fld('Auto-off after idle', '', { unit: 'minutes', ph: '0 = disabled', hint: 'Global override for how long a printer can sit idle before its power is cut.' })}
        ${fld('Auto-off cooldown temp', '40', { unit: '&deg;C', hint: 'Power isn\'t cut until hotend and bed both drop below this.' })}
        ${fld('Thermal runaway: max bed temp', '', { unit: '&deg;C', ph: '0 = disabled', hint: 'Cuts power if the bed exceeds this. A second layer on top of firmware protection, not a replacement.' })}
        ${fld('Thermal runaway: max extruder temp', '', { unit: '&deg;C', ph: '0 = disabled', hint: 'Same as above, for the hotend.' })}
      </div>`, SAVE)
    + card('History & logging', '', `<div class="grid2">
        ${fld('Print history retention', '', { unit: 'days', ph: '0 = keep forever', hint: 'Older entries are pruned the next time a print completes.' })}
      </div>
      ${swrow('Debug logging', 'Verbose tracing for polling, MQTT, smart plugs and API requests (docker logs -f printspy). Takes effect immediately. Leave off unless troubleshooting.', false)}`, SAVE);
}

/* ---- Notifications: per-event customization (Title, Message + placeholders, Sound, Image, High priority) ---- */
const SOUNDS = ['pushover','bike','bugle','cashregister','classical','cosmic','falling','gamelan','incoming','intermission','magic','mechanical','pianobar','siren','spacealarm','tugboat','alien','climb','persistent','echo','updown','vibrate','none'];
const NEV = {
  start:       { name: 'Print started', defT: 'Print started', defM: '{printer}: {file}', ph: ['printer','file'], defImg: 'thumbnail' },
  complete:    { name: 'Print complete', defT: 'Print complete', defM: '{printer}: {file} ({material}, {filament_g}g) - {duration}', ph: ['printer','file','material','filament_g','duration'], defImg: 'camera' },
  failed:      { name: 'Print failed', defT: 'Print failed', defM: '{printer}: {file} ({material}, {filament_g}g) - {duration}', ph: ['printer','file','material','filament_g','duration'], defImg: 'camera' },
  error:       { name: 'Printer error', sub: 'Thermal runaway, attention, etc.', defT: '{printer}: error', defM: '{message}', ph: ['printer','message'], defImg: 'camera' },
  checkpoint1: { name: 'Checkpoint 1', defT: 'Print checkpoint', defM: '{printer}: {file} reached {percent}%', ph: ['printer','file','percent'], defImg: 'camera', pct: 5 },
  checkpoint2: { name: 'Checkpoint 2', defT: 'Print checkpoint', defM: '{printer}: {file} reached {percent}%', ph: ['printer','file','percent'], defImg: 'camera', pct: 50 }
};
const NSTATE = {
  start:       { on: true,  title: '', msg: '', sound: '', image: '', high: false },
  complete:    { on: true,  title: 'Done: {printer}', msg: '{file} finished in {duration} ({filament_g}g {material})', sound: 'cosmic', image: 'camera', high: false },
  failed:      { on: true,  title: '', msg: '', sound: 'siren', image: '', high: true },
  error:       { on: true,  title: '', msg: '', sound: 'persistent', image: '', high: true },
  checkpoint1: { on: true,  title: '', msg: '', sound: '', image: 'none', high: false },
  checkpoint2: { on: false, title: '', msg: '', sound: '', image: '', high: false }
};
const SAMPLE = { printer: 'Core One', file: 'allparts_m_joints_PETG.bgcode', material: 'PETG', filament_g: '87', duration: '1h 14m', message: 'Thermal runaway: bed above limit', percent: '50' };
let nOpen = new Set(['complete']);

const fill = (t, pct) => t.replace(/\{(\w+)\}/g, (m, k) => (k === 'percent' && pct != null) ? String(pct) : (SAMPLE[k] ?? m));
function nImage(k) { const st = NSTATE[k]; return st.image || NEV[k].defImg; }
const IMG_LABEL = { camera: 'Camera snapshot', thumbnail: 'Plate thumbnail', none: 'No image' };

function nPreview(k) {
  const e = NEV[k], st = NSTATE[k], img = nImage(k), pct = e.pct;
  const title = fill(st.title || e.defT, pct), msg = fill(st.msg || e.defM, pct);
  return `<div class="pv">
    <div class="pv-top"><img src="${document.querySelector('.header-logo').getAttribute('src')}" alt=""><span>PRINTSPY</span><span class="pv-now">now</span></div>
    <div class="pv-title" id="pv-${k}-t">${title}</div>
    <div class="pv-msg" id="pv-${k}-m">${msg}</div>
    <div class="pv-img ${img}" id="pv-${k}-i">${img === 'none' ? '' : `<img src="${img === 'thumbnail' ? 'plate-coreone.png' : 'plate-mk4s.png'}" alt=""><span>${IMG_LABEL[img]}</span>`}</div>
    <div class="pv-meta" id="pv-${k}-x">${st.high ? '<b>High priority</b> · bypasses quiet hours · ' : ''}Sound: ${st.sound || 'app default'}</div>
  </div>`;
}
function nSummary(k) {
  const e = NEV[k], st = NSTATE[k], bits = [];
  if (st.title || st.msg) bits.push('<span class="chip acc">Custom text</span>');
  bits.push(`<span class="chip">${st.sound ? '&#9835; ' + st.sound : '&#9835; default'}</span>`);
  bits.push(`<span class="chip">${nImage(k) === 'none' ? 'No image' : nImage(k) === 'camera' ? 'Camera' : 'Thumbnail'}</span>`);
  if (st.high) bits.push('<span class="chip warn">High priority</span>');
  return bits.join('');
}
function nPanel(k) {
  const e = NEV[k], st = NSTATE[k];
  const imgOpt = (v, l) => `<button class="webcam-mode-btn ${st.image === v ? 'active' : ''}" onclick="nSet('${k}','image','${v}',true)">${l}</button>`;
  return `<div class="ev-panel">
    <div class="ev-form">
      <div class="fld"><label>Title</label><div class="inp"><input type="text" value="${st.title.replace(/"/g, '&quot;')}" placeholder="${e.defT}" oninput="nSet('${k}','title',this.value)"></div></div>
      <div class="fld"><label>Message</label><div class="inp"><input type="text" id="msg-${k}" value="${st.msg.replace(/"/g, '&quot;')}" placeholder="${e.defM}" oninput="nSet('${k}','msg',this.value)"></div>
        <div class="phrow"><small>Insert:</small>${e.ph.map(p => `<button class="phchip" onclick="nIns('${k}','${p}')">{${p}}</button>`).join('')}</div></div>
      <div class="ev-stack">
        <div class="fld"><label>Sound</label><div class="inp sel"><select onchange="nSet('${k}','sound',this.value,true)"><option value="">App default</option>${SOUNDS.map(x => `<option ${st.sound === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div></div>
        <div class="fld"><label>Image</label><div class="seg-pills">${imgOpt('', 'Default')}${imgOpt('camera', 'Camera')}${imgOpt('thumbnail', 'Thumbnail')}${imgOpt('none', 'None')}</div>
          <small>Default is ${e.defImg === 'camera' ? 'camera, falling back to the plate thumbnail' : 'the plate thumbnail'}.</small></div>
      </div>
      ${swrow('High priority', 'Bypasses your phone\'s quiet hours / Do Not Disturb.', st.high, '').replace('onclick="this.classList.toggle(\'on\')"', `onclick="this.classList.toggle('on');nSet('${k}','high',this.classList.contains('on'),true)"`)}
      <div class="ev-actions">${pillBtn('Send test')}${pillBtn('Reset to defaults', 'btn-quiet')}</div>
    </div>
    <div class="ev-preview"><div class="ev-prev-label">Preview <small>(sample values)</small></div>${nPreview(k)}</div>
  </div>`;
}
function nSet(k, f, v, redraw) { NSTATE[k][f] = v; if (redraw) { render(); return; }
  const e = NEV[k], st = NSTATE[k]; const t = document.getElementById('pv-' + k + '-t'), m = document.getElementById('pv-' + k + '-m');
  if (t) t.textContent = fill(st.title || e.defT, e.pct); if (m) m.textContent = fill(st.msg || e.defM, e.pct); }
function nIns(k, p) { const el = document.getElementById('msg-' + k); const st = NSTATE[k]; const cur = el.value || ''; el.value = (cur ? cur + ' ' : '') + '{' + p + '}'; nSet(k, 'msg', el.value); el.focus(); }
function nToggle(k) { nOpen.has(k) ? nOpen.delete(k) : nOpen.add(k); render(); }

function tabNotify() {
  const rows = Object.keys(NEV).map(k => {
    const e = NEV[k], st = NSTATE[k], open = nOpen.has(k);
    const pct = e.pct != null ? `<span class="inp has-unit tiny"><input type="number" value="${e.pct}"><span class="unit">%</span></span>` : '';
    return `<div class="ev ${open ? 'open' : ''} ${st.on ? '' : 'off'}">
      <div class="ev-row">
        <span class="sw ${st.on ? 'on' : ''}" onclick="nSet('${k}','on',!NSTATE['${k}'].on,true)"></span>
        <div class="ev-name"><b>${e.name}${e.pct != null ? ' at' : ''}</b>${pct}<small>${e.sub || ''}</small></div>
        <div class="ev-sum">${st.on ? nSummary(k) : '<span class="chip">Off</span>'}</div>
        <button class="btn btn-sm ${open ? 'btn-quiet' : ''}" onclick="nToggle('${k}')">${open ? 'Done' : 'Customize'}</button>
      </div>
      ${open ? nPanel(k) : ''}
    </div>`;
  }).join('');
  return card('Pushover', 'Notifications are sent through your Pushover account.', `<div class="grid2">
        ${fld('User key', 'u1a2b3c4d5e6f7g8h9', { hint: '' })}
        ${fld('App token', 'a1b2c3d4e5f6g7h8i9', { hint: '' })}
      </div>`, SAVE + pillBtn('Send test notification'))
    + card('Events', 'Turn each notification on or off and customise how it looks and sounds. Leave Title and Message empty to use the defaults shown in grey. Checkpoint and finish notifications attach a camera snapshot when a camera is assigned, otherwise the plate thumbnail.', `<div class="ev-list">${rows}</div>`, SAVE);
}

function tabIntegrations() {
  return card('MQTT broker', 'Used for MQTT smart plugs and for publishing printer state.', `<div class="grid2">
        ${fld('Broker host:port', '192.168.1.10:1883')}
        <div class="fld"><label>&nbsp;</label>${swrow('Use TLS', '', false)}</div>
        ${fld('Username', '', { ph: 'optional' })}
        ${fld('Password', '', { type: 'password', ph: 'optional' })}
      </div>
      ${swrow('Publish printer state', 'Publishes each printer\'s state to the topic below.', true)}
      <div class="grid2">${fld('Publish topic', 'printspy/printer/{id}/state', { wide: true })}</div>`, SAVE + pillBtn('Test connection') + pillBtn('Save and test'))
    + card('Dashboard status API', '', `<div class="fld"><label>API key</label>
        <div class="inline"><div class="inp"><input type="text" value="" placeholder="Leave blank to disable"></div>${pillBtn('Generate')}</div>
        <small>Used by external dashboards (e.g. Homepage) to read <code>GET /api/status</code>. Send it as the <code>X-Api-Key</code> header.</small></div>`, SAVE);
}

function tabAccount() {
  return card('Your account', '', `<div class="grid2">
        ${fld('Current password', '', { type: 'password' })}
        ${fld('New password', '', { type: 'password' })}
      </div>`, pillBtn('Change password', 'btn-primary'))
    + card('Users', '', `<div class="set-list">
        <div class="set-row"><div class="set-row-info"><span class="set-row-name">admin<span class="chip">you</span></span></div></div>
        <div class="set-row"><div class="set-row-info"><span class="set-row-name">guest</span></div><div class="set-row-actions"><button class="btn btn-sm btn-danger" onclick="confirmBtn(this)">Delete</button></div></div>
      </div>
      <div class="grid2" style="margin-top:1rem">${fld('Username', '')}${fld('Password', '', { type: 'password' })}</div>`, pillBtn('+ Add user', 'btn-primary'))
    + card('Backup', 'Export all printers, plugs, cameras and settings as YAML, or restore from a file.', '', pillBtn('Export YAML') + `<button class="btn btn-sm" onclick="importFlow()">Import YAML</button>`);
}

const SET_VIEWS = { printers: tabPrinters, devices: tabDevices, general: tabGeneral, notify: tabNotify, integrations: tabIntegrations, account: tabAccount };

function settingsHTML() {
  if (EDIT) return editHTML();
  const nav = SET_TABS.map(([k, l, n]) => `<button class="set-tab ${k === setTab ? 'on' : ''}" onclick="setTab='${k}';render()">${l}${n ? `<span class="cnt">${n}</span>` : ''}</button>`).join('');
  return `<div class="set-title"><h2><button class="crumb" onclick="go('a')">Dashboard</button> <span class="sep">&rsaquo;</span> Settings</h2></div>
    <div class="set-wrap"><nav class="set-nav">${nav}</nav><div class="set-body">${SET_VIEWS[setTab]()}</div></div>`;
}

/* ================= Edit / add pages (replace the little modals) ================= */
const PTYPES = { 'MK3S': 'prusalink', 'Core One': 'prusalink', 'MK4S': 'prusalink', 'Ender 3': 'octoprint', 'Mini+': 'prusalink', 'XL': 'prusalink', 'Voron 2.4': 'octoprint' };
const PLUGS = [
  { label: 'Printer', mode: 'http', ip: '192.168.1.60', topic: '', idx: '1', hide: false, printer: 'Core One' },
  { label: 'Lights', mode: 'http', ip: '192.168.1.60', topic: '', idx: '2', hide: false, printer: 'Core One' },
  { label: 'Printer', mode: 'mqtt', ip: '', topic: 'tasmota_mk4s', idx: '1', hide: false, printer: 'MK4S' },
  { label: 'Fan', mode: 'mqtt', ip: '', topic: 'tasmota_mk4s', idx: '2', hide: true, printer: 'MK4S' }
];
const CAMS = [
  { name: 'Core One cam', url: 'http://192.168.1.71', printer: 'Core One', res: '13', q: 12, hm: false, vf: false },
  { name: 'Ender cam', url: 'http://192.168.1.72', printer: '', res: '13', q: 12, hm: true, vf: false }
];
const TARGS = [{ label: 'mini', printer: 'Mini+' }];
const RES = [['10', 'VGA (640x480, fastest stream)'], ['11', 'SVGA (800x600)'], ['12', 'XGA (1024x768)'], ['13', 'HD (1280x720, default)'], ['14', 'SXGA (1280x1024)'], ['15', 'UXGA (1600x1200, slowest stream)']];
let EDIT = null; // { kind, idx, d: draft }

function openEdit(kind, idx, from) {
  let d;
  if (kind === 'printer') {
    const p = idx >= 0 ? DATA[idx] : null;
    d = p ? { name: p.name, model: p.model, hideModel: false, type: PTYPES[p.name] || 'prusalink', url: 'http://192.168.1.' + (40 + idx), user: 'maker', key: '••••••••••', poll: '10', idle: '', bed: '', ext: '' }
          : { name: '', model: '', hideModel: false, type: 'octoprint', url: '', user: 'maker', key: '', poll: '10', idle: '0', bed: '0', ext: '0' };
  } else if (kind === 'plug') d = idx >= 0 ? { ...PLUGS[idx] } : { label: '', mode: 'http', ip: '', topic: '', idx: '1', hide: false, printer: '' };
  else if (kind === 'camera') d = idx >= 0 ? { ...CAMS[idx] } : { name: '', url: '', printer: '', res: '13', q: 12, hm: false, vf: false };
  else d = idx >= 0 ? { ...TARGS[idx] } : { label: '', printer: 'Mini+' };
  EDIT = { kind, idx, d, from }; render(); window.scrollTo(0, 0);
}
function closeEdit() { const back = EDIT && EDIT.from === 'card'; EDIT = null; if (back) go('a'); else render(); }
function ed(k, v, redraw) { EDIT.d[k] = v; if (redraw) render(); }

const selectFld = (label, opts, cur, onchange, hint = '') => `<div class="fld"><label>${label}</label><div class="inp sel"><select onchange="${onchange}">${opts.map(([v, l]) => `<option value="${v}" ${String(v) === String(cur) ? 'selected' : ''}>${l}</option>`).join('')}</select></div>${hint ? `<small>${hint}</small>` : ''}</div>`;
const printerOpts = (none) => (none ? [['', 'Unassigned']] : []).concat(DATA.map(p => [p.name, p.name]));
const segFld = (label, opts, cur, fnName, hint = '') => `<div class="fld"><label>${label}</label><div class="seg-pills">${opts.map(([v, l]) => `<button class="webcam-mode-btn ${cur === v ? 'active' : ''}" onclick="${fnName}">${l}</button>`.replace('${v}', v)).join('')}</div>${hint ? `<small>${hint}</small>` : ''}</div>`;
function seg(label, opts, cur, key, hint) {
  return `<div class="fld"><label>${label}</label><div class="seg-pills">${opts.map(([v, l]) => `<button class="webcam-mode-btn ${cur === v ? 'active' : ''}" onclick="ed('${key}','${v}',true)">${l}</button>`).join('')}</div>${hint ? `<small>${hint}</small>` : ''}</div>`;
}
const dangerCard = (what) => `<div class="set-card danger"><div class="set-card-head"><h3>Delete ${what}</h3><p>This can't be undone.</p></div><div class="set-foot" style="border:0;margin:0;padding:0"><button class="btn btn-sm btn-danger" onclick="confirmBtn(this)">Delete ${what}</button></div></div>`;
const editBar = (extra = '') => `<div class="edit-bar"><button class="btn btn-sm btn-quiet" onclick="closeEdit()">Cancel</button>${extra}<button class="btn btn-sm btn-primary" onclick="closeEdit()">Save</button></div>`;

function editPrinter() {
  const d = EDIT.d, pl = d.type === 'prusalink';
  return card('Printer', '', `<div class="grid2">
        ${fld('Name', d.name, { ph: 'My Printer', oninput: "ed('name',this.value)" })}
        ${fld('Model', d.model, { ph: 'e.g. Core One, MK4S (optional)', oninput: "ed('model',this.value)" })}
      </div>
      ${swrow('Hide model on dashboard card', '', d.hideModel).replace("onclick=\"this.classList.toggle('on')\"", "onclick=\"this.classList.toggle('on');ed('hideModel',this.classList.contains('on'))\"")}`)
    + card('Connection', '', `${seg('Type', [['octoprint', 'OctoPrint'], ['prusalink', 'PrusaLink']], d.type, 'type', '')}
      <div class="grid2" style="margin-top:1rem">
        ${fld('URL', d.url, { ph: 'http://192.168.1.40', wide: true })}
        ${pl ? fld('Username', d.user, { ph: 'maker' }) : ''}
        ${fld(pl ? 'Password' : 'API Key', d.key, { ph: pl ? 'Your PrusaLink password' : 'Your OctoPrint API key', type: 'password' })}
        ${fld('Poll interval', d.poll, { unit: 'seconds' })}
      </div>
      <div class="test-row"><button class="btn btn-sm" onclick="testConn(this)">Test connection</button><span class="test-out"></span></div>`)
    + card('Power & safety', 'Only applies if a smart plug is assigned to this printer. Overrides the global settings when set.', `<div class="grid2">
        ${fld('Auto-off after idle', d.idle, { unit: 'minutes', ph: '0 = disabled' })}
        <span></span>
        ${fld('Thermal runaway: max bed temp', d.bed, { unit: '&deg;C', ph: '0 = disabled' })}
        ${fld('Thermal runaway: max extruder temp', d.ext, { unit: '&deg;C', ph: '0 = disabled' })}
      </div>`)
    + (EDIT.idx >= 0 ? dangerCard('printer') : '');
}
function testConn(btn) { const o = btn.parentElement.querySelector('.test-out'); o.className = 'test-out ok'; o.textContent = '✓ Connected: ' + (EDIT.d.model || 'printer') + ' (' + (EDIT.d.type === 'prusalink' ? 'PrusaLink' : 'OctoPrint') + ')'; }

function plugPreview(d) {
  const on = `<span class="plug on"><span class="knob"></span>${d.hide ? '' : (d.label || 'Printer')}</span>`;
  return `<div class="fld"><label>On the dashboard</label><div class="pvbox">${on}<small>${d.hide ? 'Label hidden: just the switch.' : 'Label shown next to the switch.'}</small></div></div>`;
}
function editPlug() {
  const d = EDIT.d, mq = d.mode === 'mqtt';
  return card('Smart plug (Tasmota)', '', `${seg('Connection', [['http', 'Direct HTTP'], ['mqtt', 'MQTT']], d.mode, 'mode')}
      <div class="grid2" style="margin-top:1rem">
        ${mq ? fld('MQTT topic', d.topic, { ph: 'tasmota_ABC123', hint: 'The device\'s Topic, as configured in its own Tasmota console (Configuration → MQTT).', wide: true })
             : fld('IP', d.ip, { ph: '192.168.1.60', wide: true })}
        ${fld('Relay index', d.idx, { hint: 'Which relay to control on multi-relay Tasmota devices. Leave as 1 for a normal single-outlet plug.' })}
        ${fld('Label', d.label, { ph: 'Printer', oninput: "ed('label',this.value)" })}
      </div>
      ${swrow('Hide label on dashboard power button', '', d.hide).replace("onclick=\"this.classList.toggle('on')\"", "onclick=\"ed('hide',!EDIT.d.hide,true)\"")}`)
    + card('Assignment', '', `<div class="grid2">${selectFld('Assigned printer', printerOpts(true), d.printer, "ed('printer',this.value)")}${plugPreview(d)}</div>`)
    + (EDIT.idx >= 0 ? dangerCard('plug') : '');
}

function editCamera() {
  const d = EDIT.d, existing = EDIT.idx >= 0;
  const tf = `${d.hm ? 'scaleX(-1) ' : ''}${d.vf ? 'scaleY(-1)' : ''}`;
  return card('Camera (printspy-cam)', '', `<div class="grid2">
        ${fld('URL', d.url, { ph: 'http://printspy-cam-abcdef.local', wide: true })}
        ${fld('Name', d.name, { ph: 'Printer' })}
        ${selectFld('Assigned printer', printerOpts(true), d.printer, "ed('printer',this.value)", 'Overrides that printer\'s own webcam, if any.')}
      </div>`)
    + (existing ? card('Camera settings', 'Applied live on the camera itself.', `<div class="cam-set">
        <div class="cam-form">
          <div class="inline" style="justify-content:flex-start"><a class="printer-link" href="#">Open camera web UI &#8599;</a></div>
          ${selectFld('Resolution', RES, d.res, "ed('res',this.value)")}
          <div class="fld"><label>JPEG quality <b id="qv">${d.q}</b> <small style="display:inline">(lower = better / slower)</small></label>
            <input type="range" class="range" min="4" max="40" step="1" value="${d.q}" oninput="document.getElementById('qv').textContent=this.value;EDIT.d.q=+this.value"></div>
          ${swrow('Mirror horizontally', '', d.hm).replace("onclick=\"this.classList.toggle('on')\"", "onclick=\"ed('hm',!EDIT.d.hm,true)\"")}
          ${swrow('Flip vertically', '', d.vf).replace("onclick=\"this.classList.toggle('on')\"", "onclick=\"ed('vf',!EDIT.d.vf,true)\"")}
        </div>
        <div class="cam-prev"><div class="ev-prev-label">Preview</div><div class="cam-img"><img src="plate-mk4s.png" style="transform:${tf || 'none'}" alt=""><span>Live from camera</span></div></div>
      </div>`) : '')
    + (existing ? dangerCard('camera') : '');
}

function editTarget() {
  const d = EDIT.d, lab = (d.label || '').toLowerCase().replace(/[^a-z0-9-]/g, '');
  const prusa = DATA.filter(p => (PTYPES[p.name] || 'prusalink') === 'prusalink').map(p => [p.name, p.name]);
  return card('Slicer target', '', `<div class="grid2">
        ${selectFld('Printer', prusa, d.printer, "ed('printer',this.value)", 'Files the slicer sends here relay to this printer automatically. "Upload" relays once it\'s online; "Upload and Print" also powers it on if it\'s off.')}
        ${fld('Label', d.label, { ph: 'Optional, e.g. coreone', oninput: "ed('label',this.value);document.getElementById('ingurl').textContent='https://printspy.example/ingest/'+(this.value.toLowerCase().replace(/[^a-z0-9-]/g,'')||'&lt;id&gt;')", hint: 'Lowercase letters, numbers and hyphens only. Becomes the ingest URL instead of the numeric id.' })}
      </div>
      <div class="fld" style="margin-top:1rem"><label>Host to enter in the slicer</label><div class="copybox"><code id="ingurl">https://printspy.example/ingest/${lab || '&lt;id&gt;'}</code><button class="btn btn-sm">Copy</button></div></div>`)
    + (EDIT.idx >= 0 ? dangerCard('slicer target') : '');
}

const EDIT_META = {
  printer: ['printers', 'Printers', () => EDIT.idx >= 0 ? EDIT.d.name : 'Add printer', editPrinter, 'printer', true],
  plug: ['devices', 'Plugs & cameras', () => EDIT.idx >= 0 ? EDIT.d.label + ' plug' : 'Add smart plug', editPlug, 'plug', false],
  camera: ['devices', 'Plugs & cameras', () => EDIT.idx >= 0 ? EDIT.d.name : 'Add camera', editCamera, 'camera', false],
  target: ['devices', 'Plugs & cameras', () => EDIT.idx >= 0 ? EDIT.d.label + ' slicer target' : 'Add slicer target', editTarget, 'slicer target', false]
};
function editHTML() {
  const [tab, tabLabel, title, body] = EDIT_META[EDIT.kind];
  const nav = SET_TABS.map(([k, l]) => `<button class="set-tab ${k === tab ? 'on' : ''}" onclick="EDIT=null;setTab='${k}';render()">${l}</button>`).join('');
  const crumbs = EDIT.from === 'card'
    ? `<button class="crumb" onclick="closeEdit()">Dashboard</button> &rsaquo; ${title()}`
    : `<button class="crumb" onclick="closeEdit()">Settings &rsaquo; ${tabLabel}</button> &rsaquo; ${title()}`;
  return `<div class="set-title"><h2>${crumbs}</h2></div>
    <div class="set-wrap"><nav class="set-nav">${nav}</nav><div class="set-body edit">${body()}${editBar(EDIT.kind === 'printer' ? '' : '')}</div></div>`;
}

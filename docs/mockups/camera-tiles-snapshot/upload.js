/* Upload dialog mockup. Same fields as today (printer, file, print immediately); nothing is really uploaded. */
let UP = null; // { printer, file:{name,size}|null, now, status:'idle'|'uploading'|'error', err }
const UP_SAMPLES = [
  { name: 'cable_clip_x12_0.4n_0.2mm_PLA_COREONE_58m.bgcode', size: 1843200 },
  { name: 'broken_upload_demo.bgcode', size: 52428800, fails: true }
];
let upSample = 0;

const prusalinkPrinters = () => DATA.filter(p => (typeof PTYPES !== 'undefined' ? PTYPES[p.name] : 'prusalink') !== 'octoprint');
const fmtSize = (b) => b >= 1048576 ? (b / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(b / 1024)) + ' KB';

function openUpload(printer) {
  const list = prusalinkPrinters();
  UP = { printer: list.find(p => p.name === printer) ? printer : (list[0] ? list[0].name : ''), file: null, now: false, status: 'idle', err: '' };
  drawUpload();
}
function closeUpload() { UP = null; const r = document.getElementById('up-root'); if (r) r.remove(); }

function upPick(f) {
  if (!f) return;
  if (!/\.(b?gcode)$/i.test(f.name)) { UP.file = null; UP.status = 'error'; UP.err = 'Only .gcode and .bgcode files can be uploaded.'; }
  else { UP.file = f; UP.status = 'idle'; UP.err = ''; }
  drawUpload();
}
function upSampleFile() { upPick(UP_SAMPLES[upSample++ % UP_SAMPLES.length]); }
function upSubmit() {
  if (!UP.file || !UP.printer) return;
  UP.status = 'uploading'; drawUpload();
  setTimeout(() => {
    if (!UP) return;
    if (UP.file.fails) { UP.status = 'error'; UP.err = 'Upload failed: printer rejected the file (storage full)'; drawUpload(); }
    else closeUpload(); // success closes the dialog, like today
  }, 1400);
}

function drawUpload() {
  let root = document.getElementById('up-root');
  if (!root) { root = document.createElement('div'); root.id = 'up-root'; document.body.appendChild(root); }
  const list = prusalinkPrinters();
  const groups = {};
  list.forEach(p => (groups[p.model] = groups[p.model] || []).push(p));
  const opts = list.length
    ? Object.keys(groups).sort().map(m => `<optgroup label="${m}">${groups[m].map(p => `<option ${p.name === UP.printer ? 'selected' : ''}>${p.name}</option>`).join('')}</optgroup>`).join('')
    : '<option value="">No PrusaLink printers configured</option>';
  const busy = UP.status === 'uploading';
  const file = UP.file;
  root.innerHTML = `<div class="mm-overlay" onclick="if(event.target===this)closeUpload()">
    <div class="mm" role="dialog" aria-label="Upload file">
      <div class="mm-head"><h2>Upload a file</h2><button class="mm-x" onclick="closeUpload()" aria-label="Close">&times;</button></div>

      <div class="fld"><label>Printer</label>
        <div class="inp sel"><select onchange="UP.printer=this.value" ${busy ? 'disabled' : ''}>${opts}</select></div>
        <small>Uploads go to PrusaLink printers.</small></div>

      <div class="fld"><label>File</label>
        ${file ? `<div class="up-file">
            <span class="up-ico">&#128196;</span>
            <span class="up-fname"><b title="${file.name}">${file.name}</b><small>${fmtSize(file.size)}</small></span>
            ${busy ? '' : `<button class="btn btn-sm" onclick="UP.file=null;UP.status='idle';UP.err='';drawUpload()">Remove</button>`}
          </div>`
        : `<div class="up-drop" id="up-drop" ondragover="event.preventDefault();this.classList.add('over')" ondragleave="this.classList.remove('over')" ondrop="event.preventDefault();this.classList.remove('over');upPick(event.dataTransfer.files[0])" onclick="document.getElementById('up-input').click()">
            <svg width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 16V4"/><polyline points="7 9 12 4 17 9"/><path d="M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3"/></svg>
            <b>Drop a file here</b>
            <span>or <u>browse</u> &middot; .gcode, .bgcode</span>
          </div>
          <input id="up-input" type="file" accept=".gcode,.bgcode" hidden onchange="upPick(this.files[0])">
          <small class="up-sample"><a href="#" onclick="event.preventDefault();upSampleFile()">Use a sample file</a> (mockup only; every second one fails)</small>`}
      </div>

      ${busy ? '<div class="up-progress"><i></i></div>' : ''}
      <div class="swrow" style="padding:.2rem 0;border:0"><span class="swtext"><b>Print immediately</b></span><span class="sw ${UP.now ? 'on' : ''}" onclick="UP.now=!UP.now;this.classList.toggle('on')"></span></div>

      ${busy ? '<div class="test-result up-status">Uploading&hellip;</div>' : UP.status === 'error' ? `<div class="test-result up-status err">${UP.err}</div>` : ''}

      <div class="mm-foot">
        <button class="btn btn-sm btn-quiet" onclick="closeUpload()" ${busy ? 'disabled' : ''}>Cancel</button>
        <button class="btn btn-sm btn-primary" onclick="upSubmit()" ${!file || busy || !UP.printer ? 'disabled' : ''}>${busy ? 'Uploading&hellip;' : 'Upload'}</button>
      </div>
    </div></div>`;
}
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && UP) closeUpload(); });

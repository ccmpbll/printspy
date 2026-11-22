/* Raw API debug window (PrusaLink printers only). Sample JSON, nothing is fetched. */
let RAW = null;
const RAW_SAMPLE = {
  version: { api: '2.0.0', server: '2.1.2', nozzle_diameter: 0.4, text: 'PrusaLink', hostname: 'coreone', capabilities: { 'upload-by-put': true } },
  info: { name: 'Core One', hostname: 'coreone', serial: 'XXXXXXXXXXXXXXX', nozzle_diameter: 0.4, mmu: false, min_extrusion_temp: 170 },
  status: { job: { id: 412, progress: 64, time_remaining: 1560, time_printing: 3960 }, printer: { state: 'PRINTING', temp_bed: 86, target_bed: 85, temp_nozzle: 250, target_nozzle: 250, axis_z: 31.4, flow: 100, speed: 100, fan_hotend: 5100, fan_print: 4800 } },
  job: { id: 412, state: 'PRINTING', progress: 64, time_remaining: 1560, time_printing: 3960, file: { name: 'JOINTS~1.BGC', display_name: 'allparts_m_joints-v1_0.4n_0.15mm_PETG_COREONE_1h14m.bgcode', path: '/usb', size: 3118340 } },
  storage: { storage_list: [{ path: '/usb', name: 'usb', type: 'USB', read_only: false, available: true }] },
  transfer: null,
  files_usb: { type: 'FOLDER', name: 'usb', children: ['…'] },
  cameras: { _status: 404, body: { message: 'Not Found' } }
};

function openRawApi(name) {
  RAW = { name };
  let root = document.getElementById('raw-root');
  if (!root) { root = document.createElement('div'); root.id = 'raw-root'; document.body.appendChild(root); }
  root.innerHTML = `<div class="mm-overlay" onclick="if(event.target===this)closeRawApi()">
    <div class="mm mm-wide" role="dialog" aria-label="Raw API">
      <div class="mm-head"><h2>Raw API <span class="mm-sub">${name}</span></h2><button class="mm-x" onclick="closeRawApi()" aria-label="Close">&times;</button></div>
      <p class="raw-note"><span class="live-dot"></span>Every PrusaLink endpoint, unfiltered. Refreshes every 5 seconds while this is open.</p>
      <div class="raw-body">${Object.keys(RAW_SAMPLE).map(k => `<div class="raw-sec"><h3>${k}</h3><pre>${JSON.stringify(RAW_SAMPLE[k], null, 2).replace(/</g, '&lt;')}</pre></div>`).join('')}</div>
      <div class="mm-foot"><button class="btn btn-sm" onclick="closeRawApi()">Close</button></div>
    </div></div>`;
}
function closeRawApi() { RAW = null; const r = document.getElementById('raw-root'); if (r) r.remove(); }
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && RAW) closeRawApi(); });

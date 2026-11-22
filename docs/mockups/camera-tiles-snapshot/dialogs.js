/* Small dialogs that are native confirm()/alert() popups today: Import YAML confirmation and result. Mockup only. */
function mmDialog(title, body, buttons) {
  let r = document.getElementById('dlg-root');
  if (!r) { r = document.createElement('div'); r.id = 'dlg-root'; document.body.appendChild(r); }
  r.innerHTML = `<div class="mm-overlay" onclick="if(event.target===this)mmClose()"><div class="mm" role="alertdialog"><div class="mm-head"><h2>${title}</h2><button class="mm-x" onclick="mmClose()" aria-label="Close">&times;</button></div>${body}<div class="mm-foot">${buttons}</div></div></div>`;
}
function mmClose() { const r = document.getElementById('dlg-root'); if (r) r.remove(); }
function importFlow() {
  mmDialog('Import settings', '<p class="mm-text">Import will add printers and overwrite settings from this file. Continue?</p>',
    `<button class="btn btn-sm btn-quiet" onclick="mmClose()">Cancel</button><button class="btn btn-sm btn-primary" onclick="importDone()">Import</button>`);
}
function importDone() {
  mmDialog('Import complete', `<p class="mm-text">Import complete: 2 printer(s), 1 smart plug(s), 1 camera(s), 1 ingest key(s) added.</p>
    <p class="mm-text">Skipped 1 item(s):</p><ul class="mm-list"><li>printer "Voron 2.4": already exists</li></ul>`,
    `<button class="btn btn-sm btn-primary" onclick="mmClose()">OK</button>`);
}
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') mmClose(); });

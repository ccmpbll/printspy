/* "States" tab: every card state the real app can show, plus the ingest banner's two variants. Mockup-only gallery. */
const MODE = {};
function setMode(id, m) { MODE[id] = m; render(); }

const STATE_DEMOS = [
  { id: 101, name: 'Ender 5', model: 'OctoPrint', state: 'attention', msg: 'Printer needs attention', hot: [24, 0], bed: [23, 0], watts: 5, plugs: [{ l: 'Printer', on: true }] },
  { id: 102, name: 'XL (backup)', model: 'Prusa XL 5T', state: 'disconnected', msg: '', plugs: [{ l: 'Printer', on: true }] },
  { id: 103, name: 'Mini (spare)', model: 'Prusa Mini+', state: 'busy', msg: 'Printer busy', hot: [24, 0], bed: [23, 0], watts: 6, plugs: [{ l: 'Printer', on: true }] },
  { id: 104, name: 'MK4 (garage)', model: 'Prusa MK4S', state: 'offline', msg: 'Unable to reach printer', plugs: [{ l: 'Printer', on: false }], ingest: { file: 'bracket_x4_0.4n_0.2mm_PETG_MK4S_1h12m.bgcode' } }
];
function statesHTML() {
  const note = '<div class="states-note">Every other state the app can show. These are extra printers just for this tab. The main fleet shows printing, paused, idle, error, maintenance and offline.</div>';
  return `<div class="set-title"><h2><button class="crumb" onclick="go('a')">Dashboard</button> <span class="sep">&rsaquo;</span> Card states</h2></div>${note}${STATE_DEMOS.map(cardA).join('')}`;
}

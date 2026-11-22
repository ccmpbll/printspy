/* Login / first-run setup pages: same fields and messages as today, restyled to match. */
function loginHTML() {
  const m = loginMode, setup = m === 'setup';
  const err = m === 'error' ? 'Incorrect username or password.' : m === 'ratelimit' ? 'Too many attempts. Try again later.' : '';
  const logo = document.querySelector('.header-logo').getAttribute('src');
  return `<div class="auth-page"><div class="auth-card">
      <img class="auth-logo" src="${logo}" alt="">
      <h1>PrintSpy</h1>
      ${err ? `<div class="auth-error">${err}</div>` : ''}
      ${setup ? '<p class="auth-sub">Create the first account to secure this PrintSpy instance.</p>' : ''}
      <div class="fld"><label>Username</label><div class="inp"><input type="text" ${m === 'login' ? 'autofocus' : ''}></div></div>
      <div class="fld" style="margin-top:1rem"><label>Password</label><div class="inp"><input type="password">${''}</div>${setup ? '<small>At least 8 characters.</small>' : ''}</div>
      <button class="btn btn-sm btn-primary auth-btn">${setup ? 'Create account' : 'Log in'}</button>
    </div>
    <div class="auth-mock">Mockup:
      ${[['login', 'Log in'], ['error', 'Wrong password'], ['ratelimit', 'Rate limited'], ['setup', 'First-run setup']].map(([k, l]) => `<button class="${loginMode === k ? 'on' : ''}" onclick="loginMode='${k}';render()">${l}</button>`).join('')}
    </div></div>`;
}

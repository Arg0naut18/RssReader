// ═══════════════════════════════════════════════════════════════
// Auth state
// ═══════════════════════════════════════════════════════════════
const auth = {
  get accessToken()  { return localStorage.getItem('access_token'); },
  get refreshToken() { return localStorage.getItem('refresh_token'); },
  save(tokens) {
    localStorage.setItem('access_token',  tokens.access_token);
    localStorage.setItem('refresh_token', tokens.refresh_token);
  },
  clear() {
    localStorage.removeItem('access_token');
    localStorage.removeItem('refresh_token');
  },
  get isLoggedIn() { return !!this.accessToken; },
};

// ═══════════════════════════════════════════════════════════════
// App state
// ═══════════════════════════════════════════════════════════════
const state = {
  feeds: [],
  items: [],
  activeSources: new Set(),
  searchQuery: '',
  loading: false,
};

const TAG_COLORS = ['#3b5bdb','#7950f2','#1098ad','#0ca678','#e67700','#c2255c','#5c7cfa','#20c997','#f59f00','#e64980'];
function tagColor(source) {
  let h = 0;
  for (let i = 0; i < source.length; i++) h = (h * 31 + source.charCodeAt(i)) >>> 0;
  return TAG_COLORS[h % TAG_COLORS.length];
}

// ═══════════════════════════════════════════════════════════════
// API helpers — automatically attach Bearer token, handle 401
// ═══════════════════════════════════════════════════════════════
async function api(method, path, body, { skipAuth = false } = {}) {
  const opts = { method, headers: { 'Content-Type': 'application/json' } };
  if (!skipAuth && auth.accessToken) {
    opts.headers['Authorization'] = `Bearer ${auth.accessToken}`;
  }
  if (body !== undefined) opts.body = JSON.stringify(body);

  let res = await fetch(path, opts);

  // Access token expired — try refreshing once.
  if (res.status === 401 && !skipAuth && auth.refreshToken) {
    const refreshed = await tryRefreshTokens();
    if (refreshed) {
      opts.headers['Authorization'] = `Bearer ${auth.accessToken}`;
      res = await fetch(path, opts);
    }
  }

  // Still 401 after refresh attempt → session dead, force sign-out.
  if (res.status === 401 && !skipAuth) {
    signOut();
    return null;
  }

  if (res.status === 204) return null;
  return res.json();
}

async function tryRefreshTokens() {
  try {
    const data = await api('POST', '/api/auth/refresh',
      { refresh_token: auth.refreshToken },
      { skipAuth: true }
    );
    if (data && data.access_token) {
      auth.save(data);
      return true;
    }
  } catch (_) { /* fall through */ }
  return false;
}

// ═══════════════════════════════════════════════════════════════
// Toast
// ═══════════════════════════════════════════════════════════════
let toastTimer;
function toast(msg, type = '') {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.className = 'show ' + type;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.className = '', 3000);
}

// ═══════════════════════════════════════════════════════════════
// AUTH SCREEN
// ═══════════════════════════════════════════════════════════════
let currentTab = 'login';

function switchTab(tab) {
  currentTab = tab;
  document.getElementById('tabLogin').classList.toggle('active', tab === 'login');
  document.getElementById('tabRegister').classList.toggle('active', tab === 'register');
  document.getElementById('confirmField').style.display = tab === 'register' ? '' : 'none';
  document.getElementById('authSubmitBtn').textContent = tab === 'login' ? 'Sign in' : 'Create account';
  document.getElementById('authPassword').autocomplete = tab === 'login' ? 'current-password' : 'new-password';
  clearAuthError();
}

function showAuthError(msg) {
  const el = document.getElementById('authError');
  el.textContent = msg;
  el.style.display = '';
}
function clearAuthError() {
  const el = document.getElementById('authError');
  el.style.display = 'none';
  el.textContent = '';
}

async function submitAuth() {
  clearAuthError();
  const email    = document.getElementById('authEmail').value.trim();
  const password = document.getElementById('authPassword').value;
  const confirm  = document.getElementById('authConfirm').value;
  const btn      = document.getElementById('authSubmitBtn');

  if (!email || !password) { showAuthError('Email and password are required.'); return; }
  if (currentTab === 'register' && password !== confirm) {
    showAuthError('Passwords do not match.'); return;
  }

  btn.disabled = true;
  btn.textContent = '…';

  const endpoint = currentTab === 'login' ? '/api/auth/login' : '/api/auth/register';
  const data = await api('POST', endpoint, { email, password }, { skipAuth: true });

  btn.disabled = false;
  btn.textContent = currentTab === 'login' ? 'Sign in' : 'Create account';

  if (!data) { showAuthError('Network error — please try again.'); return; }
  if (data.error) { showAuthError(data.error); return; }

  auth.save(data);
  showApp();
}

function signOut() {
  auth.clear();
  // Reset app state.
  state.feeds = [];
  state.items = [];
  state.activeSources.clear();
  document.getElementById('appScreen').style.display = 'none';
  document.getElementById('authScreen').style.display = '';
  switchTab('login');
  document.getElementById('authEmail').value    = '';
  document.getElementById('authPassword').value = '';
}

function showApp() {
  document.getElementById('authScreen').style.display = 'none';
  document.getElementById('appScreen').style.display = '';
  loadFeeds().then(() => {
    if (state.feeds.length > 0) loadItems();
  });
}

document.getElementById('signOutBtn').addEventListener('click', signOut);

// ═══════════════════════════════════════════════════════════════
// ISLAND 1 — Feed Manager
// ═══════════════════════════════════════════════════════════════
async function loadFeeds() {
  const data = await api('GET', '/api/feeds');
  if (!data || data.error || !Array.isArray(data)) {
    if (data && data.error) toast(data.error, 'error');
    state.feeds = [];
  } else {
    state.feeds = data;
  }
  state.feeds.forEach(f => state.activeSources.add(f.title));
  renderFeedList();
  renderConfigList();
  document.getElementById('feedCount').textContent = state.feeds.length + ' feed' + (state.feeds.length !== 1 ? 's' : '');
}

function renderFeedList() {
  const list = document.getElementById('feedList');
  if (state.feeds.length === 0) {
    list.innerHTML = '<div style="padding:20px 16px;color:var(--muted);font-size:13px;text-align:center">No feeds yet — use ⚙ to add</div>';
    return;
  }
  list.innerHTML = state.feeds.map(f => {
    const color  = tagColor(f.title);
    const active = state.activeSources.has(f.title);
    return `<div class="feed-item ${active ? 'active' : ''}" data-title="${esc(f.title)}">
      <div class="feed-dot" style="background:${color}"></div>
      <div class="feed-info">
        <div class="feed-title">${esc(f.title)}</div>
      </div>
    </div>`;
  }).join('');
}

document.getElementById('feedList').addEventListener('click', e => {
  const item = e.target.closest('.feed-item');
  if (!item) return;
  const title = item.dataset.title;
  if (state.activeSources.has(title)) {
    state.activeSources.delete(title);
  } else {
    state.activeSources.add(title);
  }
  renderFeedList();
  renderItems();
  // auto-close sidebar on mobile after selecting a feed
  if (window.innerWidth <= 768) closeMobileSidebar();
});

document.getElementById('selectAllBtn').addEventListener('click', () => {
  state.feeds.forEach(f => state.activeSources.add(f.title));
  renderFeedList(); renderItems();
});
document.getElementById('selectNoneBtn').addEventListener('click', () => {
  state.activeSources.clear();
  renderFeedList(); renderItems();
});

// ── Config drawer ──────────────────────────────────────────────
function openConfig() {
  document.getElementById('configDrawer').classList.add('open');
  document.getElementById('configBackdrop').classList.add('open');
  document.getElementById('feedUrlInput').focus();
}
function closeConfig() {
  document.getElementById('configDrawer').classList.remove('open');
  document.getElementById('configBackdrop').classList.remove('open');
}

document.getElementById('openConfigBtn').addEventListener('click', openConfig);
document.getElementById('closeConfigBtn').addEventListener('click', closeConfig);
document.getElementById('configBackdrop').addEventListener('click', closeConfig);

// ── Mobile sidebar toggle ───────────────────────────────────────
function openMobileSidebar() {
  document.getElementById('sidebar').classList.add('open');
  document.getElementById('sidebarBackdrop').classList.add('open');
}
function closeMobileSidebar() {
  document.getElementById('sidebar').classList.remove('open');
  document.getElementById('sidebarBackdrop').classList.remove('open');
}

document.getElementById('mobileMenuBtn').addEventListener('click', () => {
  const sidebar = document.getElementById('sidebar');
  if (sidebar.classList.contains('open')) {
    closeMobileSidebar();
  } else {
    openMobileSidebar();
  }
});
document.getElementById('sidebarBackdrop').addEventListener('click', closeMobileSidebar);

// ── Desktop sidebar collapse/expand ────────────────────────────
document.getElementById('collapseSidebarBtn').addEventListener('click', () => {
  document.getElementById('appScreen').querySelector('.main').classList.add('sidebar-collapsed');
});
document.getElementById('expandSidebarBtn').addEventListener('click', () => {
  document.getElementById('appScreen').querySelector('.main').classList.remove('sidebar-collapsed');
});

function renderConfigList() {
  const list = document.getElementById('configList');
  if (state.feeds.length === 0) {
    list.innerHTML = '<div style="padding:20px 16px;color:var(--muted);font-size:13px;text-align:center">No feeds added yet</div>';
    return;
  }
  list.innerHTML = state.feeds.map(f => {
    const color = tagColor(f.title);
    return `<div class="config-item">
      <div class="feed-dot" style="background:${color};flex-shrink:0"></div>
      <div class="config-item-info">
        <div class="config-item-title">${esc(f.title)}</div>
        <div class="config-item-url">${esc(f.url)}</div>
      </div>
      <button class="icon-btn" data-del="${esc(f.url)}" title="Remove feed">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
      </button>
    </div>`;
  }).join('');
}

document.getElementById('configList').addEventListener('click', async e => {
  const btn = e.target.closest('[data-del]');
  if (!btn) return;
  const url = btn.dataset.del;
  const res = await api('DELETE', '/api/feeds', { url });
  if (res && res.error) { toast(res.error, 'error'); return; }
  toast('Feed removed', 'success');
  const feed = state.feeds.find(f => f.url === url);
  if (feed) state.activeSources.delete(feed.title);
  await loadFeeds();
  await loadItems();
});

document.getElementById('addFeedBtn').addEventListener('click', addFeed);
document.getElementById('feedUrlInput').addEventListener('keydown', e => { if (e.key === 'Enter') addFeed(); });

async function addFeed() {
  const input = document.getElementById('feedUrlInput');
  const url   = input.value.trim();
  if (!url) return;
  const btn = document.getElementById('addFeedBtn');
  btn.disabled = true; btn.textContent = '…';
  const res = await api('POST', '/api/feeds', { url });
  btn.disabled = false; btn.textContent = 'Add';
  if (res && res.error) { toast(res.error, 'error'); return; }
  input.value = '';
  toast('Feed added!', 'success');
  await loadFeeds();
  await loadItems();
}

// ═══════════════════════════════════════════════════════════════
// ISLAND 2 — Filter Bar
// ═══════════════════════════════════════════════════════════════
document.getElementById('searchInput').addEventListener('input', e => {
  state.searchQuery = e.target.value.toLowerCase();
  renderItems();
});

// ═══════════════════════════════════════════════════════════════
// ISLAND 3 — Items Grid
// ═══════════════════════════════════════════════════════════════
async function loadItems() {
  state.loading = true;
  renderLoadingGrid();
  const data = await api('GET', '/api/items');
  state.loading = false;
  state.items = (data && data.items) ? data.items : [];
  if (data && data.errors && data.errors.length) {
    toast('Some feeds failed to load', 'error');
  }
  renderItems();
}

function filteredItems() {
  return state.items.filter(item => {
    if (!state.activeSources.has(item.source)) return false;
    if (state.searchQuery) {
      const hay = (item.title + ' ' + item.source + ' ' + stripHtml(item.description)).toLowerCase();
      if (!hay.includes(state.searchQuery)) return false;
    }
    return true;
  });
}

function renderLoadingGrid() {
  document.getElementById('itemsGrid').innerHTML = '<div class="spinner"></div>';
}

function renderItems() {
  const grid  = document.getElementById('itemsGrid');
  const items = filteredItems();
  document.getElementById('itemCount').textContent = items.length + ' article' + (items.length !== 1 ? 's' : '');

  if (items.length === 0) {
    grid.innerHTML = state.feeds.length === 0
      ? `<div class="empty-state">
          <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4 11a9 9 0 0 1 9 9"/><path d="M4 4a16 16 0 0 1 16 16"/><circle cx="5" cy="19" r="1"/></svg>
          <p>No feeds yet</p><small>Add an RSS or Atom feed URL in the sidebar.</small></div>`
      : `<div class="empty-state">
          <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/></svg>
          <p>No articles match</p><small>Try adjusting your search or tag filters.</small></div>`;
    return;
  }

  grid.innerHTML = items.map((item, idx) => buildCard(item, idx)).join('');

  // Delegate card navigation — open the article URL when clicking the card
  // but NOT when the click target is an inner link (embedded URL in description).
  grid.querySelectorAll('.item-card[data-href]').forEach(card => {
    card.addEventListener('click', e => {
      if (e.target.closest('a, button')) return;
      window.open(card.dataset.href, '_blank', 'noopener');
    });
    card.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        window.open(card.dataset.href, '_blank', 'noopener');
      }
    });
  });
}

function buildCard(item, idx) {
  const color  = tagColor(item.source);
  const imgSrc = item.imageUrl ? `/api/proxy?url=${encodeURIComponent(item.imageUrl)}` : '';
  const imgHtml = imgSrc
    ? `<img class="item-img" src="${esc(imgSrc)}" alt="" loading="lazy" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'">`
      + `<div class="item-img-placeholder" style="display:none">📰</div>`
    : `<div class="item-img-placeholder">📰</div>`;

  const desc = stripHtml(item.description || '');
  const descHtml = desc
    ? `<div class="item-desc" id="desc-${idx}">${linkify(desc)}</div>
       <button class="item-desc-toggle" onclick="event.preventDefault();toggleDesc(${idx})">Show more</button>`
    : '';

  const pubStr = item.published && !item.published.startsWith('0001') ? formatDate(item.published) : '';

  const cardTag   = item.link
    ? `<div class="item-card" role="link" tabindex="0" data-href="${esc(item.link)}">`
    : `<div class="item-card">`;
  const cardClose = `</div>`;

  return `${cardTag}
    ${imgHtml}
    <div class="item-body">
      <div class="item-meta">
        <span class="item-tag" style="background:${color}22;color:${color}">${esc(item.source)}</span>
        ${pubStr ? `<span class="item-date">${pubStr}</span>` : ''}
      </div>
      <div class="item-title">${esc(item.title)}</div>
      ${descHtml}
    </div>
  ${cardClose}`;
}

function toggleDesc(idx) {
  const el  = document.getElementById('desc-' + idx);
  const btn = el.nextElementSibling;
  if (el.classList.contains('expanded')) {
    el.classList.remove('expanded');
    btn.textContent = 'Show more';
  } else {
    el.classList.add('expanded');
    btn.textContent = 'Show less';
  }
}

// ═══════════════════════════════════════════════════════════════
// Refresh button
// ═══════════════════════════════════════════════════════════════
document.getElementById('refreshAllBtn').addEventListener('click', async () => {
  const btn = document.getElementById('refreshAllBtn');
  btn.disabled = true;
  await api('POST', '/api/refresh');
  await loadItems();
  btn.disabled = false;
  toast('Feeds refreshed', 'success');
});

// ═══════════════════════════════════════════════════════════════
// Utilities
// ═══════════════════════════════════════════════════════════════
function esc(s) {
  return String(s ?? '')
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

function linkify(text) {
  // Split on URLs found in the raw text, escape each non-URL segment,
  // and wrap each URL in a safe <a> tag.
  const urlRegex = /(https?:\/\/[^\s<>"']+)/g;
  const parts = [];
  let last = 0;
  let match;
  while ((match = urlRegex.exec(text)) !== null) {
    if (match.index > last) parts.push(esc(text.slice(last, match.index)));
    const url = match[1];
    parts.push(`<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(url)}</a>`);
    last = match.index + url.length;
  }
  if (last < text.length) parts.push(esc(text.slice(last)));
  return parts.join('');
}

function stripHtml(html) {
  const tmp = document.createElement('div');
  tmp.innerHTML = html;
  return tmp.textContent || tmp.innerText || '';
}

function formatDate(iso) {
  try {
    const d = new Date(iso);
    if (isNaN(d)) return '';
    const now  = new Date();
    const diff = now - d;
    const mins  = Math.floor(diff / 60000);
    const hours = Math.floor(diff / 3600000);
    const days  = Math.floor(diff / 86400000);
    if (mins < 1)  return 'just now';
    if (mins < 60) return `${mins}m ago`;
    if (hours < 24) return `${hours}h ago`;
    if (days < 7)   return `${days}d ago`;
    return d.toLocaleDateString(undefined, { month:'short', day:'numeric', year: d.getFullYear() !== now.getFullYear() ? 'numeric' : undefined });
  } catch { return ''; }
}

// ═══════════════════════════════════════════════════════════════
// Boot — show auth screen or app depending on stored token
// ═══════════════════════════════════════════════════════════════
(async () => {
  if (!auth.isLoggedIn) return; // auth screen is shown by default

  // We have a token — try to use it; if it's expired, tryRefreshTokens is
  // called automatically by api() on the first 401.
  showApp();
})();

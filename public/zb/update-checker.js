// 版本显示 + 服务端版本变化提示。
// OpenList 侧没有 version.json 指纹机制，改为读取 /zb/api/version（conf.Version + WebVersion）。
const FIRST_CHECK_DELAY = 3000;
const POLL_INTERVAL = 30 * 1000;

const BASE = String(window.__ZB_BASE__ || '').replace(/\/+$/, '');
const boot = { version: String(window.__APP_VERSION__ || 'unknown') };
const $ = (id) => document.getElementById(id);
const badge = $('appVersion');
const toast = $('updateToast');

let checking = false;
let latest = null;
let badgeTimer = null;

function renderBadge(info) {
  if (!badge) return;
  badge.textContent = `v${boot.version}`;
  badge.title = [
    `OpenList 版本 v${boot.version}`,
    info && info.webVersion ? `前端版本 ${info.webVersion}` : '',
    '点击可立即检查',
  ].filter(Boolean).join('\n');
}

function flashBadge(text) {
  if (!badge) return;
  clearTimeout(badgeTimer);
  badge.textContent = text;
  badge.classList.add('checked');
  badgeTimer = setTimeout(() => {
    badge.textContent = `v${boot.version}`;
    badge.classList.remove('checked');
  }, 1600);
}

function showToast(next) {
  if (!toast) return;
  $('updateTitle').textContent = `服务端已更新到 v${next.version}`;
  $('updateSub').textContent = '刷新页面以加载新版本';
  toast.classList.remove('hidden');
}

async function check({ force = false, manual = false } = {}) {
  if (checking || (!force && document.hidden)) return;
  checking = true;
  try {
    const response = await fetch(`${BASE}/api/version?t=${Date.now()}`, { cache: 'no-store' });
    if (!response.ok) return;
    const info = await response.json();
    if (!info) return;
    latest = info;
    renderBadge(info);
    if (info.version && info.version !== boot.version) showToast(info);
    else if (manual) flashBadge('✓ 已是最新');
  } catch {
    // 服务重启或网络抖动时静默，下一轮继续检测
  } finally {
    checking = false;
  }
}

renderBadge(null);

badge?.addEventListener('click', () => check({ force: true, manual: true }));
$('updateNow')?.addEventListener('click', () => location.reload());
$('updateLater')?.addEventListener('click', () => $('updateToast')?.classList.add('hidden'));

window.addEventListener('focus', () => check());
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) check();
});

setTimeout(() => check(), FIRST_CHECK_DELAY);
setInterval(() => check(), POLL_INTERVAL);

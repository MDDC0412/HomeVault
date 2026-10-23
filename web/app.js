/* ===========================================================================
   homevault 用户端入口（/app.js）
   ---------------------------------------------------------------------------
   1. 启动与鉴权（/api/auth/me、登录页、强制改凭据页）
   2. 外壳与路由（侧栏导航 / 顶栏 / 移动端底栏 / 悬浮上传）
   3. 我的文件（复用 ui.js 里的 FileBrowser）
   4. 搜索（关键词 + 类型/大小/时间过滤 + 聚合面板）
   5. 回收站（恢复 / 永久删除 / 清空）
   6. 设置（凭据、配额、关于）
   7. 操作日志（仅在拥有 logs_view 权限时可见）
   约定：所有服务端返回的字符串一律经 textContent 输出，绝不拼接进 innerHTML。
   =========================================================================== */

import {
  api, apiGet, authEvents, el, clearNode, theme, onNarrowChange, session,
  toastInfo, toastSuccess, toastError, toastApiError,
  fmtBytes, fmtQuota, fmtDate, fmtDateTime, fmtRelative, fmtUptime, nextDay,
  KIND_LABELS, KIND_ICONS, kindLabel, ACTION_LABELS, actionLabel, statusLabel, statusBadgeClass,
  permissionLabel, parentPath, parseSizeInput,
  UploadManager, FileBrowser, createUploadPanel, mountShell, buildAccountBlock, themeToggleButton,
  showLoginView, showCredentialGate, credentialPanel, confirmDialog, showMenu,
  fileUrl, withQuery, viewHead, cardSection, cardScrollSection, noteRow, sortRows, enableLocalSort,
  donutChart, barChart, lineChart,
  append,
} from '/ui.js';

/* ===== 1. 启动与鉴权 ====================================================== */

const app = {
  me: null,
  user: null,
  route: '',
  shell: null,
  uploads: null,
  uploadPanel: null,
  browser: null,
  pendingPath: null,
  pendingPreview: null,
  pendingEdit: false,
};

const ROUTES = ['overview', 'files', 'search', 'trash', 'settings', 'logs'];

const ROUTE_TITLES = {
  overview: '概览',
  files: '我的文件',
  search: '搜索',
  trash: '回收站',
  settings: '设置',
  logs: '操作日志',
};

const ROUTE_SUBS = {
  overview: '容量、文件构成与最近活动',
  files: '拖拽文件到页面即可上传，支持分片续传',
  search: '按名称、类型、大小与时间查找文件',
  trash: '删除的文件会先进入回收站，可随时恢复',
  settings: '账号凭据、存储配额与运行信息',
  logs: '你在这个服务上的操作记录',
};

function removeBootSplash() {
  const boot = document.getElementById('ld-boot');
  if (boot) boot.remove();
}

/** 显示登录页（未登录、会话过期、账号被禁用都会走到这里）。 */
function requireLogin(notice) {
  removeBootSplash();
  if (app.route === 'login') return;
  app.route = 'login';
  showLoginView({
    title: 'HomeVault',
    subtitle: '自托管文件托管服务',
    notice,
    onSuccess: (result) => startApp(result),
  });
}

/** 首次登录必须改凭据：全屏页面，不可关闭。 */
function requireCredentialChange(user) {
  removeBootSplash();
  if (app.route === 'gate') return;
  app.route = 'gate';
  showCredentialGate({
    user,
    onDone: () => window.location.reload(),
    onLogout: () => logout(),
  });
}

async function logout() {
  try {
    await api('/api/auth/logout', { method: 'POST', body: {} });
  } catch {
    /* 退出接口失败也要回到登录页 */
  }
  window.location.href = '/';
}

async function boot() {
  theme.init();
  authEvents.onMustChange = () => requireCredentialChange(app.user);
  authEvents.onDisabled = () => requireLogin('账号已被禁用，请联系管理员。');

  let me;
  try {
    me = await apiGet('/api/auth/me');
  } catch (err) {
    // 首次访问本来就没有会话，不该弹"已过期"这种提示。
    if (err && err.status === 401) {
      requireLogin();
      return;
    }
    requireLogin((err && err.message) || '无法读取会话，请重新登录。');
    return;
  }
  // 会话探测完成后再挂 401 处理，避免首屏误报"登录状态已过期"。
  authEvents.onUnauthorized = () => requireLogin('登录状态已过期，请重新登录。');
  // 管理员没有自己的存储空间：给一个明确的说明页 + 去管理端的入口，
  // 不要静默跳转（否则点"用户端"会像什么都没发生）。
  if (me.user && me.user.isAdmin) {
    renderAdminNotice(me.user);
    return;
  }
  if (me.user && me.user.mustChangeCredentials) {
    requireCredentialChange(me.user);
    return;
  }
  startApp(me);
}

/** 管理员访问用户端时的说明页。 */
function renderAdminNotice(user) {
  removeBootSplash();
  app.route = 'admin-notice';
  const card = el('div', { class: 'ld-auth__card' },
    el('div', { class: 'ld-auth__brand' },
      el('span', { class: 'ld-auth__logo' }, el('ld-icon', { name: 'shield', size: 'xl' })),
      el('div', { class: 'ld-auth__title', text: '本账号是管理员，只可使用管理端' }),
      el('div', { class: 'ld-auth__sub', text: `${user.username} · 管理员` })),
    el('div', { class: 'ld-note ld-note--info' },
      el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: 'info' })),
      el('div', { class: 'ld-note__body', text: '管理与用户端是分离的：管理员负责账号与别人的文件，不分配个人存储空间，因此没有客户端界面。需要存取文件请用普通用户账号在客户端登录。' })),
    el('div', { class: 'ld-view__actions' },
      el('a', { class: 'ld-btn ld-btn--primary ld-btn--block', href: '/admin/', text: '前往管理端' }),
      el('ld-button', { label: '退出登录', icon: 'logout', variant: 'ghost', block: true, onClick: () => logout() })));
  const host = el('div', { class: 'ld-auth' }, el('div', { class: 'ld-auth__inner' }, card));
  document.body.appendChild(host);
}

async function startApp(result) {
  // 登录接口只返回 user/home，这里补齐版本、空间目录等运行信息
  let me = result;
  if (!me || !me.version) {
    try {
      me = await apiGet('/api/auth/me');
    } catch {
      me = result;
    }
  }
  // 管理员没有自己的存储空间：登录后直接送去管理端，不要渲染空的用户界面
  if (me.user && me.user.isAdmin) {
    window.location.replace('/admin/');
    return;
  }
  app.me = me;
  app.user = me.user;
  session.set({
    ready: true,
    user: me.user,
    home: me.home || '/',
    version: me.version || '',
    spaceDir: me.spaceDir || '',
    sessionTTL: me.sessionTTL || '',
    officeAvailable: !!me.officeAvailable,
    permissionLabels: me.permissionLabels || [],
  });
  removeBootSplash();
  if (!app.shell) buildShell();
  // 登录页阶段 route 是 'login'，renderRoute() 会因此直接返回，这里必须先复位
  app.route = '';
  if (!window.location.hash) window.location.hash = '#/files';
  else renderRoute();
}

/* ===== 2. 外壳与路由 ===================================================== */

/** 是否拥有某个权限（管理员视为拥有全部）。 */
function can(permission) {
  if (!app.user) return false;
  if (app.user.isAdmin) return true;
  return (app.user.permissions || []).includes(permission);
}

function navItems() {
  const items = [
    { id: 'overview', label: '概览', icon: 'dashboard', hint: '容量、文件构成与最近活动' },
    { id: 'files', label: '我的文件', icon: 'folder', hint: '浏览、上传与管理文件' },
    { id: 'search', label: '搜索', icon: 'search', hint: '按条件查找文件' },
  ];
  if (can('trash_view')) items.push({ id: 'trash', label: '回收站', icon: 'trash', hint: '恢复或彻底删除文件' });
  if (can('logs_view')) items.push({ id: 'logs', label: '操作日志', icon: 'logs', hint: '查看自己的操作记录' });
  items.push({ id: 'settings', label: '设置', icon: 'settings', hint: '账号与配额' });
  return items;
}

function accountBlock() {
  return buildAccountBlock(app.user, {
    onSettings: () => go('settings'),
    onSelect: (id) => {
      if (id === 'logout') logout();
      else if (id === 'settings') go('settings');
    },
  });
}

function buildShell() {
  app.uploads = new UploadManager({ user: null, requirePassword: false });
  app.shell = mountShell({ brandName: 'HomeVault', brandSub: '我的文件' });

  const searchInput = el('input', {
    class: 'ld-input ld-input--search',
    type: 'search',
    placeholder: '搜索文件…',
    'aria-label': '全局搜索',
  });
  const searchForm = el('form', {
    class: 'ld-top__search',
    role: 'search',
    onsubmit: (event) => {
      event.preventDefault();
      const q = searchInput.value.trim();
      if (!q) {
        toastInfo('请输入要搜索的关键词');
        return;
      }
      go('search', `q=${encodeURIComponent(q)}`);
    },
  }, searchInput);

  const accountTop = accountBlock();
  const narrowQuery = window.matchMedia('(max-width: 859px)');
  accountTop.hidden = !narrowQuery.matches;
  onNarrowChange((narrow) => {
    accountTop.hidden = !narrow;
  });

  app.shell.topRight.append(searchForm, themeToggleButton(), accountTop);
  app.shell.setAccount(accountBlock());
  app.uploadPanel = createUploadPanel(app.uploads);

  window.addEventListener('hashchange', () => renderRoute());
  window.addEventListener('beforeunload', (event) => {
    if (app.uploads && app.uploads.hasWork()) {
      event.preventDefault();
      event.returnValue = '';
    }
  });

  app.shell.fab.addEventListener('click', () => {
    if (app.browser) app.browser.pickFiles();
  });
}

function go(route, query) {
  window.location.hash = `#/${route}${query ? `?${query}` : ''}`;
}

function renderRoute() {
  if (!app.shell || app.route === 'login' || app.route === 'gate') return;
  const raw = window.location.hash.replace(/^#\/?/, '');
  const [name, queryString] = raw.split('?');
  const route = ROUTES.includes(name) ? name : 'files';
  if (route === 'logs' && !can('logs_view')) {
    toastError('当前账号没有查看日志的权限');
    go('files');
    return;
  }
  if (route === 'trash' && !can('trash_view')) {
    toastError('当前账号没有查看回收站的权限');
    go('files');
    return;
  }
  app.route = route;
  app.shell.fab.hidden = true;

  app.shell.setNav(navItems(), route, (id) => go(id));
  app.shell.setBrandSub(ROUTE_TITLES[route]);
  app.shell.setTitle(`${ROUTE_TITLES[route]} · HomeVault`);

  const left = app.shell.topLeft;
  clearNode(left);
  left.append(
    el('div', { class: 'ld-top__title', text: ROUTE_TITLES[route] }),
    el('span', { class: 'ld-view__sub ld-ellipsis', text: ROUTE_SUBS[route] }),
  );

  const main = app.shell.main;
  clearNode(main);
  main.scrollTop = 0;
  // 旧的 FileBrowser 挂过 window 级拖拽监听，必须显式销毁
  if (app.browser) {
    app.browser.destroy();
    app.browser = null;
  }

  if (route === 'overview') renderOverview();
  else if (route === 'files') renderFiles();
  else if (route === 'search') renderSearch(queryString);
  else if (route === 'trash') renderTrash();
  else if (route === 'settings') renderSettings();
  else if (route === 'logs') renderLogs(queryString);
}

/* ===== 3. 概览 =========================================================== */

const overviewState = {
  nodes: null,
  token: 0,
};

function renderOverview() {
  const view = el('section', { class: 'ld-view', 'aria-label': '概览' });
  const kpiBox = el('div', { class: 'ld-kpi' });
  const usageBox = el('div', { class: 'ld-view__grid' });
  const chartsBox = el('div', { class: 'ld-view__grid' });
  const recentBox = el('div', { class: 'ld-view__grid' });
  // 没有日志权限时压根不会出现「最近操作」卡片，副标题就别承诺它了。
  const sub = can('logs_view')
    ? '容量、文件构成与最近活动一眼看清'
    : '容量、文件构成与收拾进度一眼看清';
  const viewNodes = [viewHead('我的概览', sub, [
    el('ld-button', { label: '刷新', icon: 'refresh', variant: 'ghost', onClick: () => loadOverview() }),
  ]), kpiBox, usageBox, chartsBox, recentBox];
  append(view, ...viewNodes);
  app.shell.main.appendChild(view);
  overviewState.nodes = { kpiBox, usageBox, chartsBox, recentBox };
  loadOverview();
}

function kpiCard(label, value, hint) {
  return el('div', { class: 'ld-kpi__card' },
    el('div', { class: 'ld-kpi__label', text: label }),
    el('div', { class: 'ld-kpi__value', text: String(value) }),
    hint ? el('div', { class: 'ld-kpi__hint', text: hint }) : null);
}

function chartCard(title, sub, body, legend) {
  return el('div', { class: 'ld-card' },
    el('div', { class: 'ld-card__body' },
      el('div', { class: 'ld-chart-card__head' },
        el('div', { class: 'ld-chart-card__title', text: title }),
        sub ? el('div', { class: 'ld-chart-card__sub', text: sub }) : null),
      body,
      legend || null));
}

function legendRow(items) {
  return el('div', { class: 'ld-legend' },
    items.map((it) => el('span', { class: 'ld-legend__item' },
      el('span', { class: 'ld-legend__dot', style: { background: it.color } }),
      el('span', { text: it.label }))));
}

/** 概览数据：/api/search 一次请求就带回全部聚合（total / byKind / bySize / byDay）。 */
async function loadOverview() {
  const nodes = overviewState.nodes;
  if (!nodes) return;
  // 每次加载带一个序号：切换路由后旧请求的结果直接丢弃，也不会卡住新的加载。
  const token = ++overviewState.token;
  const stale = () => token !== overviewState.token || overviewState.nodes !== nodes;
  clearNode(nodes.kpiBox);
  nodes.kpiBox.appendChild(el('div', { class: 'ld-kpi__card' }, el('ld-spinner')));
  try {
    const me = await apiGet('/api/auth/me');
    if (stale()) return;
    const user = (me && me.user) || app.user || {};
    const quota = Number(user.quota) || 0;
    const used = Number(user.used) || 0;
    const trashUsed = Number(user.trashUsed) || 0;
    const total = used + trashUsed;
    const pct = quota > 0 ? Math.min(100, Math.round((total / quota) * 100)) : 0;

    // limit=1：只要聚合，不要命中列表，避免把所有文件名都拉回来。
    const agg = await apiGet('/api/search?limit=1&includeDirs=0');
    if (stale()) return;
    const fileCount = Number(agg.total) || 0;
    const kinds = agg.byKind || [];
    const bySize = agg.bySize || [];
    const byDay = agg.byDay || [];

    // ---- KPI
    clearNode(nodes.kpiBox);
    append(nodes.kpiBox,
      kpiCard('已用空间', fmtBytes(total), quota > 0 ? `上限 ${fmtBytes(quota)}（${pct}%）` : '未设置上限'),
      kpiCard('文件', String(fileCount), `共 ${fmtBytes(used)}`),
      kpiCard('回收站', fmtBytes(trashUsed), trashUsed > 0 ? '可随时恢复' : '是空的'),
      kpiCard('剩余空间', quota > 0 ? fmtBytes(Math.max(0, quota - total)) : '不限',
        quota > 0 ? `已用 ${pct}%` : '未设置上限'));

    // ---- 配额使用
    clearNode(nodes.usageBox);
    const bar = document.createElement('ld-quota-bar');
    bar.used = used;
    bar.trashUsed = trashUsed;
    bar.quota = quota;
    nodes.usageBox.appendChild(cardSection('空间使用',
      bar,
      quota > 0
        ? noteRow('info', `你的配额是 ${fmtBytes(quota)}，已用 ${fmtBytes(total)}，还剩 ${fmtBytes(Math.max(0, quota - total))}。`)
        : noteRow('info', '当前账号未设置配额上限，可使用的空间取决于服务器磁盘容量。')));

    // ---- 容量环形 + 文件类型分布
    clearNode(nodes.chartsBox);
    const donut = donutChart({
      segments: [
        { value: used, color: 'var(--ld-accent)' },
        { value: trashUsed, color: 'var(--ld-warn)' },
        { value: quota > 0 ? Math.max(0, quota - total) : 0, color: 'var(--ld-surface-3)' },
      ],
      centerLabel: quota > 0 ? `${pct}%` : fmtBytes(total),
      centerSub: quota > 0 ? '已用 / 上限' : '已用',
    });
    const kindPalette = ['var(--ld-accent)', 'var(--ld-ok)', 'var(--ld-warn)', 'var(--ld-danger)', 'var(--ld-info)', 'var(--ld-muted)'];
    const kindItems = kinds.slice(0, 6).map((k, i) => ({
      label: kindLabel(k.key), value: Number(k.bytes) || 0, color: kindPalette[i % kindPalette.length],
    })).filter((k) => k.value > 0);
    append(nodes.chartsBox,
      chartCard('容量占用', quota > 0 ? `上限 ${fmtBytes(quota)}` : '未设置上限', donut,
        legendRow([
          { label: `文件 ${fmtBytes(used)}`, color: 'var(--ld-accent)' },
          { label: `回收站 ${fmtBytes(trashUsed)}`, color: 'var(--ld-warn)' },
          { label: `剩余 ${quota > 0 ? fmtBytes(Math.max(0, quota - total)) : '不限'}`, color: 'var(--ld-surface-3)' },
        ])),
      chartCard('文件类型', kinds.length ? `按占用体积（前 6 类，共 ${kinds.length} 类）` : '按占用体积',
        kindItems.length
          ? barChart({ items: kindItems, height: 170 })
          : el('div', { class: 'ld-view__sub', text: '还没有文件，去「我的文件」上传吧。' }),
        kindItems.length
          ? legendRow(kindItems.map((k) => ({ label: `${k.label} ${fmtBytes(k.value)}`, color: k.color })))
          : null));

    // ---- 体积分布 + 入库趋势 + 最近操作
    clearNode(nodes.recentBox);
    const sizeItems = bySize
      .filter((b) => Number(b.count) > 0)
      .map((b) => ({ label: b.key, value: Number(b.count) || 0 }));
    // 只取最近 7 天，按日期升序，缺的日期补 0，折线才不会断
    const dayMap = new Map(byDay.map((b) => [b.key, Number(b.count) || 0]));
    const points = [];
    const today = new Date();
    for (let i = 6; i >= 0; i -= 1) {
      const d = new Date(today.getFullYear(), today.getMonth(), today.getDate() - i);
      const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
      points.push({ label: key.slice(5), value: dayMap.get(key) || 0 });
    }
    append(nodes.recentBox,
      chartCard('文件体积分布', sizeItems.length ? '按文件个数统计' : '',
        sizeItems.length
          ? barChart({ items: sizeItems, height: 160 })
          : el('div', { class: 'ld-view__sub', text: '还没有文件' })),
      chartCard('近 7 天新增文件', '按修改时间统计', lineChart({ points, height: 160 })));

    // 最近操作需要日志权限，没有权限就不请求，也不要空卡片
    if (can('logs_view')) {
      const logs = await apiGet('/api/logs?limit=8');
      if (stale()) return;
      const recentList = el('div', { class: 'ld-list' });
      for (const entry of (logs.entries || [])) {
        recentList.appendChild(el('div', { class: 'ld-list__item' },
          el('div', { class: 'ld-view__row' },
            el('div', { class: 'ld-view__row-main' },
              // 搜索这类操作 target 可能是空的，拼出「搜索 · —」没意义
              el('div', {
                text: entry.target
                  ? `${actionLabel(entry.action)} · ${entry.target}`
                  : actionLabel(entry.action),
              }),
              el('div', { class: 'ld-view__sub', text: `${fmtDateTime(entry.ts)}${entry.status !== 'ok' ? ' · ' + statusLabel(entry.status) : ''}` })),
            el('div', { class: 'ld-view__row-side' },
              el('span', { class: `ld-badge ${statusBadgeClass(entry.status)}`, text: statusLabel(entry.status) })))));
      }
      if (!(logs.entries || []).length) {
        recentList.appendChild(el('div', { class: 'ld-view__sub', text: '还没有操作记录' }));
      }
      nodes.recentBox.appendChild(cardScrollSection('最近操作', recentList, {
        bodyClass: 'ld-card__body--scroll ld-card__body--flush',
        action: el('ld-button', { label: '查看全部', icon: 'logs', variant: 'ghost', onClick: () => go('logs') }),
      }));
    }
  } catch (err) {
    if (stale()) return;
    clearNode(nodes.kpiBox);
    clearNode(nodes.usageBox);
    clearNode(nodes.chartsBox);
    clearNode(nodes.recentBox);
    nodes.recentBox.appendChild(cardSection('读取失败', noteRow('danger', (err && err.message) || '无法读取概览数据')));
    toastApiError(err, '读取概览失败');
  }
}

/* ===== 3. 我的文件 ======================================================= */

function renderFiles() {
  const view = el('section', { class: 'ld-view', 'aria-label': '我的文件' });
  app.shell.main.appendChild(view);

  const browser = new FileBrowser(view, {
    user: null,
    isAdmin: false,
    permissions: app.user.permissions || [],
    requirePassword: false,
    uploads: app.uploads,
    showQuota: true,
    rootLabel: '我的空间',
  });
  browser.addEventListener('ld-upload-open', () => {
    if (app.uploadPanel) app.uploadPanel.panel.collapsed = false;
  });
  browser.mount();
  app.browser = browser;

  app.shell.fab.hidden = !browser.can('upload');
  const target = app.pendingPath;
  const pendingPreview = app.pendingPreview;
  const pendingEdit = !!app.pendingEdit;
  app.pendingPath = null;
  app.pendingPreview = null;
  app.pendingEdit = false;
  browser.load(target || '')
    .catch(() => {})
    .then(() => {
      if (pendingPreview) browser.preview(pendingPreview, { edit: pendingEdit });
    });
  // 列出服务端尚未完成的分片会话：刷新后可重新选择同一文件续传
  app.uploads.refreshSessions();
}

/** 从搜索结果跳到文件视图并定位到某个目录。 */
function openInFiles(dir) {
  app.pendingPath = dir || '';
  go('files');
}

/* ===== 4. 搜索 =========================================================== */

const searchState = {
  q: '',
  kinds: new Set(),
  minSize: '',
  maxSize: '',
  from: '',
  to: '',
  dir: '',
  includeDirs: false,
  limit: 200,
};

function renderSearch(queryString) {
  const params = new URLSearchParams(queryString || '');
  searchState.q = params.get('q') || '';
  searchState.kinds = new Set(String(params.get('kind') || '').split(',').filter(Boolean));
  searchState.minSize = params.get('minSize') || '';
  searchState.maxSize = params.get('maxSize') || '';
  searchState.from = params.get('from') || '';
  searchState.to = params.get('to') || '';
  searchState.dir = params.get('path') || '';
  searchState.includeDirs = params.get('includeDirs') === '1';

  const view = el('section', { class: 'ld-view', 'aria-label': '搜索' });
  // 结果摘要：搜索后显示“找到 N 个匹配项”，紧贴结果表上方。
  const summary = el('div', { class: 'ld-search__summary ld-hidden' });
  const aggs = el('div', { class: 'ld-aggs' });
  // 聚合分布默认折叠：结果优先，需要时再展开看分布。
  const aggsFold = el('details', { class: 'ld-aggs-fold' },
    el('summary', { class: 'ld-aggs-fold__summary' },
      el('ld-icon', { name: 'filter', size: 'sm' }),
      el('span', { text: '按类型 / 大小 / 日期分布' })),
    aggs);

  const keyword = el('input', {
    class: 'ld-input',
    type: 'search',
    value: searchState.q,
    placeholder: '文件名关键词，例如 报告',
    'aria-label': '文件名关键词',
  });
  const minSize = el('input', {
    class: 'ld-input',
    type: 'text',
    value: searchState.minSize,
    placeholder: '如 10MB',
    'aria-label': '最小大小',
  });
  const maxSize = el('input', {
    class: 'ld-input',
    type: 'text',
    value: searchState.maxSize,
    placeholder: '如 2GB',
    'aria-label': '最大大小',
  });
  const fromInput = el('input', { class: 'ld-input', type: 'date', value: searchState.from, 'aria-label': '起始日期' });
  const toInput = el('input', { class: 'ld-input', type: 'date', value: searchState.to, 'aria-label': '结束日期' });
  const dirInput = el('input', {
    class: 'ld-input',
    type: 'text',
    value: searchState.dir,
    placeholder: '留空 = 全部目录',
    'aria-label': '限定目录',
  });
  const includeDirs = el('input', { type: 'checkbox', checked: searchState.includeDirs, 'aria-label': '包含文件夹' });

  const kindChips = el('div', { class: 'ld-chips' });
  for (const [key, label] of Object.entries(KIND_LABELS)) {
    if (key === 'folder') continue;
    const on = searchState.kinds.has(key);
    const chip = el('button', {
      class: `ld-chip ld-chip--kind${on ? ' is-on' : ''}`,
      type: 'button',
      dataset: { kind: key },
      'aria-pressed': on ? 'true' : 'false',
      onclick: () => {
        if (searchState.kinds.has(key)) searchState.kinds.delete(key);
        else searchState.kinds.add(key);
        const active = searchState.kinds.has(key);
        chip.classList.toggle('is-on', active);
        chip.setAttribute('aria-pressed', active ? 'true' : 'false');
      },
    }, el('ld-icon', { name: KIND_ICONS[key] || 'file', size: 'sm' }), el('span', { text: label }));
    kindChips.appendChild(chip);
  }

  const pickDirButton = el('ld-button', {
    label: '选择目录',
    icon: 'folder',
    variant: 'soft',
    size: 'sm',
    onClick: () => pickSearchDir(dirInput),
  });

  const runButton = el('ld-button', {
    label: '开始搜索',
    icon: 'search',
    variant: 'primary',
    onClick: () => runSearch(),
  });
  const resetButton = el('ld-button', {
    label: '重置条件',
    icon: 'refresh',
    variant: 'ghost',
    onClick: () => {
      keyword.value = '';
      minSize.value = '';
      maxSize.value = '';
      fromInput.value = '';
      toInput.value = '';
      dirInput.value = '';
      includeDirs.checked = false;
      searchState.kinds.clear();
      for (const chip of kindChips.querySelectorAll('.ld-chip')) {
        chip.classList.remove('is-on');
        chip.setAttribute('aria-pressed', 'false');
      }
      // 条件清空后旧结果就失效了，一并收起，避免“条件空着却还摆着一堆结果”。
      searchState.hits = [];
      if (searchState.table) searchState.table.rows = [];
      clearNode(searchState.aggsNode);
      searchState.summaryNode.textContent = '';
      searchState.summaryNode.classList.add('ld-hidden');
      if (aggsFold) aggsFold.open = false;
      toastInfo('搜索条件已重置');
    },
  });

  // 主搜索行：关键词 + 开始搜索，最常用的操作压到一行里。
  const quickRow = el('div', { class: 'ld-search__quick' },
    el('div', { class: 'ld-search__quick-input' }, keyword),
    resetButton,
    runButton);

  // 高级条件折叠：大小 / 日期 / 目录 / 包含文件夹，默认收起。
  const advanced = el('details', { class: 'ld-search__more' },
    el('summary', { class: 'ld-search__more-summary' },
      el('ld-icon', { name: 'settings', size: 'sm' }),
      el('span', { text: '更多条件：大小 / 日期 / 目录' })),
    el('div', { class: 'ld-search__more-body' },
      el('div', { class: 'ld-search' },
        el('ld-field', { label: '最小大小' }, minSize),
        el('ld-field', { label: '最大大小' }, maxSize),
        el('ld-field', { label: '起始日期' }, fromInput),
        el('ld-field', { label: '结束日期' }, toInput),
        el('ld-field', { class: 'ld-search__wide', label: '限定目录' },
          el('div', { class: 'ld-search__dir' }, dirInput, pickDirButton)),
        el('ld-field', { class: 'ld-search__wide', label: '包含文件夹' },
          el('label', { class: 'ld-check' }, includeDirs, el('span', { text: '把目录也作为结果' }))))));

  const filterCard = el('section', { class: 'ld-card ld-search-card' },
    el('div', { class: 'ld-card__body' },
      quickRow,
      el('div', { class: 'ld-search__kinds' },
        el('div', { class: 'ld-field__label', text: '文件类型（可多选）' }),
        kindChips),
      advanced));

  const table = el('ld-data-table', {
    selectable: false,
    rowMenu: true,
    // 双击结果行即可打开（文件夹跳转 / 文件预览）
    clickable: true,
    // 手机上表格会横向溢出，窄屏自动改用卡片
    autoCards: true,
    ariaLabel: '搜索结果',
    emptyIcon: 'search',
    emptyTitle: '还没有搜索',
    emptyDesc: '填写关键词或选择类型后点击“开始搜索”。',
  });
  table.columns = [
    {
      key: 'name',
      title: '名称',
      sortable: true,
      render: (row) => el('div', { class: 'ld-cell-name' },
        el('ld-file-icon', { kind: row.isDir ? 'folder' : row.kind, size: 'md' }),
        el('div', {},
          el('div', { class: 'ld-cell-name__text ld-ellipsis', text: row.name }),
          el('div', { class: 'ld-cell-name__sub ld-ellipsis', text: `/${row.path}` }))),
    },
    { key: 'size', title: '大小', sortable: true, width: '110px', class: 'ld-col-size', render: (row) => (row.isDir ? '—' : fmtBytes(row.size)) },
    { key: 'kind', title: '类型', sortable: true, width: '110px', class: 'ld-col-kind', render: (row) => kindLabel(row.isDir ? 'folder' : row.kind) },
    { key: 'modTime', title: '修改时间', sortable: true, width: '170px', class: 'ld-col-time', render: (row) => fmtDate(row.modTime) },
    { key: 'parent', title: '所在目录', width: '200px', render: (row) => (row.parent ? `/${row.parent}` : '根目录') },
  ];
  table.addEventListener('ld-row-open', (event) => {
    const row = event.detail.row;
    if (row.isDir) openInFiles(row.path);
    else previewHit(row);
  });
  table.addEventListener('ld-row-menu', (event) => hitMenu(event.detail));

  view.append(
    viewHead('搜索', '按名称、类型、大小与时间过滤，并查看聚合结果'),
    filterCard,
    summary,
    table,
    aggsFold,
  );
  app.shell.main.appendChild(view);

  searchState.summaryNode = summary;
  searchState.aggsNode = aggs;
  searchState.table = table;
  searchState.keyword = keyword;
  searchState.minInput = minSize;
  searchState.maxInput = maxSize;
  searchState.fromInput = fromInput;
  searchState.toInput = toInput;
  searchState.dirInput = dirInput;
  searchState.includeDirsInput = includeDirs;
  searchState.hits = [];
  searchState.sort = { key: 'name', dir: 'asc' };
  enableLocalSort(table, searchState, () => searchState.hits);

  if (searchState.q || searchState.kinds.size || searchState.dir) runSearch();
  else keyword.focus();
}

async function pickSearchDir(dirInput) {
  const picker = el('ld-dir-picker', { rootLabel: '我的空间' });
  picker.loader = (path) => api(withQuery('/api/fs/list', { path }));
  const dialog = document.createElement('ld-modal');
  dialog.title = '选择搜索范围';
  dialog.size = 'lg';
  dialog.append(picker);
  document.body.appendChild(dialog);
  dialog.setActions([
    { label: '取消', value: 'cancel' },
    { label: '使用这个目录', value: 'ok', variant: 'primary' },
  ]);
  dialog.open();
  picker.open(dirInput.value);
  const ok = await new Promise((resolve) => {
    dialog.addEventListener('ld-action', (event) => resolve(event.detail.value === 'ok'));
    dialog.addEventListener('ld-close', () => resolve(false));
  });
  const chosen = picker.current;
  dialog.remove();
  if (ok) dirInput.value = chosen || '';
}

function currentSearchParams() {
  const to = searchState.toInput.value;
  return {
    q: searchState.keyword ? searchState.keyword.value.trim() : searchState.q,
    kind: Array.from(searchState.kinds).join(','),
    minSize: parseSizeInput(searchState.minInput.value) ?? '',
    maxSize: parseSizeInput(searchState.maxInput.value) ?? '',
    from: searchState.fromInput.value,
    // 结束日期按“包含当天”处理：服务端只接受一个时间点，这里补到次日 00:00
    to: to ? nextDay(to) : '',
    path: searchState.dirInput.value.trim(),
    includeDirs: searchState.includeDirsInput.checked ? 1 : '',
    limit: searchState.limit,
  };
}

async function runSearch() {
  const table = searchState.table;
  if (!table) return;
  const minRaw = searchState.minInput.value.trim();
  const maxRaw = searchState.maxInput.value.trim();
  if ((minRaw && parseSizeInput(minRaw) === null) || (maxRaw && parseSizeInput(maxRaw) === null)) {
    toastError('大小格式不正确，可写 10MB、1GiB 这类写法');
    return;
  }
  const params = currentSearchParams();
  if (!params.q && !params.kind && !params.path) {
    toastInfo('请至少填写关键词、选择类型或限定目录');
    return;
  }
  table.loading = true;
  try {
    const data = await apiGet(withQuery('/api/search', params));
    searchState.hits = (data.hits || []).map((hit) => ({ ...hit, parent: parentPath(hit.path) }));
    table.rows = sortRows(searchState.hits, searchState.sort.key, searchState.sort.dir);
    table.loading = false;
    const total = data.total || 0;
    const scanned = data.scanned || 0;
    paintSearchSummary(total, scanned, data.truncated);
    renderAggs(data);
    if (total > 0) {
      toastSuccess(`找到 ${total} 个匹配项`);
    } else {
      toastInfo('没有找到匹配项，试试放宽筛选条件');
    }
  } catch (err) {
    table.loading = false;
    searchState.hits = [];
    table.rows = [];
    clearNode(searchState.aggsNode);
    searchState.summaryNode.textContent = '';
    searchState.summaryNode.classList.add('ld-hidden');
    toastApiError(err, '搜索失败');
  }
}

/** 结果摘要条：把“多少个结果”放在结果表正上方，而不是塞在角落小字里。 */
function paintSearchSummary(total, scanned, truncated) {
  const node = searchState.summaryNode;
  if (!node) return;
  clearNode(node);
  node.classList.remove('ld-hidden');
  node.dataset.state = total > 0 ? 'ok' : 'empty';
  node.append(
    el('span', { class: 'ld-search__summary-icon' },
      el('ld-icon', { name: total > 0 ? 'check' : 'search', size: 'sm' })),
    el('span', { class: 'ld-search__summary-text' },
      total > 0 ? `找到 ${total} 个匹配项` : '没有找到匹配项'),
    el('span', { class: 'ld-search__summary-meta',
      text: `扫描 ${scanned} 项${truncated ? '，结果已截断' : ''}` }),
  );
}

/** 服务端固定 5 个大小桶（顺序固定），点击后按序号换算成 min/max 条件。 */
const SIZE_BUCKETS = [
  { min: '', max: '1MiB' },
  { min: '1MiB', max: '10MiB' },
  { min: '10MiB', max: '100MiB' },
  { min: '100MiB', max: '1GiB' },
  { min: '1GiB', max: '' },
];

function renderAggs(data) {
  const box = searchState.aggsNode;
  clearNode(box);
  searchState.bySize = data.bySize || [];
  const groups = [
    { title: '按类型', buckets: data.byKind || [], field: 'kind' },
    { title: '按大小', buckets: searchState.bySize, field: 'size' },
    { title: '按日期', buckets: data.byDay || [], field: 'day' },
  ];
  for (const group of groups) {
    if (!group.buckets.length) continue;
    const chips = el('div', { class: 'ld-chips' });
    group.buckets.forEach((bucket, index) => {
      chips.appendChild(el('button', {
        class: 'ld-chip',
        type: 'button',
        title: `${bucket.count} 项，共 ${fmtBytes(bucket.bytes)}`,
        onclick: () => applyAggFilter(group.field, bucket.key, index),
      },
        el('span', { text: aggLabel(group.field, bucket.key) }),
        el('span', { class: 'ld-badge ld-badge--accent', text: String(bucket.count) })));
    });
    box.appendChild(el('div', { class: 'ld-agg__row' },
      el('div', { class: 'ld-agg__title', text: group.title }),
      chips));
  }
}

function aggLabel(field, key) {
  if (field === 'kind') return kindLabel(key);
  return key || '未知';
}

/** 点聚合桶 → 把条件写回表单，再让用户点“开始搜索”生效。 */
function applyAggFilter(field, key, index) {
  const form = searchState;
  if (field === 'kind') {
    searchState.kinds = new Set([key]);
    for (const chip of form.aggsNode.querySelectorAll('.ld-chip')) chip.classList.remove('is-on');
  } else if (field === 'size') {
    const bucket = SIZE_BUCKETS[index] || { min: '', max: '' };
    form.minInput.value = bucket.min;
    form.maxInput.value = bucket.max;
  } else if (field === 'day') {
    form.fromInput.value = key;
    form.toInput.value = key;
  }
  toastInfo(`已把「${aggLabel(field, key)}」写进筛选条件，点击“开始搜索”生效`);
}

async function previewHit(row) {
  // 搜索结果页没有文件管理器实例，回到文件视图并定位到所在目录后自动预览
  app.pendingPath = parentPath(row.path);
  app.pendingPreview = row;
  go('files');
}

async function hitMenu({ row, target, x, y }) {
  const canEditText = !row.isDir && row.kind === 'text' && can('modify');
  const choice = await showMenu({
    items: [
      { id: 'open', label: row.isDir ? '打开所在文件夹' : '下载', icon: row.isDir ? 'folder' : 'download' },
      canEditText ? { id: 'edit', label: '编辑文本', icon: 'edit' } : null,
      { id: 'locate', label: '在文件视图中定位', icon: 'eye' },
      { separator: true },
      { id: 'copy', label: '复制路径', icon: 'copy' },
    ].filter(Boolean),
    target,
    x,
    y,
    header: row.name,
  });
  if (!choice) return;
  if (choice === 'edit') {
    app.pendingPath = parentPath(row.path);
    app.pendingPreview = row;
    app.pendingEdit = true;
    go('files');
    return;
  }
  if (choice === 'open') {
    if (row.isDir) openInFiles(row.path);
    else {
      const link = el('a', { href: fileUrl(row.path, { download: 1 }), download: row.name, class: 'ld-sr' });
      document.body.appendChild(link);
      link.click();
      link.remove();
    }
  } else if (choice === 'locate') {
    openInFiles(parentPath(row.path));
  } else if (choice === 'copy') {
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(row.path);
      else throw new Error('clipboard unavailable');
      toastSuccess('路径已复制');
    } catch {
      toastInfo(`路径：${row.path}`);
    }
  }
}

/* ===== 5. 回收站 ========================================================= */

const trashState = { items: [], selected: new Set() };

function renderTrash() {
  const view = el('section', { class: 'ld-view', 'aria-label': '回收站' });
  const summary = el('div', { class: 'ld-view__sub', text: '正在读取…' });

  const table = el('ld-data-table', {
    selectable: true,
    rowMenu: true,
    rowKey: 'id',
    // 手机上表格会横向溢出，窄屏自动改用卡片
    autoCards: true,
    ariaLabel: '回收站列表',
    emptyIcon: 'trash',
    emptyTitle: '回收站是空的',
    emptyDesc: '在文件视图里删除的内容会先出现在这里，可以恢复或彻底删除。',
  });
  table.columns = [
    {
      key: 'name',
      title: '名称',
      sortable: true,
      render: (row) => el('div', { class: 'ld-cell-name' },
        el('ld-file-icon', { kind: row.isDir ? 'folder' : 'file', size: 'md' }),
        el('span', { class: 'ld-cell-name__text ld-ellipsis', title: row.name, text: row.name })),
    },
    { key: 'originalPath', title: '原位置', render: (row) => `/${row.originalPath || ''}` },
    { key: 'size', title: '大小', sortable: true, width: '110px', class: 'ld-col-size', render: (row) => (row.isDir ? '—' : fmtBytes(row.size)) },
    { key: 'deletedAt', title: '删除时间', sortable: true, width: '170px', class: 'ld-col-time', render: (row) => fmtDate(row.deletedAt) },
    { key: 'expiresAt', title: '保留期限', width: '160px', class: 'ld-col-time', render: (row) => (row.expiresAt ? `${fmtRelative(row.expiresAt)}后清理` : '永不自动清理') },
    { key: 'deletedBy', title: '删除者', width: '110px', render: (row) => row.deletedBy || '—' },
  ];

  const manageable = can('trash_manage');
  trashState.sort = { key: 'deletedAt', dir: 'desc' };
  enableLocalSort(table, trashState, () => trashState.items);
  const restoreButton = el('ld-button', { label: '恢复选中', icon: 'restore', variant: 'soft', disabled: true, onClick: () => restoreIds(Array.from(trashState.selected)) });
  const purgeButton = el('ld-button', { label: '彻底删除', icon: 'delete', variant: 'ghost-danger', disabled: true, onClick: () => purgeIds(Array.from(trashState.selected)) });
  const emptyButton = el('ld-button', { label: '清空回收站', icon: 'trash', variant: 'ghost-danger', onClick: () => emptyTrash() });

  function syncBar() {
    const count = trashState.selected.size;
    restoreButton.toggleAttribute('disabled', !manageable || count === 0);
    purgeButton.toggleAttribute('disabled', !manageable || count === 0);
    emptyButton.toggleAttribute('disabled', !manageable || trashState.items.length === 0);
  }

  table.addEventListener('ld-select', (event) => {
    const row = event.detail.row;
    if (event.detail.checked) trashState.selected.add(row.id);
    else trashState.selected.delete(row.id);
    // 把选中集合回灌给表格：卡片/行才会上高亮，重建时复选框也不会被重置
    table.setSelected(trashState.selected);
    syncBar();
  });
  table.addEventListener('ld-select-all', (event) => {
    trashState.selected = event.detail.checked ? new Set(trashState.items.map((item) => item.id)) : new Set();
    table.setSelected(trashState.selected);
    syncBar();
  });
  table.addEventListener('ld-row-menu', (event) => trashMenu(event.detail));

  // 注意：element.append() 收到 null 会渲染成字符串 "null"，必须先过滤掉。
  const viewNodes = [
    viewHead('回收站', '删除的文件会先进入回收站，到期后自动清理', [
      el('ld-button', { label: '刷新', icon: 'refresh', variant: 'ghost', onClick: () => loadTrash() }),
      restoreButton,
      purgeButton,
      emptyButton,
    ]),
  ];
  if (!manageable) {
    viewNodes.push(noteRow('info', '你只有查看回收站的权限，恢复与删除需要管理员为你开启「管理回收站」。'));
  }
  viewNodes.push(summary, table);
  append(view, ...viewNodes);
  app.shell.main.appendChild(view);

  trashState.table = table;
  trashState.summaryNode = summary;
  trashState.syncBar = syncBar;
  trashState.selected = new Set();
  loadTrash();
}

async function loadTrash() {
  const table = trashState.table;
  if (!table) return;
  table.loading = true;
  try {
    const data = await apiGet(withQuery('/api/trash', { limit: 200 }));
    trashState.items = data.items || [];
    trashState.selected = new Set();
    table.rows = sortRows(trashState.items, trashState.sort.key, trashState.sort.dir);
    table.setSelected(new Set());
    table.loading = false;
    trashState.summaryNode.textContent = trashState.items.length
      ? `${trashState.items.length} 项，共 ${fmtBytes(data.totalBytes || 0)}`
      : '回收站里没有内容';
    if (trashState.syncBar) trashState.syncBar();
  } catch (err) {
    table.loading = false;
    table.rows = [];
    toastApiError(err, '读取回收站失败');
    trashState.summaryNode.textContent = '';
  }
}

async function trashMenu({ row, target, x, y }) {
  const choice = await showMenu({
    items: [
      { id: 'restore', label: '恢复', icon: 'restore', disabled: !can('trash_manage') },
      { separator: true },
      { id: 'purge', label: '彻底删除', icon: 'delete', danger: true, disabled: !can('trash_manage') },
    ],
    target,
    x,
    y,
    header: row.name,
  });
  if (choice === 'restore') restoreIds([row.id]);
  else if (choice === 'purge') purgeIds([row.id]);
}

async function restoreIds(ids) {
  if (!ids.length) return;
  try {
    const data = await api('/api/trash/restore', { method: 'POST', body: { ids } });
    const failed = data.failed || [];
    if (failed.length) {
      toastError(`已恢复 ${data.restored || 0} 项，${failed.length} 项失败：${failed.map((item) => `${item.id}（${item.error}）`).join('；')}`);
    } else {
      toastSuccess(`已恢复 ${data.restored || ids.length} 项`);
    }
  } catch (err) {
    toastApiError(err, '恢复失败');
  }
  await loadTrash();
}

async function purgeIds(ids) {
  if (!ids.length) return;
  const ok = await confirmDialog({
    title: '彻底删除',
    message: `将永久删除选中的 ${ids.length} 项，无法恢复。确定继续吗？`,
    danger: true,
    confirmText: '永久删除',
  });
  if (!ok) return;
  try {
    const data = await api('/api/trash/delete', { method: 'POST', body: { ids } });
    toastSuccess(`已永久删除 ${data.deleted || ids.length} 项`);
  } catch (err) {
    toastApiError(err, '删除失败');
  }
  await loadTrash();
}

async function emptyTrash() {
  const ok = await confirmDialog({
    title: '清空回收站',
    message: '回收站里的所有内容都会被永久删除，无法恢复。确定继续吗？',
    danger: true,
    confirmText: '清空回收站',
  });
  if (!ok) return;
  try {
    const data = await api('/api/trash/empty', { method: 'POST', body: {} });
    toastSuccess(`已清空 ${data.count || 0} 项，释放 ${fmtBytes(data.bytes || 0)}`);
  } catch (err) {
    toastApiError(err, '清空回收站失败');
  }
  await loadTrash();
}

/* ===== 6. 设置 =========================================================== */

function renderSettings() {
  const user = app.user;
  const view = el('section', { class: 'ld-view', 'aria-label': '设置' });
  const grid = el('div', { class: 'ld-view__grid' });

  const panel = credentialPanel({
    user,
    onDone: (result) => {
      app.user = result.user;
      app.me = { ...app.me, user: result.user, home: result.home };
      if (app.shell) app.shell.setAccount(accountBlock());
      toastInfo(`当前账号：${result.user.username}`);
    },
  });
  grid.appendChild(cardSection('账号凭据',
    noteRow('info', '修改账号或密码需要输入当前密码；密码至少 8 个字符，改完请用新账号重新登录。'),
    panel.node));

  const usage = user.used || 0;
  const trashUsed = user.trashUsed || 0;
  const quotaBar = document.createElement('ld-quota-bar');
  quotaBar.used = usage;
  quotaBar.trashUsed = trashUsed;
  quotaBar.quota = user.quota || 0;
  grid.appendChild(cardSection('存储配额',
    quotaBar,
    el('dl', { class: 'ld-kv' },
      el('dt', { text: '已用空间' }), el('dd', { text: fmtBytes(usage) }),
      el('dt', { text: '回收站占用' }), el('dd', { text: fmtBytes(trashUsed) }),
      el('dt', { text: '总配额' }), el('dd', { text: fmtQuota(user.quota || 0) }),
      el('dt', { text: '下载限速' }), el('dd', { text: user.rateLimitMbps ? `${user.rateLimitMbps} MB/s` : '不限速' }))));

  grid.appendChild(cardSection('关于本服务',
    el('dl', { class: 'ld-kv' },
      el('dt', { text: '版本' }), el('dd', { text: app.me.version || '—' }),
      el('dt', { text: '存储目录' }), el('dd', { class: 'ld-mono', text: app.me.spaceDir || '—' }),
      el('dt', { text: '会话有效期' }), el('dd', { text: app.me.sessionTTL || '—' }),
      el('dt', { text: 'Office 预览' }), el('dd', { text: app.me.officeAvailable ? '服务端可用（转 PDF）' : '未启用，改用内置渲染方案' }),
      el('dt', { text: '我的权限' }), el('dd', { text: (user.permissions || []).map(permissionLabel).join('、') || '无' })),
    noteRow('info', '需要更大配额或更多权限，请联系管理员在管理端调整。')));

  view.append(viewHead('设置', '账号、配额与运行信息'), grid);
  app.shell.main.appendChild(view);
}

/* ===== 7. 操作日志 ======================================================= */

const logsState = { offset: 0, limit: 50, action: '', search: '', from: '', to: '', total: 0 };

function renderLogs(queryString) {
  const params = new URLSearchParams(queryString || '');
  logsState.action = params.get('action') || '';
  logsState.search = params.get('search') || '';
  logsState.from = params.get('from') || '';
  logsState.to = params.get('to') || '';
  logsState.offset = 0;

  const view = el('section', { class: 'ld-view', 'aria-label': '操作日志' });
  const info = el('div', { class: 'ld-pager__info', text: '正在读取…' });

  const actionSelect = el('select', { class: 'ld-select', 'aria-label': '操作类型' },
    el('option', { value: '', text: '全部操作' }),
    ...Object.entries(ACTION_LABELS).map(([key, label]) => el('option', { value: key, text: label })));
  actionSelect.value = logsState.action;
  const searchInput = el('input', { class: 'ld-input', type: 'search', value: logsState.search, placeholder: '搜索对象或说明', 'aria-label': '搜索日志' });
  const fromInput = el('input', { class: 'ld-input', type: 'date', value: logsState.from, 'aria-label': '起始日期' });
  const toInput = el('input', { class: 'ld-input', type: 'date', value: logsState.to, 'aria-label': '结束日期' });

  const filterCard = cardSection('筛选',
    el('div', { class: 'ld-filters' },
      el('ld-field', { label: '操作类型' }, actionSelect),
      el('ld-field', { label: '关键词' }, searchInput),
      el('ld-field', { label: '起始日期' }, fromInput),
      el('ld-field', { label: '结束日期' }, toInput)),
    el('div', { class: 'ld-view__actions' },
      el('ld-button', { label: '导出 CSV', icon: 'download', variant: 'soft', onClick: () => exportLogs() }),
      el('ld-button', {
        label: '查询',
        icon: 'search',
        variant: 'primary',
        onClick: () => {
          logsState.action = actionSelect.value;
          logsState.search = searchInput.value.trim();
          logsState.from = fromInput.value;
          logsState.to = toInput.value;
          logsState.offset = 0;
          loadLogs();
        },
      })));

  const table = el('ld-data-table', {
    rowKey: 'key',
    // 手机上表格会横向溢出，窄屏自动改用卡片
    autoCards: true,
    ariaLabel: '操作日志',
    emptyIcon: 'logs',
    emptyTitle: '没有匹配的日志',
    emptyDesc: '换个时间范围或关键词再试试。',
  });
  table.columns = [
    { key: 'ts', title: '时间', width: '170px', class: 'ld-col-time', render: (row) => fmtDate(row.ts) },
    { key: 'action', title: '操作', width: '130px', render: (row) => actionLabel(row.action) },
    { key: 'target', title: '对象', render: (row) => el('span', { class: 'ld-log-target ld-ellipsis', title: row.target || '', text: row.target || '—' }) },
    { key: 'status', title: '结果', width: '90px', render: (row) => el('span', { class: statusBadgeClass(row.status), text: statusLabel(row.status) }) },
    { key: 'bytes', title: '流量', width: '100px', class: 'ld-col-size', render: (row) => (row.bytes ? fmtBytes(row.bytes) : '—') },
    { key: 'ip', title: '来源 IP', width: '140px', class: 'ld-col-time', render: (row) => row.ip || '—' },
    { key: 'message', title: '说明', render: (row) => el('span', { class: 'ld-log-msg ld-ellipsis', title: row.message || '', text: row.message || '—' }) },
  ];

  const prevButton = el('ld-button', {
    label: '上一页', icon: 'chevron-left', variant: 'ghost', size: 'sm',
    onClick: () => { logsState.offset = Math.max(0, logsState.offset - logsState.limit); loadLogs(); },
  });
  const nextButton = el('ld-button', {
    label: '下一页', icon: 'chevron-right', variant: 'ghost', size: 'sm',
    onClick: () => { logsState.offset += logsState.limit; loadLogs(); },
  });
  const pager = el('div', { class: 'ld-pager' }, info, el('span', { class: 'ld-toolbar__spacer' }), prevButton, nextButton);

  view.append(viewHead('操作日志', '普通用户只能看到自己的记录'), filterCard, table, pager);
  app.shell.main.appendChild(view);

  logsState.table = table;
  logsState.infoNode = info;
  logsState.prevButton = prevButton;
  logsState.nextButton = nextButton;
  loadLogs();
}

function logsQuery(extra) {
  return {
    limit: logsState.limit,
    offset: logsState.offset,
    action: logsState.action,
    search: logsState.search,
    from: logsState.from,
    to: logsState.to,
    ...extra,
  };
}

async function loadLogs() {
  const table = logsState.table;
  if (!table) return;
  table.loading = true;
  try {
    const data = await apiGet(withQuery('/api/logs', logsQuery()));
    table.rows = (data.entries || []).map((entry, index) => ({
      ...entry,
      key: `${entry.ts || ''}#${index}#${entry.action || ''}`,
    }));
    table.loading = false;
    logsState.total = data.total || 0;
    const from = logsState.total ? logsState.offset + 1 : 0;
    const to = Math.min(logsState.total, logsState.offset + logsState.limit);
    logsState.infoNode.textContent = `第 ${from}-${to} 条，共 ${logsState.total} 条`;
    logsState.prevButton.toggleAttribute('disabled', logsState.offset <= 0);
    logsState.nextButton.toggleAttribute('disabled', logsState.offset + logsState.limit >= logsState.total);
  } catch (err) {
    table.loading = false;
    table.rows = [];
    logsState.infoNode.textContent = '';
    toastApiError(err, '读取日志失败');
  }
}

function exportLogs() {
  const link = el('a', {
    href: withQuery('/api/logs', logsQuery({ format: 'csv' })),
    download: 'homevault-logs.csv',
    class: 'ld-sr',
  });
  document.body.appendChild(link);
  link.click();
  link.remove();
  toastInfo('已开始导出 CSV');
}

/* ===== 启动 ============================================================== */

boot();

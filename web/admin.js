/* ===========================================================================
   homevault 管理端入口（/admin.js）
   ---------------------------------------------------------------------------
   1. 启动与鉴权（仅管理员可用）
   2. 外壳与路由
   3. 概览（磁盘、账号数、回收站、上传会话）
   4. 用户管理（新建 / 编辑 / 删除）
   5. 用户文件（管理员代操作，写操作每次都要管理员密码）
   6. 审计日志（筛选、分页、导出 CSV）
   7. 存储与设置（运行期设置、磁盘列表、空间迁移提示）
   8. 关于与帮助（版本、部署升级、CLI 用法）
   安全约定：所有写操作都走 withAdminPassword()，每次都是新的一次询问，
   绝不缓存密码、绝不写入 localStorage；密码只作为本次请求的参数存在。
   =========================================================================== */

import {
  api, apiGet, withAdminPassword, authEvents, el, clearNode, theme, onNarrowChange, session,
  toastInfo, toastSuccess, toastError, toastApiError,
  fmtBytes, fmtQuota, fmtDate, fmtDateTime, fmtUptime, fmtSizeOrDash, nextDay,
  ACTION_LABELS, actionLabel, statusLabel, statusBadgeClass, permissionLabel,
  UploadManager, FileBrowser, createUploadPanel, mountShell, buildAccountBlock, themeToggleButton,
  showLoginView, showCredentialGate, confirmDialog, showMenu, askText, promptPassword,
  withQuery, parseSizeInput, viewHead, cardSection, cardScrollSection, noteRow, sortRows, enableLocalSort,
  donutChart, barChart, lineChart, quotaInput, LdCombobox, append,
} from '/ui.js';

/* ===== 1. 启动与鉴权 ====================================================== */

const app = {
  me: null,
  user: null,
  route: '',
  shell: null,
  users: [],
  usersLoaded: false,
  target: '',
  uploads: null,
  uploadPanel: null,
  browser: null,
};

const ROUTES = ['overview', 'users', 'files', 'logs', 'settings', 'about'];

const ROUTE_TITLES = {
  overview: '概览',
  users: '用户管理',
  files: '用户文件',
  logs: '审计日志',
  settings: '存储与设置',
  about: '关于与帮助',
};

const ROUTE_SUBS = {
  overview: '磁盘、账号与运行状态',
  users: '新建、编辑、禁用与删除账号',
  files: '以管理员身份浏览与操作用户的文件',
  logs: '全部账号的操作记录，可导出 CSV',
  settings: '存储位置、配额默认值与清理策略',
  about: '版本、部署升级与命令行用法',
};

function removeBootSplash() {
  const boot = document.getElementById('ld-boot');
  if (boot) boot.remove();
}

function requireLogin(notice) {
  removeBootSplash();
  if (app.route === 'login') return;
  app.route = 'login';
  showLoginView({
    title: 'HomeVault 管理端',
    subtitle: '请使用管理员账号登录',
    notice,
    onSuccess: (result) => startApp(result),
  });
}

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
    /* 忽略 */
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
    // 首次访问没有会话是常态，不弹"已过期"。
    if (err && err.status === 401) {
      requireLogin();
      return;
    }
    requireLogin((err && err.message) || '无法读取会话。');
    return;
  }
  // 会话探测完成后再挂 401 处理，避免首屏误报"登录状态已过期"。
  authEvents.onUnauthorized = () => requireLogin('登录状态已过期，请重新登录。');
  if (!me.user || !me.user.isAdmin) {
    window.location.replace('/');
    return;
  }
  if (me.user.mustChangeCredentials) {
    requireCredentialChange(me.user);
    return;
  }
  startApp(me);
}

async function startApp(result) {
  let me = result;
  if (!me || !me.version) {
    try {
      me = await apiGet('/api/auth/me');
    } catch {
      me = result;
    }
  }
  if (!me.user || !me.user.isAdmin) {
    window.location.replace('/');
    return;
  }
  app.me = me;
  app.user = me.user;
  session.set({
    ready: true,
    user: me.user,
    home: me.home || '/admin/',
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
  if (!window.location.hash) window.location.hash = '#/overview';
  else renderRoute();
}

/* ===== 2. 外壳与路由 ===================================================== */

function navItems() {
  return [
    { id: 'overview', label: '概览', icon: 'dashboard', hint: '磁盘与运行状态' },
    { id: 'users', label: '用户管理', icon: 'users', hint: '账号与权限' },
    { id: 'files', label: '用户文件', icon: 'folder', hint: '代用户管理文件' },
    { id: 'logs', label: '审计日志', icon: 'logs', hint: '全部操作记录' },
    { id: 'settings', label: '存储与设置', icon: 'settings', hint: '运行期设置' },
    { id: 'about', label: '关于与帮助', icon: 'info', hint: '版本与 CLI' },
  ];
}

function accountBlock() {
  return buildAccountBlock(app.user, {
    onSelect: (id) => {
      if (id === 'logout') logout();
      else if (id === 'settings') go('settings');
    },
  });
}

function buildShell() {
  app.uploads = new UploadManager({ user: '', requirePassword: true });
  // 上传属于写操作：每批上传前都要重新输入一次管理员密码
  app.uploads.setPasswordProvider(() => promptPassword({
    title: '管理员二次验证',
    message: `上传会写入用户 ${app.target || ''} 的存储空间，请输入管理员密码确认。`,
    confirmText: '开始上传',
  }));
  app.shell = mountShell({ brandName: 'HomeVault', brandIcon: 'shield', brandSub: '管理端' });

  const accountTop = accountBlock();
  const narrowQuery = window.matchMedia('(max-width: 859px)');
  accountTop.hidden = !narrowQuery.matches;
  onNarrowChange((narrow) => {
    accountTop.hidden = !narrow;
  });

  app.shell.topRight.append(
    el('ld-button', {
      label: '用户端', icon: 'home', variant: 'ghost', size: 'sm',
      title: '管理员在用户端没有存储空间，点击后会看到说明页',
      onClick: () => { window.location.href = '/'; },
    }),
    themeToggleButton(),
    accountTop,
  );
  app.shell.setAccount(el('div', {},
    accountBlock(),
    el('div', { class: 'ld-view__sub', text: '管理员没有自己的存储空间，只能操作用户的文件' })));
  app.uploadPanel = createUploadPanel(app.uploads);

  window.addEventListener('hashchange', () => renderRoute());
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
  const route = ROUTES.includes(name) ? name : 'overview';
  app.route = route;
  app.shell.fab.hidden = true;
  app.shell.setNav(navItems(), route, (id) => go(id));
  app.shell.setBrandSub(ROUTE_TITLES[route]);
  app.shell.setTitle(`${ROUTE_TITLES[route]} · HomeVault 管理端`);

  const left = app.shell.topLeft;
  clearNode(left);
  left.append(
    el('div', { class: 'ld-top__title', text: ROUTE_TITLES[route] }),
    el('span', { class: 'ld-view__sub ld-ellipsis', text: ROUTE_SUBS[route] }),
  );

  const main = app.shell.main;
  clearNode(main);
  main.scrollTop = 0;
  if (app.browser) {
    app.browser.destroy();
    app.browser = null;
  }

  if (route === 'overview') renderOverview();
  else if (route === 'users') renderUsers();
  else if (route === 'files') renderUserFiles(queryString);
  else if (route === 'logs') renderLogs(queryString);
  else if (route === 'settings') renderSettings();
  else if (route === 'about') renderAbout();
}

/* ===== 3. 概览 =========================================================== */

async function renderOverview() {
  const view = el('section', { class: 'ld-view', 'aria-label': '概览' });
  const kpiBox = el('div', { class: 'ld-kpi' });
  const chartsBox = el('div', { class: 'ld-view__grid' });
  const quotaBox = el('div', { class: 'ld-view__grid' });
  const recentBox = el('div', { class: 'ld-view__grid' });
  view.append(
    viewHead('系统概览', '容量、账号与最近活动一眼看清', [
      el('ld-button', { label: '刷新', icon: 'refresh', variant: 'ghost', onClick: () => loadOverview() }),
    ]),
    kpiBox,
    chartsBox,
    quotaBox,
    recentBox,
  );
  app.shell.main.appendChild(view);
  app.overview = { kpiBox, chartsBox, quotaBox, recentBox };
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

async function loadOverview() {
  const nodes = app.overview;
  if (!nodes) return;
  clearNode(nodes.kpiBox);
  nodes.kpiBox.appendChild(el('div', { class: 'ld-kpi__card' }, el('ld-spinner')));
  try {
    const [stats, sys] = await Promise.all([apiGet('/api/stats'), apiGet('/api/system')]);
    const space = stats.space || {};
    const users = stats.users || {};
    const quota = space.quota || 0;
    const used = space.used || 0;
    const pct = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;

    // KPI
    clearNode(nodes.kpiBox);
    nodes.kpiBox.append(
      kpiCard('空间已用', fmtBytes(used), quota > 0 ? `上限 ${fmtBytes(quota)}（${pct}%）` : '未设置上限'),
      kpiCard('剩余空间', quota > 0 ? fmtBytes(Math.max(0, quota - used)) : '不限', `磁盘可用 ${fmtBytes(sys.diskFree || 0)}`),
      kpiCard('账号', `${users.total ?? 0}`, `普通 ${users.normal ?? 0} · 禁用 ${users.disabled ?? 0}`),
      kpiCard('文件', `${space.fileCount ?? 0}`, `共 ${fmtBytes(space.files || 0)}`),
      kpiCard('回收站', fmtBytes(space.trash || 0), '所有账号合计'),
      kpiCard('运行时长', fmtUptime(stats.server?.uptimeSeconds || 0), `版本 ${stats.server?.version || '—'}`),
    );

    // 容量环形 + 文件类型分布
    clearNode(nodes.chartsBox);
    const donut = donutChart({
      segments: [
        { value: used - (space.trash || 0), color: 'var(--ld-accent)' },
        { value: space.trash || 0, color: 'var(--ld-warn)' },
        { value: Math.max(0, quota - used), color: 'var(--ld-surface-3)' },
      ],
      centerLabel: quota > 0 ? `${pct}%` : '—',
      centerSub: '已用 / 上限',
    });
    const kinds = stats.kinds || [];
    const kindPalette = ['var(--ld-accent)', 'var(--ld-ok)', 'var(--ld-warn)', 'var(--ld-danger)', 'var(--ld-info)', 'var(--ld-muted)'];
    const kindItems = kinds.slice(0, 6).map((k, i) => ({
      label: kindLabel(k.kind), value: k.bytes, color: kindPalette[i % kindPalette.length],
    }));
    nodes.chartsBox.append(
      chartCard('容量占用', quota > 0 ? `上限 ${fmtBytes(quota)}` : '未设置上限', donut,
        legendRow([
          { label: `文件 ${fmtBytes(used - (space.trash || 0))}`, color: 'var(--ld-accent)' },
          { label: `回收站 ${fmtBytes(space.trash || 0)}`, color: 'var(--ld-warn)' },
          { label: `剩余 ${quota > 0 ? fmtBytes(Math.max(0, quota - used)) : '不限'}`, color: 'var(--ld-surface-3)' },
        ])),
      chartCard('文件类型分布', `按占用体积（前 6 类，共 ${kinds.length} 类）`,
        kindItems.length
          ? barChart({ items: kindItems.map((k) => ({ label: k.label, value: k.value, color: k.color })), height: 170 })
          : el('div', { class: 'ld-view__sub', text: '还没有文件' }),
        kindItems.length
          ? legendRow(kindItems.map((k, i) => ({ label: `${k.label} ${fmtBytes(k.value)}`, color: kindPalette[i % kindPalette.length] })))
          : null),
    );

    // 用户配额
    clearNode(nodes.quotaBox);
    const rows = (stats.userUsage || []).filter((u) => !u.isAdmin);
    const quotaList = el('div', { class: 'ld-list' });
    if (!rows.length) {
      quotaList.appendChild(el('div', { class: 'ld-view__sub', text: '还没有普通账号，去「用户管理」创建一个。' }));
    }
    for (const u of rows) {
      const bar = document.createElement('ld-quota-bar');
      bar.used = u.used;
      bar.trashUsed = u.trashUsed;
      bar.quota = u.quota;
      quotaList.appendChild(el('div', { class: 'ld-list__item' },
        el('div', { class: 'ld-view__row' },
          el('div', { class: 'ld-view__row-main' },
            el('div', { text: `${u.displayName || u.username}${u.disabled ? '（已禁用）' : ''}` }),
            el('div', { class: 'ld-view__sub', text: `${u.files} 个文件 · 配额 ${fmtQuota(u.quota)}` })),
          el('div', { class: 'ld-view__row-side' }, el('ld-button', {
            label: '管理', icon: 'settings', variant: 'ghost', size: 'sm',
            onClick: () => go('users'),
          }))),
        bar));
    }
    nodes.quotaBox.append(cardSection('账号配额', quotaList,
      noteRow('info', `空间总容量 ${quota > 0 ? fmtBytes(quota) : '不限'}；所有账号配额之和不能超过它，超出会被拒绝。`)));

    // 近 7 日趋势 + 最近操作
    clearNode(nodes.recentBox);
    const activity = stats.activity || [];
    const line = lineChart({
      points: activity.map((a) => ({ label: a.date.slice(5), value: a.total })),
      height: 160,
    });
    const logs = await apiGet('/api/logs?limit=8');
    const recentList = el('div', { class: 'ld-list' });    for (const entry of (logs.entries || [])) {
      recentList.appendChild(el('div', { class: 'ld-list__item' },
        el('div', { class: 'ld-view__row' },
          el('div', { class: 'ld-view__row-main' },
            el('div', { text: `${actionLabel(entry.action)} · ${entry.target || '—'}` }),
            el('div', { class: 'ld-view__sub', text: `${fmtDateTime(entry.ts)} · ${entry.user || '—'}${entry.actingAs ? '（代办 ' + entry.actingAs + '）' : ''}${entry.status !== 'ok' ? ' · ' + statusLabel(entry.status) : ''}` })),
          el('div', { class: 'ld-view__row-side' },
            el('span', { class: 'ld-badge ' + statusBadgeClass(entry.status), text: statusLabel(entry.status) })))));
    }
    if (!(logs.entries || []).length) {
      recentList.appendChild(el('div', { class: 'ld-view__sub', text: '还没有操作记录' }));
    }
    nodes.recentBox.append(
      chartCard('近 7 天操作趋势', '每天的操作条数', line),
      // 日志条数不固定，卡片体限高内部滚动，避免把整页撑长。
      cardScrollSection('最近操作', recentList, {
        bodyClass: 'ld-card__body--scroll ld-card__body--flush',
        action: el('ld-button', { label: '查看全部日志', icon: 'logs', variant: 'ghost', onClick: () => go('logs') }),
      }),
    );
  } catch (err) {
    clearNode(nodes.kpiBox);
    clearNode(nodes.chartsBox);
    clearNode(nodes.quotaBox);
    clearNode(nodes.recentBox);
    nodes.recentBox.appendChild(cardSection('读取失败', noteRow('danger', (err && err.message) || '无法读取概览数据')));
    toastApiError(err, '读取概览失败');
  }
}

/** 文件类型的中文名（概览图表用）。 */
function kindLabel(kind) {
  const map = {
    folder: '文件夹', image: '图片', video: '视频', audio: '音频', pdf: 'PDF',
    text: '文本', office: 'Office', archive: '压缩包', other: '其它',
  };
  return map[kind] || kind || '其它';
}

/* ===== 4. 用户管理 ====================================================== */

const usersState = { users: [], permissionNames: [], defaults: {}, sort: { key: 'username', dir: 'asc' } };

async function ensureUsers(force = false) {
  if (app.usersLoaded && !force) return usersState;
  const data = await apiGet('/api/users');
  usersState.users = data.users || [];
  usersState.permissionNames = data.permissionNames || [];
  usersState.defaults = data.defaults || {};
  app.users = usersState.users;
  app.usersLoaded = true;
  return usersState;
}

function usersTable(rows) {
  const table = el('ld-data-table', {
    rowKey: 'username',
    ariaLabel: '账号列表',
    rowMenu: true,
    // 双击账号行 = 进入该用户的文件
    clickable: true,
    // 手机上 8 列表格会横向溢出，窄屏自动改用卡片
    autoCards: true,
    emptyIcon: 'users',
    emptyTitle: '还没有账号',
    emptyDesc: '点击“新建用户”创建第一个普通用户。',
  });
  table.columns = [
    {
      key: 'username',
      title: '账号',
      sortable: true,
      // 窄屏卡片里这一列就是卡片标题，不需要「账号：」前缀
      cardHead: true,
      render: (row) => el('div', { class: 'ld-user-cell' },
        el('ld-icon', { name: row.isAdmin ? 'shield' : 'user' }),
        el('div', {},
          el('div', { class: 'ld-user-cell__name', text: row.username }),
          el('div', { class: 'ld-user-cell__sub', text: row.displayName || (row.isAdmin ? '管理员' : '普通用户') }))),
    },
    { key: 'isAdmin', title: '角色', width: '100px', render: (row) => el('span', { class: `ld-badge${row.isAdmin ? ' ld-badge--accent' : ''}`, text: row.isAdmin ? '管理员' : '普通用户' }) },
    { key: 'root', title: '存储空间', width: '128px', render: (row) => (row.isAdmin ? el('span', { class: 'ld-muted', text: '无（管理员）' }) : (row.root || '—')) },
    {
      key: 'used',
      title: '配额使用',
      width: '168px',
      sortable: true,
      // 进度条在卡片里要占满一整行，挤在网格里只剩十几像素宽，看不出比例
      cardWide: true,
      render: (row) => {
        if (row.isAdmin) return el('span', { class: 'ld-muted', text: '—' });
        const bar = document.createElement('ld-quota-bar');
        bar.used = row.used || 0;
        bar.trashUsed = row.trashUsed || 0;
        bar.quota = row.quota || 0;
        bar.legend = false;
        return el('div', {}, bar,
          el('div', { class: 'ld-view__sub', text: `${fmtBytes((row.used || 0) + (row.trashUsed || 0))} / ${row.quota > 0 ? fmtBytes(row.quota) : '不限'}` }));
      },
    },
    { key: 'rateLimitMbps', title: '限速', width: '88px', sortable: true, render: (row) => (row.rateLimitMbps ? `${row.rateLimitMbps} Mbps` : '不限') },
    { key: 'disabled', title: '状态', width: '84px', render: (row) => el('span', { class: `ld-badge ${row.disabled ? 'ld-badge--denied' : 'ld-badge--ok'}`, text: row.disabled ? '已禁用' : '正常' }) },
    {
      key: 'permissions',
      title: '权限',
      // 权限位最多 8 个，全写出来会把表格撑宽、把"操作"列挤出视口。
      // 这里只显示前 3 个 + 剩余数量，完整内容放在 title 里悬浮可看。
      width: '176px',
      render: (row) => {
        if (row.isAdmin) return el('span', { class: 'ld-badge ld-badge--ok', text: '全部权限' });
        const names = (row.permissions || []).map(permissionLabel);
        if (!names.length) return el('span', { class: 'ld-view__sub', text: '无' });
        const shown = names.slice(0, 3);
        const rest = names.length - shown.length;
        return el('span', {
          class: 'ld-perm-cell',
          title: names.join('、'),
        },
        el('span', { text: shown.join('、') }),
        rest > 0 ? el('span', { class: 'ld-perm-more', text: ` +${rest}` }) : null);
      },
    },
    { key: 'createdAt', title: '创建时间', width: '150px', sortable: true, class: 'ld-col-time', render: (row) => fmtDate(row.createdAt) },
    {
      key: 'actions',
      title: '操作',
      width: '150px',
      // 常显：不依赖鼠标悬浮，触屏和键盘也能用
      render: (row) => el('div', { class: 'ld-row-actions' },
        el('ld-button', {
          label: '文件', icon: 'folder', size: 'sm', variant: 'ghost',
          title: row.isAdmin ? '管理员没有自己的存储空间' : `查看 ${row.username} 的文件`,
          onClick: () => openUserFiles(row),
        }),
        el('ld-button', {
          label: '编辑', icon: 'edit', size: 'sm', variant: 'ghost',
          onClick: () => openUserDialog(row),
        })),
    },
  ];
  table.rows = rows;
  table.addEventListener('ld-row-menu', (event) => userMenu(event.detail));
  table.addEventListener('ld-row-open', (event) => openUserFiles(event.detail.row));
  return table;
}

function renderUsers() {
  const view = el('section', { class: 'ld-view', 'aria-label': '用户管理' });
  const summary = el('div', { class: 'ld-view__sub', text: '正在读取账号…' });
  const box = el('div', { class: 'ld-view__stack' });

  view.append(
    viewHead('用户管理', '所有写操作都需要输入管理员密码', [
      el('ld-button', { label: '刷新', icon: 'refresh', variant: 'ghost', onClick: () => loadUsers(true) }),
      el('ld-button', { label: '新建用户', icon: 'plus', variant: 'primary', onClick: () => openUserDialog(null) }),
    ]),
    summary,
    box,
  );
  app.shell.main.appendChild(view);
  usersState.summaryNode = summary;
  usersState.box = box;
  loadUsers();
}

async function loadUsers(force = false) {
  const box = usersState.box;
  if (!box) return;
  clearNode(box);
  box.appendChild(el('div', { class: 'ld-card' }, el('div', { class: 'ld-card__body' },
    el('div', { class: 'ld-view__sub', text: '正在读取账号列表…' }))));
  try {
    const state = await ensureUsers(force);
    const rows = sortRows(state.users, usersState.sort.key, usersState.sort.dir);
    clearNode(box);
    const table = usersTable(rows);
    enableLocalSort(table, usersState, () => usersState.users);
    table.rows = sortRows(usersState.users, usersState.sort.key, usersState.sort.dir);
    box.appendChild(table);
    const admins = state.users.filter((u) => u.isAdmin).length;
    usersState.summaryNode.textContent = `共 ${state.users.length} 个账号（管理员 ${admins} 个）；默认配额 ${fmtBytes(state.defaults.quota || 0)}，默认限速 ${state.defaults.rateLimitMbps ? `${state.defaults.rateLimitMbps} Mbps` : '不限'}`;
  } catch (err) {
    clearNode(box);
    box.appendChild(cardSection('读取失败', noteRow('danger', (err && err.message) || '无法读取账号列表')));
    toastApiError(err, '读取账号列表失败');
    usersState.summaryNode.textContent = '';
  }
}

async function userMenu({ row, target, x, y }) {
  const choice = await showMenu({
    items: [
      { id: 'files', label: '查看其文件', icon: 'folder' },
      { id: 'edit', label: '编辑账号', icon: 'edit' },
      { separator: true },
      { id: 'delete', label: '删除账号', icon: 'delete', danger: true, disabled: row.username === app.user.username },
    ],
    target,
    x,
    y,
    header: `${row.username}${row.displayName ? `（${row.displayName}）` : ''}`,
  });
  if (choice === 'files') openUserFiles(row);
  else if (choice === 'edit') openUserDialog(row);
  else if (choice === 'delete') deleteUser(row);
}

function openUserFiles(row) {
  if (row.isAdmin) {
    toastInfo('管理员没有自己的存储空间，无法浏览其文件');
    return;
  }
  app.target = row.username;
  go('files', `user=${encodeURIComponent(row.username)}`);
}

/** 新建 / 编辑账号弹窗。user 为空表示新建。 */
async function openUserDialog(user) {
  const isNew = !user;
  const editingSelf = !!user && user.username === app.user.username;
  const editingAdmin = !!user && !!user.isAdmin;
  const state = await ensureUsers().catch(() => usersState);
  // 后端下发普通账号可用的权限位（含回收站查看/管理、日志查看）；
  // 这些权限按账号自己的维度隔离，所以可由管理员按需授予普通用户。
  const allPerms = (state.permissionNames && state.permissionNames.length)
    ? state.permissionNames
    : ['read', 'upload', 'modify', 'delete'];
  const defaultPerms = ['read', 'upload', 'modify', 'delete'];
  const defaultQuota = (state.defaults && state.defaults.quota) || 0;

  const username = el('input', {
    class: 'ld-input', type: 'text', value: user ? user.username : '',
    autocomplete: 'off', spellcheck: 'false', 'aria-label': '账号',
  });
  const password = el('input', {
    class: 'ld-input', type: 'password', autocomplete: 'new-password', 'aria-label': '密码',
    placeholder: isNew ? '至少 8 个字符' : '留空表示不修改',
  });
  const displayName = el('input', {
    class: 'ld-input', type: 'text', value: user ? user.displayName || '' : '', 'aria-label': '显示名',
  });
  const disabled = el('input', { type: 'checkbox', checked: user ? !!user.disabled : false, 'aria-label': '禁用账号' });
  const root = el('input', {
    class: 'ld-input', type: 'text', value: user ? user.root || '' : '',
    placeholder: '留空 = 使用小写账号名', 'aria-label': '存储空间目录名',
  });
  const quota = quotaInput({
    value: user && user.quota ? user.quota : (isNew ? defaultQuota : 0),
    label: '配额',
  });
  const rate = el('input', {
    class: 'ld-input', type: 'number', min: '0',
    value: user && user.rateLimitMbps ? String(user.rateLimitMbps) : '',
    placeholder: '0 = 不限', 'aria-label': '限速 Mbps',
  });

  const permBoxes = new Map();
  const permGrid = el('div', { class: 'perm-grid' });
  for (const key of allPerms) {
    const checked = user ? (user.permissions || []).includes(key) : defaultPerms.includes(key);
    const box = el('input', { type: 'checkbox', checked, 'aria-label': permissionLabel(key) });
    permBoxes.set(key, box);
    permGrid.appendChild(el('label', { class: 'ld-check' }, box, el('span', { text: permissionLabel(key) })));
  }

  const error = el('div', { class: 'ld-note ld-note--danger ld-hidden' },
    el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: 'alert' })),
    el('div', { class: 'ld-note__body' }));

  const rootField = el('ld-field', { label: '存储空间目录名', hint: '相对于空间根目录，留空则使用小写账号名' }, root);
  const quotaField = el('ld-field', { label: '配额', hint: '选择单位后填数字；不能超过空间总容量' }, quota.node);
  const rateField = el('ld-field', { label: '限速（Mbps）', hint: '0 = 不限速' }, rate);
  const permField = el('ld-field', { label: '权限' }, permGrid);
  const disabledField = el('ld-field', { label: '状态' },
    el('label', { class: 'ld-check' }, disabled, el('span', { text: '禁用该账号（禁止登录）' })));

  const adminNote = noteRow('info',
    '系统只有一个管理员账号：管理员没有自己的存储空间，配额、目录与权限位对它都不适用，只能管理别人的文件。');

  const form = el('form', { class: 'ld-form', novalidate: true },
    el('ld-field', { label: '账号', required: true }, username),
    el('ld-field', { label: isNew ? '密码' : '新密码', required: isNew, hint: '至少 8 个字符' }, password),
    el('ld-field', { label: '显示名' }, displayName));
  if (editingAdmin) {
    form.appendChild(adminNote);
  } else {
    form.append(rootField, quotaField, rateField, permField);
    // 不能停用自己：管理员把自己关掉就等于把系统锁死
    if (!editingSelf) form.appendChild(disabledField);
  }
  form.appendChild(error);
  if (!isNew) username.setAttribute('disabled', '');

  const dialog = document.createElement('ld-modal');
  dialog.title = isNew ? '新建用户' : `编辑用户 ${user.username}`;
  dialog.size = 'md';
  dialog.append(form);
  document.body.appendChild(dialog);
  dialog.setActions([
    { label: '取消', value: 'cancel' },
    { label: isNew ? '创建' : '保存', value: 'ok', variant: 'primary' },
  ]);

  const showError = (message) => {
    error.querySelector('.ld-note__body').textContent = message;
    error.classList.remove('ld-hidden');
  };

  const submit = async () => {
    error.classList.add('ld-hidden');
    const name = username.value.trim();
    if (isNew && !name) return showError('请输入账号');
    if (isNew && password.value.length < 8) return showError('密码至少 8 个字符');
    if (!isNew && password.value && password.value.length < 8) return showError('新密码至少 8 个字符');

    const body = {};
    if (isNew) {
      body.username = name;
      body.displayName = displayName.value.trim();
    } else {
      body.displayName = displayName.value.trim();
      if (!editingAdmin && !editingSelf) body.disabled = disabled.checked;
    }
    if (password.value) body[isNew ? 'password' : 'newPassword'] = password.value;

    if (!editingAdmin) {
      const perms = Array.from(permBoxes.entries()).filter(([, box]) => box.checked).map(([key]) => key);
      const fallbackRoot = (isNew ? name : user.root || user.username).toLowerCase();
      body.root = root.value.trim() || fallbackRoot;
      body.quota = quota.getValue();
      if (!body.quota) return showError('请填写配额（不能为 0；空间有总容量上限）');
      const rateValue = rate.value.trim();
      body.rateLimitMbps = rateValue ? Number(rateValue) || 0 : 0;
      body.permissions = perms;
    }

    return withAdminPassword(async (adminPassword) => {
      if (isNew) {
        await api('/api/users', { method: 'POST', body, password: adminPassword, passwordField: 'adminPassword' });
        toastSuccess(`已创建账号 ${name}`);
      } else {
        await api(`/api/users/${encodeURIComponent(user.username)}`, {
          method: 'PATCH', body, password: adminPassword, passwordField: 'adminPassword',
        });
        toastSuccess(`已保存 ${user.username} 的修改`);
      }
      dialog.close();
      await loadUsers(true);
      return true;
    }, {
      title: isNew ? '创建账号' : '保存账号修改',
      message: isNew ? `将创建账号 ${name}，请输入管理员密码确认。` : `将修改账号 ${user.username}，请输入管理员密码确认。`,
      confirmText: isNew ? '创建' : '保存',
    }).then((done) => {
      if (done === null) toastInfo('已取消');
    }).catch((err) => {
      showError((err && err.message) || '保存失败');
      toastApiError(err, '保存失败');
    });
  };

  dialog.addEventListener('ld-action', (event) => {
    const value = event.detail.value;
    if (value === 'ok') submit();
    else if (value === 'cancel') dialog.close();
  });
  dialog.addEventListener('ld-close', () => dialog.remove());
  dialog.open();
}

/** 删除账号：勾选是否同时删除文件，再二次确认。 */
async function deleteUser(user) {
  const withFiles = el('input', { type: 'checkbox', 'aria-label': '同时删除其全部文件' });
  const box = el('div', { class: 'ld-form' },
    el('div', { class: 'ld-modal__msg', text: `即将删除账号 ${user.username}${user.displayName ? `（${user.displayName}）` : ''}。` }),
    el('label', { class: 'ld-check' }, withFiles, el('span', { text: '同时删除该账号的全部文件与回收站内容（不可恢复）' })),
    noteRow('warn', '账号删除后无法恢复；不改密码的情况下请谨慎操作。'));

  const dialog = document.createElement('ld-modal');
  dialog.title = '删除账号';
  dialog.size = 'sm';
  dialog.append(box);
  document.body.appendChild(dialog);
  dialog.setActions([
    { label: '取消', value: 'cancel' },
    { label: '继续', value: 'ok', variant: 'danger' },
  ]);
  dialog.open();

  const goOn = await new Promise((resolve) => {
    dialog.addEventListener('ld-action', (event) => resolve(event.detail.value === 'ok'));
    dialog.addEventListener('ld-close', () => resolve(false));
  });
  const alsoFiles = withFiles.checked;
  dialog.remove();
  if (!goOn) return;

  const confirmed = await confirmDialog({
    title: '再次确认删除',
    message: `确认永久删除账号 ${user.username} 吗？`,
    details: [
      `存储空间：${user.isAdmin ? '无（管理员）' : user.root || '—'}`,
      alsoFiles ? '同时删除该账号的全部文件与回收站内容' : '保留其文件（仅删除账号记录）',
    ],
    danger: true,
    confirmText: '永久删除',
  });
  if (!confirmed) return;

  try {
    const done = await withAdminPassword(async (adminPassword) => {
      const path = `/api/users/${encodeURIComponent(user.username)}${alsoFiles ? '?deleteFiles=1' : ''}`;
      await api(path, { method: 'DELETE', password: adminPassword, passwordField: 'adminPassword' });
      return true;
    }, {
      title: '删除账号',
      message: `请输入管理员密码以删除账号 ${user.username}。`,
      confirmText: '删除',
    });
    if (done === null) {
      toastInfo('已取消');
      return;
    }
    toastSuccess(`已删除账号 ${user.username}`);
    if (app.target === user.username) app.target = '';
    await loadUsers(true);
  } catch (err) {
    toastApiError(err, '删除账号失败');
  }
}

/* ===== 5. 用户文件 ====================================================== */

function renderUserFiles(queryString) {
  const params = new URLSearchParams(queryString || '');
  const asked = params.get('user') || app.target || '';
  const view = el('section', { class: 'ld-view', 'aria-label': '用户文件' });
  const picker = el('select', { class: 'ld-select', 'aria-label': '选择要管理的账号' },
    el('option', { value: '', text: '请选择账号…' }));
  const banner = el('div', {});
  const host = el('div', { class: 'ld-files-host' });

  view.append(
    viewHead('用户文件', '以管理员身份浏览与操作用户的文件', [
      el('ld-button', { label: '刷新账号列表', icon: 'refresh', variant: 'ghost', onClick: () => fillUserPicker(picker, asked) }),
    ]),
    cardSection('选择账号', el('div', { class: 'ld-toolbar' },
      el('div', { class: 'ld-toolbar__group ld-toolbar__grow' }, picker),
      pickerHint())),
    banner,
    host,
  );
  app.shell.main.appendChild(view);
  app.filesView = { picker, banner, host, target: asked };

  picker.addEventListener('change', () => {
    app.target = picker.value;
    mountTargetFiles(picker.value);
  });

  fillUserPicker(picker, asked).then(() => {
    if (asked) {
      picker.value = asked;
      app.target = asked;
      mountTargetFiles(asked);
    } else {
      clearNode(banner);
      clearNode(host);
      banner.appendChild(noteRow('info', '请先在上方选择一个账号，之后所有文件操作都会记录到审计日志。'));
      app.shell.fab.hidden = true;
    }
  });
  renderUploadPanel();
}

function pickerHint() {
  return el('span', { class: 'ld-view__sub', text: '管理员没有自己的存储空间，必须先选择目标账号' });
}

async function fillUserPicker(picker, keep) {
  const current = keep || picker.value;
  try {
    const state = await ensureUsers();
    clearNode(picker);
    picker.appendChild(el('option', { value: '', text: '请选择账号…' }));
    for (const user of state.users) {
      picker.appendChild(el('option', {
        value: user.username,
        text: `${user.username}${user.displayName ? `（${user.displayName}）` : ''}${user.isAdmin ? ' · 管理员无存储空间' : ''}${user.disabled ? ' · 已禁用' : ''}`,
      }));
    }
    if (current) picker.value = current;
  } catch (err) {
    toastApiError(err, '读取账号列表失败');
  }
}

function renderUploadPanel() {
  if (app.uploadPanel && !app.uploadPanel.panel.isConnected) {
    document.body.appendChild(app.uploadPanel.panel);
  }
}

/** 为目标账号挂上文件管理器（复用用户端的 FileBrowser）。 */
function mountTargetFiles(target) {
  const nodes = app.filesView;
  if (!nodes) return;
  clearNode(nodes.banner);
  clearNode(nodes.host);
  if (app.browser) {
    app.browser.destroy();
    app.browser = null;
  }
  app.shell.fab.hidden = true;
  if (!target) {
    nodes.banner.appendChild(noteRow('info', '请先选择一个账号。'));
    return;
  }
  const user = usersState.users.find((item) => item.username === target);
  if (user && user.isAdmin) {
    nodes.banner.appendChild(noteRow('warn', `${target} 是管理员账号，没有自己的存储空间，因此无法浏览其文件。`));
    return;
  }
  // 用安全 append：它会跳过 null，不会在页面上漏出字符串 "null"。
  append(nodes.banner,
    noteRow('warn', `你正在以管理员身份操作用户 ${target} 的文件，所有操作都会记录审计日志；每次写操作都需要重新输入管理员密码。`),
    user && user.disabled ? noteRow('danger', `${target} 当前处于禁用状态，其文件仍可浏览与整理。`) : null,
  );

  app.uploads.setUser(target);
  const browser = new FileBrowser(nodes.host, {
    user: target,
    isAdmin: true,
    permissions: [],
    requirePassword: true,
    uploads: app.uploads,
    showQuota: true,
    rootLabel: target,
  });
  browser.addEventListener('ld-upload-open', () => {
    if (app.uploadPanel) app.uploadPanel.panel.collapsed = false;
  });
  browser.mount();
  app.browser = browser;
  app.shell.fab.hidden = false;
  browser.load('');
  app.uploads.refreshSessions();
}

/* ===== 6. 审计日志 ====================================================== */

const logsState = { offset: 0, limit: 100, total: 0, action: '', search: '', user: '', from: '', to: '' };

function renderLogs(queryString) {
  const params = new URLSearchParams(queryString || '');
  logsState.action = params.get('action') || '';
  logsState.search = params.get('search') || '';
  logsState.user = params.get('user') || '';
  logsState.from = params.get('from') || '';
  logsState.to = params.get('to') || '';
  logsState.offset = 0;

  // 账号下拉要列出所有账号，直接进「审计日志」时用户列表还没拉过，这里补一次。
  // 拉取失败不阻塞日志：下拉里只剩「全部账号」，日志照常显示。
  if (!usersState.users.length) {
    ensureUsers().then(() => {
      if (app.route !== 'logs') return;
      const opts = [{ value: '', label: '全部账号' }].concat(usersState.users.map((u) => ({
        value: u.username,
        label: `${u.username}${u.displayName ? `（${u.displayName}）` : ''}`,
        hint: u.isAdmin ? '管理员' : '',
        icon: u.isAdmin ? 'shield' : 'user',
      })));
      logsState.userCombo.props = { options: opts, value: logsState.user };
      logsState.userCombo.render();
    }).catch(() => {});
  }

  const view = el('section', { class: 'ld-view', 'aria-label': '审计日志' });
  const info = el('div', { class: 'ld-pager__info', text: '正在读取…' });

  // 账号与操作类型都可能很长，用可输入过滤的下拉，避免一长串选项里逐个找。
  const userOptions = [{ value: '', label: '全部账号' }].concat(
    (usersState.users.length ? usersState.users : app.users || []).map((u) => ({
      value: u.username,
      label: `${u.username}${u.displayName ? `（${u.displayName}）` : ''}`,
      hint: u.isAdmin ? '管理员' : '',
      icon: u.isAdmin ? 'shield' : 'user',
    })));
  const userCombo = document.createElement('ld-combobox');
  userCombo.props = { options: userOptions, value: logsState.user };
  userCombo.setAttribute('value', logsState.user);
  userCombo.setAttribute('placeholder', '全部账号');
  // 记下引用：ensureUsers() 异步返回后要把账号列表补进这个下拉。
  logsState.userCombo = userCombo;

  const actionOptions = [{ value: '', label: '全部操作' }].concat(
    Object.entries(ACTION_LABELS).map(([key, label]) => ({ value: key, label })));
  const actionCombo = document.createElement('ld-combobox');
  actionCombo.props = { options: actionOptions, value: logsState.action };
  actionCombo.setAttribute('value', logsState.action);
  actionCombo.setAttribute('placeholder', '全部操作');

  const searchInput = el('input', { class: 'ld-input', type: 'search', value: logsState.search, placeholder: '搜索对象或说明', 'aria-label': '搜索日志' });
  const fromInput = el('input', { class: 'ld-input', type: 'date', value: logsState.from, 'aria-label': '起始日期' });
  const toInput = el('input', { class: 'ld-input', type: 'date', value: logsState.to, 'aria-label': '结束日期' });

  const filterCard = cardSection('筛选',
    el('div', { class: 'ld-filters' },
      el('ld-field', { label: '账号' }, userCombo),
      el('ld-field', { label: '操作类型' }, actionCombo),
      el('ld-field', { label: '关键词' }, searchInput),
      el('ld-field', { label: '起始日期' }, fromInput),
      el('ld-field', { label: '结束日期' }, toInput)),
    // 操作按钮单独一行靠右，避免和筛选网格挤在一起折行
    el('div', { class: 'ld-filters__tools' },
      el('ld-button', { label: '清除旧日志', icon: 'trash', variant: 'ghost', onClick: () => purgeLogs() }),
      el('ld-button', { label: '导出 CSV', icon: 'download', variant: 'soft', onClick: () => exportLogs() }),
      el('ld-button', {
        label: '查询',
        icon: 'search',
        variant: 'primary',
        onClick: () => {
          logsState.user = userCombo.value || '';
          logsState.action = actionCombo.value || '';
          logsState.search = searchInput.value.trim();
          logsState.from = fromInput.value;
          logsState.to = toInput.value;
          logsState.offset = 0;
          loadLogs();
        },
      })));

  const table = el('ld-data-table', {
    rowKey: 'key',
    ariaLabel: '审计日志',
    // 手机上 9 列表格会横向溢出，窄屏自动改用卡片
    autoCards: true,
    emptyIcon: 'logs',
    emptyTitle: '没有匹配的日志',
    emptyDesc: '换个账号、时间范围或关键词再试试。',
    // 日志表格自己内部滚动，分页条才能一直留在视口内：
    // 视口高减去"标题+筛选卡片+分页条"占用的固定高度（实测约 470px），留 40px 余量。
    maxHeight: 'max(280px, calc(100vh - 510px))',
  });
  table.columns = [
    { key: 'ts', title: '时间', width: '180px', class: 'ld-col-time', render: (row) => fmtDateTime(row.ts) },
    { key: 'user', title: '账号', width: '120px', render: (row) => row.user || '—' },
    { key: 'actingAs', title: '代办账号', width: '110px', render: (row) => row.actingAs || '—' },
    { key: 'action', title: '操作', width: '110px', render: (row) => actionLabel(row.action) },
    { key: 'target', title: '对象', render: (row) => el('span', { class: 'ld-log-target ld-ellipsis', title: row.target || '', text: row.target || '—' }) },
    { key: 'status', title: '结果', width: '80px', render: (row) => el('span', { class: statusBadgeClass(row.status), text: statusLabel(row.status) }) },
    { key: 'bytes', title: '流量', width: '96px', class: 'ld-col-size', render: (row) => (row.bytes ? fmtBytes(row.bytes) : '—') },
    { key: 'ip', title: '来源 IP', width: '130px', class: 'ld-col-time', render: (row) => row.ip || '—' },
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
  const pagesBox = el('div', { class: 'ld-pager__pages' });

  // 每页条数
  const sizeSelect = el('select', { 'aria-label': '每页显示条数' },
    [20, 50, 100, 200].map((n) => el('option', { value: String(n), text: `${n} 条/页` })));
  sizeSelect.value = String(logsState.limit);
  sizeSelect.addEventListener('change', () => {
    logsState.limit = Number(sizeSelect.value) || 100;
    logsState.offset = 0; // 改每页条数后回到第一页
    loadLogs();
  });
  const sizeBox = el('div', { class: 'ld-pager__size' }, el('span', { text: '每页' }), sizeSelect);

  const pager = el('div', { class: 'ld-pager' },
    info, el('span', { class: 'ld-toolbar__spacer' }),
    sizeBox, pagesBox, prevButton, nextButton);

  view.append(viewHead('审计日志', '所有账号的操作记录，可按账号与操作类型过滤'), filterCard, table, pager);
  app.shell.main.appendChild(view);

  logsState.table = table;
  logsState.infoNode = info;
  logsState.prevButton = prevButton;
  logsState.nextButton = nextButton;
  logsState.pagesBox = pagesBox;
  loadLogs();
}

/**
 * 渲染页码：首尾页 + 当前页附近，其余用省略号折叠。
 * 例：1 … 4 5 [6] 7 8 … 20
 */
function paintPagerPages() {
  const box = logsState.pagesBox;
  if (!box) return;
  clearNode(box);
  const total = logsState.total;
  const pages = Math.max(1, Math.ceil(total / logsState.limit));
  const current = Math.min(pages, Math.floor(logsState.offset / logsState.limit) + 1);
  if (pages <= 1) return;

  const goPage = (p) => {
    logsState.offset = (p - 1) * logsState.limit;
    loadLogs();
  };

  // 生成要显示的页码序列，null 代表省略号
  const shown = new Set([1, pages, current]);
  for (let d = 1; d <= 2; d += 1) {
    if (current - d >= 1) shown.add(current - d);
    if (current + d <= pages) shown.add(current + d);
  }
  const seq = [...shown].sort((a, b) => a - b);

  let last = 0;
  for (const p of seq) {
    if (last && p - last > 1) box.appendChild(el('span', { class: 'ld-page-btn ld-page-btn--gap', text: '…' }));
    box.appendChild(el('button', {
      class: `ld-page-btn${p === current ? ' is-current' : ''}`,
      type: 'button',
      text: String(p),
      'aria-label': `第 ${p} 页`,
      'aria-current': p === current ? 'page' : undefined,
      onclick: p === current ? undefined : () => goPage(p),
    }));
    last = p;
  }
}

function logsQuery(extra) {
  return {
    limit: logsState.limit,
    offset: logsState.offset,
    user: logsState.user,
    action: logsState.action,
    search: logsState.search,
    from: logsState.from,
    // 结束日期按“包含当天”处理：服务端只接受一个时间点，这里补到次日 00:00
    to: logsState.to ? nextDay(logsState.to) : '',
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
    const pages = Math.max(1, Math.ceil(logsState.total / logsState.limit));
    const currentPage = Math.min(pages, Math.floor(logsState.offset / logsState.limit) + 1);
    logsState.infoNode.textContent = logsState.total
      ? `第 ${from}-${to} 条，共 ${logsState.total} 条（第 ${currentPage}/${pages} 页）`
      : '共 0 条';
    logsState.prevButton.toggleAttribute('disabled', logsState.offset <= 0);
    logsState.nextButton.toggleAttribute('disabled', logsState.offset + logsState.limit >= logsState.total);
    paintPagerPages();
  } catch (err) {
    table.loading = false;
    table.rows = [];
    logsState.infoNode.textContent = '';
    toastApiError(err, '读取日志失败');
  }
}

/**
 * 清除旧日志：只能删 2 个月以前的记录。
 * 流程：先查能删多少 → 明确告知边界与后果 → 二次确认 → 输入管理员密码 → 执行。
 */
async function purgeLogs() {
  let info;
  try {
    info = await apiGet('/api/logs/purge');
  } catch (err) {
    toastApiError(err, '无法读取可清理的日志信息');
    return;
  }
  const keepDays = info.keepDays || 60;
  const cutoff = info.cutoff ? info.cutoff.slice(0, 10) : '';
  if (!info.files) {
    toastInfo(`没有可清除的日志：最近 ${keepDays} 天的记录受保护，只有 ${cutoff} 之前的记录才能删除`);
    return;
  }

  const ok = await confirmDialog({
    title: '清除历史日志？',
    message: `只有 ${keepDays} 天以前的记录允许删除，此操作不可撤销。`,
    details: [
      `删除范围：${cutoff} 之前`,
      `文件数：${info.files} 个`,
      `记录数：${info.entries} 条`,
      `释放空间：约 ${fmtBytes(info.bytes)}`,
      `保留范围：${cutoff} 起的最近 ${keepDays} 天`,
    ],
    confirmText: '继续',
    danger: true,
  });
  if (!ok) return;

  const res = await withAdminPassword(
    (password) => api('/api/logs/purge', { method: 'POST', body: { password } }),
    {
      title: '清除日志',
      message: `即将删除 ${cutoff} 之前的 ${info.files} 个日志文件。请输入管理员密码确认。`,
      confirmText: '确认清除',
    },
  );
  if (res === null) return; // 用户取消

  toastSuccess(`已清除 ${res.files} 个日志文件，释放 ${fmtBytes(res.bytes)}`);
  // 清理后刷新列表与页码
  logsState.offset = 0;
  loadLogs();
}

function exportLogs() {  const link = el('a', {
    href: withQuery('/api/logs', logsQuery({ format: 'csv' })),
    download: 'homevault-logs.csv',
    class: 'ld-sr',
  });
  document.body.appendChild(link);
  link.click();
  link.remove();
  toastInfo('已开始导出 CSV');
}

/* ===== 7. 存储与设置 =================================================== */

const settingsState = { settings: null, effective: null, restartRequired: [], disks: [] };

async function renderSettings() {
  const view = el('section', { class: 'ld-view', 'aria-label': '存储与设置' });
  const box = el('div', { class: 'ld-view__stack', text: '正在读取设置…' });
  view.append(viewHead('存储与设置', '运行期设置修改后立即生效；切换存储位置需要重启'), box);
  app.shell.main.appendChild(view);
  settingsState.box = box;
  await loadSettings();
}

async function loadSettings() {
  const box = settingsState.box;
  if (!box) return;
  clearNode(box);
  box.appendChild(el('div', { class: 'ld-card' }, el('div', { class: 'ld-card__body' },
    el('div', { class: 'ld-view__sub', text: '正在读取设置…' }))));
  try {
    const [data, diskData, stats] = await Promise.all([
      apiGet('/api/settings'),
      apiGet('/api/system/disks').catch(() => ({ disks: [], currentSpaceDir: '', defaultSpaceDir: '' })),
      apiGet('/api/stats').catch(() => null),
    ]);
    settingsState.settings = data.settings || {};
    settingsState.effective = data.effective || {};
    settingsState.restartRequired = data.restartRequired || [];
    settingsState.disks = diskData.disks || [];
    settingsState.currentSpaceDir = diskData.currentSpaceDir || data.effective?.spaceDir || '';
    settingsState.defaultSpaceDir = diskData.defaultSpaceDir || '';
    settingsState.stats = stats;
    renderSettingsForm();
  } catch (err) {
    clearNode(box);
    box.appendChild(cardSection('读取失败', noteRow('danger', (err && err.message) || '无法读取设置')));
    toastApiError(err, '读取设置失败');
  }
}

function renderSettingsForm() {
  const box = settingsState.box;
  const s = settingsState.settings;
  const eff = settingsState.effective;
  clearNode(box);

  const restartBanner = settingsState.restartRequired.includes('spaceDir')
    ? noteRow('warn', '存储位置已写入配置文件，需要重启 homevault 服务后才会真正切换。')
    : null;

  /* --- 存储位置 --- */
  const spaceDir = el('input', { class: 'ld-input', type: 'text', value: eff.spaceDir || s.spaceDir || '', 'aria-label': '存储位置' });
  const diskBox = el('div', { class: 'ld-view__stack' });
  const renderDisks = () => {
    clearNode(diskBox);
    if (!settingsState.disks.length) return;
    const list = el('div', { class: 'ld-list' });
    for (const disk of settingsState.disks) {
      list.appendChild(el('div', { class: 'ld-list__item' },
        el('div', {},
          el('div', { class: 'ld-mono', text: disk.path }),
          el('div', { class: 'ld-view__sub', text: `共 ${fmtBytes(disk.total || 0)}，可用 ${fmtBytes(disk.free || 0)}${disk.remarks ? ` · ${disk.remarks}` : ''}` })),
        el('ld-button', {
          label: '填入',
          icon: 'check',
          variant: 'ghost',
          size: 'sm',
          onClick: () => {
            spaceDir.value = disk.path;
            toastInfo(`已填入 ${disk.path}，请确认后再保存`);
          },
        })));
    }
    diskBox.appendChild(el('details', { class: 'ld-card' },
      el('summary', { class: 'ld-section-title', text: `本机磁盘（${settingsState.disks.length} 个）` }),
      el('div', { class: 'ld-card__body' }, list,
        noteRow('info', '保存新的存储位置时，服务端会自动把现有数据复制过去，然后提示重启——不需要再去命令行跑迁移命令。'))));
  };
  renderDisks();

  const storageCard = cardSection('存储位置',
    restartBanner,
    el('ld-field', {
      label: '空间目录（绝对路径）',
      hint: `当前生效：${eff.spaceDir || '—'} · 系统默认：${settingsState.defaultSpaceDir || '—'}`,
    }, spaceDir),
    el('div', { class: 'ld-view__actions' },
      el('ld-button', { label: '显示本机磁盘', icon: 'disk', variant: 'ghost', onClick: () => { renderDisks(); toastInfo('已展开磁盘列表'); } })),
    noteRow('warn', '改到新位置时，会先把文件、回收站、日志整体复制过去（文件多时需要等一会儿），成功后需要重启服务。'),
    diskBox);

  /* --- 空间总容量 --- */
  const spaceQuota = quotaInput({ value: s.spaceQuota || 0, label: '空间总容量' });
  const spaceUsedNow = (settingsState.stats && settingsState.stats.space && settingsState.stats.space.used) || 0;
  const spaceCard = cardSection('空间总容量',
    el('ld-field', {
      label: '整个空间目录的上限',
      hint: `当前已用 ${fmtBytes(spaceUsedNow)}；所有账号配额之和不能超过这个值`,
    }, spaceQuota.node),
    noteRow('info', '默认 5 GB；0 = 不限。例：上限 10 GB，A 分了 8 GB，B 最多只能再分 2 GB。'));
  const defaultQuota = quotaInput({ value: s.defaultQuota || 0, label: '默认配额' });
  const defaultRate = el('input', { class: 'ld-input', type: 'number', min: '0', value: String(s.defaultRateLimitMbps ?? 0), 'aria-label': '默认限速' });
  const defaultsCard = cardSection('新用户默认值',
    el('div', { class: 'ld-filters' },
      el('ld-field', { label: '默认配额', hint: '新建账号时预填；不能超过空间总容量' }, defaultQuota.node),
      el('ld-field', { label: '默认限速（Mbps）', hint: '0 = 不限速' }, defaultRate)));

  /* --- 限制与保留 --- */
  const maxUpload = el('input', { class: 'ld-input', type: 'text', value: fmtBytes(s.maxUploadSize || 0), 'aria-label': '单文件上限' });
  const chunkSize = el('input', { class: 'ld-input', type: 'text', value: fmtBytes(s.chunkSize || 0), 'aria-label': '分片大小' });
  const uploadTTL = el('input', { class: 'ld-input', type: 'text', value: s.uploadSessionTTL || '0', 'aria-label': '上传会话有效期' });
  const trashRetention = el('input', { class: 'ld-input', type: 'text', value: s.trashRetention || '0', 'aria-label': '回收站保留期' });
  const logRetention = el('input', { class: 'ld-input', type: 'text', value: s.logRetention || '0', 'aria-label': '日志保留期' });
  const sessionTTL = el('input', { class: 'ld-input', type: 'text', value: s.sessionTTL || '', 'aria-label': '登录会话有效期' });
  const limitsCard = cardSection('上传与保留策略',
    el('div', { class: 'ld-filters' },
      el('ld-field', { label: '单文件上限', hint: '0 = 不限；不能小于分片大小' }, maxUpload),
      el('ld-field', { label: '分片大小', hint: '建议 4MiB ~ 32MiB，最小 64KiB' }, chunkSize),
      el('ld-field', { label: '上传会话有效期', hint: '如 24h / 7d，必须大于 0' }, uploadTTL),
      el('ld-field', { label: '回收站保留期', hint: '如 720h / 30d，0 = 永不清理' }, trashRetention),
      el('ld-field', { label: '审计日志保留期', hint: '如 2160h / 90d，0 = 永久保留' }, logRetention),
      el('ld-field', { label: '登录会话有效期', hint: '如 168h / 7d，必须大于 0' }, sessionTTL)));

  /* --- 预览与网络 --- */
  const officePreview = el('select', { class: 'ld-select', 'aria-label': 'Office 预览方式' },
    el('option', { value: 'auto', text: 'auto（有 LibreOffice 就用服务端转换）' }),
    el('option', { value: 'off', text: 'off（关闭服务端 Office 预览）' }),
    el('option', { value: 'builtin', text: 'builtin（仅用前端内置方案）' }));
  officePreview.value = s.officePreview || 'auto';
  const trustProxy = el('input', { type: 'checkbox', checked: !!s.trustProxy, 'aria-label': '信任反向代理' });
  const accessLogDisabled = el('input', { type: 'checkbox', checked: !!s.accessLogDisabled, 'aria-label': '关闭访问日志' });
  const runtimeCard = cardSection('预览与网络',
    el('div', { class: 'ld-filters' },
      el('ld-field', { label: 'Office 预览', hint: `当前环境：${eff.officeAvailable ? '可用（服务端可转换）' : '不可用'}` }, officePreview)),
    el('label', { class: 'ld-check' }, trustProxy, el('span', { text: '信任反向代理传来的 X-Forwarded-For（仅在确实部署在代理后开启）' })),
    el('label', { class: 'ld-check' }, accessLogDisabled, el('span', { text: '关闭 HTTP 访问日志（审计日志不受影响）' })));

  const form = el('form', { class: 'ld-form', novalidate: true },
    storageCard, spaceCard, defaultsCard, limitsCard, runtimeCard,
    el('div', { class: 'ld-view__actions' },
      el('ld-button', { label: '重新读取', icon: 'refresh', variant: 'ghost', onClick: () => loadSettings() }),
      el('ld-button', { label: '保存设置', icon: 'check', variant: 'primary', type: 'submit' })));

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const badSize = () => toastError('容量格式不正确，请填数字并选择单位');
    const quotaBytes = defaultQuota.getValue();
    const spaceBytes = spaceQuota.getValue();
    const maxBytes = maxUpload.value.trim() ? sizeOrDefault(maxUpload.value) : 0;
    if (quotaBytes === null || maxBytes === null) {
      badSize();
      return;
    }
    let chunkBytes = null;
    if (chunkSize.value.trim()) {
      chunkBytes = sizeOrDefault(chunkSize.value);
      if (chunkBytes === null) {
        badSize();
        return;
      }
    }
    const patch = {
      spaceQuota: spaceBytes,
      defaultQuota: quotaBytes,
      defaultRateLimitMbps: Number(defaultRate.value) || 0,
      maxUploadSize: maxBytes,
      trashRetention: trashRetention.value.trim() || '0',
      logRetention: logRetention.value.trim() || '0',
      officePreview: officePreview.value,
      trustProxy: trustProxy.checked,
      accessLogDisabled: accessLogDisabled.checked,
    };
    // 分片大小与两个 TTL 在服务端有下限（必须大于 0），留空就保持原值不提交
    if (chunkBytes !== null) patch.chunkSize = chunkBytes;
    if (uploadTTL.value.trim()) patch.uploadSessionTTL = uploadTTL.value.trim();
    if (sessionTTL.value.trim()) patch.sessionTTL = sessionTTL.value.trim();
    const wantDir = spaceDir.value.trim();
    if (wantDir && wantDir !== (eff.spaceDir || '')) patch.spaceDir = wantDir;

    try {
      const done = await withAdminPassword(async (adminPassword) => api('/api/settings', {
        method: 'PATCH',
        body: patch,
        password: adminPassword,
        passwordField: 'adminPassword',
      }), {
        title: '保存运行期设置',
        message: '设置会立即影响所有账号，请输入管理员密码确认。',
        confirmText: '保存',
      });
      if (done === null) {
        toastInfo('已取消保存');
        return;
      }
      toastSuccess('设置已保存');
      if ((done.restartRequired || []).includes('spaceDir')) {
        toastInfo('存储位置已写入配置文件，重启服务后生效');
      }
      await loadSettings();
    } catch (err) {
      if (err && err.code === 'migration_required') {
        toastError(err.message);
      } else {
        toastApiError(err, '保存设置失败');
      }
    }
  });

  box.append(form, cardSection('当前生效值',
    el('dl', { class: 'ld-kv' },
      el('dt', { text: '配置文件' }), el('dd', { class: 'ld-mono', text: eff.configFile || '—' }),
      el('dt', { text: '监听地址' }), el('dd', { class: 'ld-mono', text: eff.listen || '—' }),
      el('dt', { text: '空间目录' }), el('dd', { class: 'ld-mono', text: eff.spaceDir || '—' }),
      el('dt', { text: '默认空间目录' }), el('dd', { class: 'ld-mono', text: eff.defaultSpaceDir || settingsState.defaultSpaceDir || '—' }),
      el('dt', { text: 'HTTPS' }), el('dd', { text: eff.tlsEnabled ? '已启用' : '未启用' }),
      el('dt', { text: 'Office 预览' }), el('dd', { text: eff.officeAvailable ? '可用' : '不可用' }),
      el('dt', { text: '版本' }), el('dd', { text: eff.version || app.me.version || '—' }))));
}

/** 把容量输入转成字节数；空串或 0/unlimited 都表示不限。 */
function sizeOrDefault(raw) {
  const text = String(raw ?? '').trim();
  if (!text) return 0;
  return parseSizeInput(text);
}

/* ===== 8. 关于与帮助 ==================================================== */

const CLI_HELP = [
  ['homevault serve', '启动服务（默认命令）'],
  ['homevault serve -config /etc/homevault/config.ini', '指定配置文件启动'],
  ['homevault serve -log-level debug', '调整日志级别：debug / info / warn / error'],
  ['homevault migrate-space --to /mnt/big/HomeVault', '迁移空间目录到新位置'],
  ['homevault setpass -user admin -password 新密码', '重置某个账号的密码'],
  ['homevault adduser -user zhangsan -password 密码 -quota 10GiB', '用命令行新建账号'],
  ['homevault version', '查看版本'],
];

function renderAbout() {
  const view = el('section', { class: 'ld-view', 'aria-label': '关于与帮助' });
  const kv = el('dl', { class: 'ld-kv' },
    el('dt', { text: '版本' }), el('dd', { text: app.me.version || '—' }),
    el('dt', { text: '空间目录' }), el('dd', { class: 'ld-mono', text: app.me.spaceDir || '—' }),
    el('dt', { text: '会话有效期' }), el('dd', { text: app.me.sessionTTL || '—' }),
    el('dt', { text: 'Office 预览' }), el('dd', { text: app.me.officeAvailable ? '服务端可用（转 PDF）' : '不可用（使用内置渲染）' }));

  const cliList = el('div', { class: 'ld-list' });
  for (const [command, note] of CLI_HELP) {
    cliList.appendChild(el('div', { class: 'ld-list__item' },
      el('div', {},
        el('div', { class: 'ld-mono', text: command }),
        el('div', { class: 'ld-view__sub', text: note }))));
  }

  view.append(
    viewHead('关于与帮助', '版本、部署升级与命令行用法'),
    el('div', { class: 'ld-view__grid' },
      cardSection('本实例', kv,
        noteRow('info', '所有文件都存在空间目录里，备份时直接复制该目录即可；配置文件只需要 listen / space_dir / tls_cert / tls_key 四项。')),
      cardSection('配置文件与部署',
        el('div', { class: 'ld-view__sub', text: '配置文件只保存启动参数，其余（配额、保留期、限速、分片大小等）都在本页面修改并立即生效。升级步骤：' }),
        el('div', { class: 'ld-list' },
          el('div', { class: 'ld-list__item' }, el('div', { text: '1. 停止正在运行的 homevault 服务' })),
          el('div', { class: 'ld-list__item' }, el('div', { text: '2. 用新版本可执行文件替换旧文件（空间目录不用动）' })),
          el('div', { class: 'ld-list__item' }, el('div', { text: '3. 重新执行 homevault serve，设置与账号会自动沿用' })),
          el('div', { class: 'ld-list__item' }, el('div', { text: '4. 需要换存储位置时先执行 homevault migrate-space --to 目标目录' }))),
      cardSection('命令行用法', cliList,
        noteRow('warn', '命令行操作会直接读写空间目录，请在服务停止时执行。')))),
  );
  app.shell.main.appendChild(view);
}

/* ===== 启动 ============================================================= */

boot();
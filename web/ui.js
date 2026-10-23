/* ===========================================================================
 * homevault ui.js —— 共享库
 * ---------------------------------------------------------------------------
 * 本文件是用户端(app.js)与管理端(admin.js)共用的唯一基础库，包含四类东西：
 *
 *   §1 工具与格式化        纯函数：DOM 构建、字节/时间/速率格式化、词典
 *   §2 主题与偏好          theme（localStorage 记忆 + 跟随系统）
 *   §3 轻量状态 store      createStore / session
 *   §4 API 封装            api() / ApiError / 二次密码 withAdminPassword()
 *   §5 自定义元素组件      ld-icon ld-button ld-modal ld-toast ld-confirm …
 *   §6 共享服务            UploadManager（分片续传）、FileBrowser（文件管理器）、mountShell
 *
 * 设计约定
 *   1. 组件一律使用**轻量 DOM**（不建 Shadow DOM），样式集中在 style.css，
 *      这样设计令牌只有一份，页面可以自由组合组件。
 *   2. 组件**不发请求**：数据由页面通过属性/属性器传入，交互通过自定义事件抛出。
 *      唯一的两个例外是共享服务 UploadManager / FileBrowser —— 它们是"页面级
 *      服务"而非展示组件，页面创建它们时就显式传入了目标账号与取密码的回调。
 *   3. 禁止把用户数据拼进 innerHTML：全部使用 createElement / textContent。
 *      本文件中出现的 innerHTML 均为完全静态的模板字符串。
 *   4. 图标统一走 /icons.svg 的 <symbol>，通过 <use href="/icons.svg#ic-xxx"> 复用。
 * =========================================================================== */

/* ===== §1 工具与格式化 ==================================================== */

/** SVG 命名空间标识符（W3C 命名空间 URI，不是网络请求地址）。 */
const SVG_NS = 'http://www.w3.org/2000/svg';

/** 当前断点：移动端布局阈值，与 style.css 的 860px 保持一致。 */
export const narrowQuery = window.matchMedia('(max-width: 859px)');

export function isNarrow() {
  return narrowQuery.matches;
}

export function onNarrowChange(fn) {
  const handler = (e) => fn(e.matches);
  narrowQuery.addEventListener('change', handler);
  return () => narrowQuery.removeEventListener('change', handler);
}

/** 把子节点（字符串 / 节点 / 数组 / null）挂到父节点。 */
export function appendChildren(parent, children) {
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) appendChildren(parent, child);
    else if (child instanceof Node) parent.appendChild(child);
    else parent.appendChild(document.createTextNode(String(child)));
  }
  return parent;
}

/**
 * 取代原生的 node.append(...)：跳过 null / undefined / false 与数组展开。
 * 原生的 append() 会把 null 当成字符串渲染出 "null"，页面上就直接漏出这五个字母。
 * 需要往已有元素里塞“可能是空的”子节点时，一律用这个。
 */
export function append(parent, ...children) {
  appendChildren(parent, children);
  return parent;
}

/**
 * el('div', { class: 'x', text: '内容', onclick: fn }, child1, child2…)
 * - text      → textContent（安全，不解析 HTML）
 * - dataset   → 对象
 * - style     → 对象
 * - on<event> → addEventListener
 * - true      → 空属性；false/null/undefined → 跳过
 */
export function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [key, value] of Object.entries(attrs)) {
      if (value === null || value === undefined || value === false) continue;
      if (key === 'class') node.className = String(value);
      else if (key === 'text') node.textContent = String(value);
      else if (key === 'dataset') Object.assign(node.dataset, value);
      else if (key === 'style' && typeof value === 'object') Object.assign(node.style, value);
      else if (key.startsWith('on') && typeof value === 'function') {
        node.addEventListener(key.slice(2).toLowerCase(), value);
      } else if (value === true) node.setAttribute(key, '');
      else node.setAttribute(key, String(value));
    }
  }
  return appendChildren(node, children);
}

/** 清空一个节点的所有子节点。 */
export function clearNode(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
  return node;
}

/** 创建一个图标节点：<svg><use href="/icons.svg#ic-name"></use></svg>。 */
export function iconNode(name, className) {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.7');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  if (className) svg.setAttribute('class', className);
  const use = document.createElementNS(SVG_NS, 'use');
  // 同文档引用：index.html / admin.html 内联了 sprite，
  // 避免外部 <use href="/icons.svg#…"> 因缓存/MIME/CSP 失效时整片图标空白。
  use.setAttribute('href', `#ic-${name}`);
  svg.appendChild(use);
  return svg;
}

/** 时钟/滑块等局部动画的取消。 */
export function debounce(fn, wait = 250) {
  let timer = 0;
  return function debounced(...args) {
    window.clearTimeout(timer);
    timer = window.setTimeout(() => fn.apply(this, args), wait);
  };
}

export function b64(str) {
  return btoa(unescape(encodeURIComponent(String(str))));
}

/** 把毫秒数变成 1.5s / 2m 这样的短标签。 */
export function fmtDurationSec(seconds) {
  const s = Math.max(0, Math.round(Number(seconds) || 0));
  if (s < 60) return `${s} 秒`;
  if (s < 3600) return `${Math.floor(s / 60)} 分 ${s % 60} 秒`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时 ${Math.floor((s % 3600) / 60)} 分`;
  return `${Math.floor(s / 86400)} 天 ${Math.floor((s % 86400) / 3600)} 小时`;
}

const SIZE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

export function fmtBytes(value) {
  let n = Number(value) || 0;
  const sign = n < 0 ? '-' : '';
  n = Math.abs(n);
  if (n < 1024) return `${sign}${n} B`;
  let i = 0;
  let v = n;
  while (v >= 1024 && i < SIZE_UNITS.length - 1) {
    v /= 1024;
    i += 1;
  }
  const digits = v >= 100 ? 0 : v >= 10 ? 1 : 2;
  return `${sign}${v.toFixed(digits)} ${SIZE_UNITS[i]}`;
}

/** 配额：0 表示不限。 */
export function fmtQuota(value) {
  const n = Number(value) || 0;
  return n <= 0 ? '不限' : fmtBytes(n);
}

function pad2(n) {
  return String(n).padStart(2, '0');
}

export function toDate(value) {
  if (!value) return null;
  const d = value instanceof Date ? value : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

export function fmtDate(value) {
  const d = toDate(value);
  if (!d) return '—';
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** 精确到秒的时间（审计日志用；排障时"哪一秒"经常是关键）。 */
export function fmtDateTime(value) {
  const d = toDate(value);
  if (!d) return '—';
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} `
    + `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

export function fmtDay(value) {
  const d = toDate(value);
  if (!d) return '—';
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

/** 日期 + 1 天，用于把 type=date 的“结束日期”变成包含当天的区间终点。 */
export function nextDay(value) {
  const text = String(value ?? '').slice(0, 10);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(text)) return '';
  const date = new Date(`${text}T00:00:00`);
  if (Number.isNaN(date.getTime())) return '';
  date.setDate(date.getDate() + 1);
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
}

export function fmtRelative(value) {
  const d = toDate(value);
  if (!d) return '—';
  const diff = Date.now() - d.getTime();
  if (diff < 0) return fmtDate(d);
  const sec = Math.floor(diff / 1000);
  if (sec < 60) return '刚刚';
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`;
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)} 天前`;
  return fmtDate(d);
}

export function fmtSpeed(bytesPerSec) {
  const v = Number(bytesPerSec) || 0;
  if (v <= 0) return '—';
  return `${fmtBytes(v)}/s`;
}

export function fmtEta(seconds) {
  const s = Number(seconds);
  if (!Number.isFinite(s) || s <= 0) return '—';
  return fmtDurationSec(s);
}

export function fmtUptime(seconds) {
  return fmtDurationSec(seconds);
}

/** 目录体积占位。 */
export function fmtSizeOrDash(size) {
  return size > 0 ? fmtBytes(size) : '—';
}

export const KIND_LABELS = {
  folder: '文件夹',
  image: '图片',
  video: '视频',
  audio: '音频',
  pdf: 'PDF',
  text: '文本',
  office: 'Office 文档',
  archive: '压缩包',
  other: '其他',
};

export function kindLabel(kind) {
  return KIND_LABELS[kind] || KIND_LABELS.other;
}

export const KIND_ICONS = {
  folder: 'folder',
  image: 'image',
  video: 'video',
  audio: 'audio',
  pdf: 'pdf',
  text: 'text',
  office: 'office',
  archive: 'archive',
  other: 'file',
};

export const ACTION_LABELS = {
  login: '登录',
  login_failed: '登录失败',
  logout: '登出',
  upload: '上传',
  download: '下载',
  mkdir: '新建文件夹',
  rename: '重命名',
  move: '移动',
  copy: '复制',
  delete: '删除（入回收站）',
  restore: '恢复',
  purge: '永久删除',
  trash_empty: '清空回收站',
  user_create: '新建用户',
  user_update: '修改用户',
  user_delete: '删除用户',
  config_reload: '修改设置',
  settings_set: '修改设置',
  cleanup: '后台清理',
  admin_access: '管理员代操作',
  reauth: '二次密码校验',
  search: '搜索',
  preview: '预览',
  text_save: '编辑文本',
  logs_purge: '清除日志',
  quota_denied: '配额拒绝',
};

export function actionLabel(action) {
  return ACTION_LABELS[action] || action || '—';
}

export const ACTION_KEYS = Object.keys(ACTION_LABELS);

export const STATUS_LABELS = { ok: '成功', denied: '已拒绝', error: '错误' };

export function statusLabel(status) {
  return STATUS_LABELS[status] || status || '—';
}

export function statusBadgeClass(status) {
  if (status === 'ok') return 'ld-badge ld-badge--ok';
  if (status === 'denied') return 'ld-badge ld-badge--denied';
  if (status === 'error') return 'ld-badge ld-badge--error';
  return 'ld-badge ld-badge--off';
}

export const PERMISSION_LABELS = {
  read: '读取',
  upload: '上传',
  modify: '重命名 / 移动 / 复制',
  delete: '删除',
  trash_view: '查看回收站',
  trash_manage: '管理回收站',
  users_manage: '用户管理',
  logs_view: '查看日志',
};

export function permissionLabel(name) {
  return PERMISSION_LABELS[name] || name;
}

/** 排序：目录永远在前，其余按字段比较。 */
export function sortEntries(entries, key = 'name', dir = 'asc') {
  const sign = dir === 'desc' ? -1 : 1;
  const list = entries.slice();
  list.sort((a, b) => {
    if (!!a.isDir !== !!b.isDir) return a.isDir ? -1 : 1;
    let result = 0;
    if (key === 'size') result = (a.size || 0) - (b.size || 0);
    else if (key === 'modTime' || key === 'modtime') {
      result = (toDate(a.modTime)?.getTime() || 0) - (toDate(b.modTime)?.getTime() || 0);
    } else if (key === 'kind') result = kindLabel(a.kind).localeCompare(kindLabel(b.kind), 'zh');
    else result = String(a.name || '').localeCompare(String(b.name || ''), 'zh', { numeric: true });
    if (result === 0) result = String(a.name || '').localeCompare(String(b.name || ''), 'zh', { numeric: true });
    return result * sign;
  });
  return list;
}

/** 解析 "10GiB" / "0" / "unlimited" → 字节数；失败返回 null。 */
export function parseSizeInput(input) {
  const raw = String(input ?? '').trim();
  if (!raw) return null;
  if (/^unlimited$/i.test(raw)) return 0;
  const m = /^([0-9]*\.?[0-9]+)\s*([a-zA-Z]*)$/.exec(raw);
  if (!m) return null;
  const num = Number(m[1]);
  if (!Number.isFinite(num) || num < 0) return null;
  const unit = m[2].toLowerCase();
  const table = {
    '': 1, b: 1, kib: 1024, mib: 1024 ** 2, gib: 1024 ** 3, tib: 1024 ** 4,
    kb: 1024, mb: 1024 ** 2, gb: 1024 ** 3, tb: 1024 ** 4,
  };
  const mul = table[unit];
  if (!mul) return null;
  return Math.round(num * mul);
}

/** 把路径切成面包屑片段：[{label, path}]。 */
export function pathCrumbs(path, rootLabel = '根目录') {
  const clean = String(path || '').replace(/^\/+|\/+$/g, '');
  const items = [{ label: rootLabel, path: '' }];
  if (!clean) return items;
  const parts = clean.split('/');
  let acc = '';
  for (const part of parts) {
    acc = acc ? `${acc}/${part}` : part;
    items.push({ label: part, path: acc });
  }
  return items;
}

export function parentPath(path) {
  const clean = String(path || '').replace(/^\/+|\/+$/g, '');
  const idx = clean.lastIndexOf('/');
  return idx < 0 ? '' : clean.slice(0, idx);
}

export function joinPath(dir, name) {
  const clean = String(dir || '').replace(/^\/+|\/+$/g, '');
  return clean ? `${clean}/${name}` : String(name);
}

export function baseName(path) {
  const clean = String(path || '').replace(/\/+$/g, '');
  const idx = clean.lastIndexOf('/');
  return idx < 0 ? clean : clean.slice(idx + 1);
}

export function sleep(ms) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

/* ===== §2 主题与偏好 ====================================================== */

const THEME_KEY = 'ld-theme';

export const theme = {
  listeners: new Set(),

  get() {
    try {
      return localStorage.getItem(THEME_KEY) || 'system';
    } catch {
      return 'system';
    }
  },

  resolved(mode = theme.get()) {
    if (mode === 'system') return narrowDark() ? 'dark' : 'light';
    return mode;
  },

  apply() {
    const mode = theme.get();
    const value = theme.resolved(mode);
    document.documentElement.dataset.theme = value;
    document.documentElement.dataset.themeMode = mode;
    return value;
  },

  set(mode) {
    try {
      localStorage.setItem(THEME_KEY, mode);
    } catch {
      /* 隐私模式下忽略 */
    }
    theme.apply();
    theme.emit();
  },

  toggle() {
    theme.set(theme.resolved() === 'dark' ? 'light' : 'dark');
  },

  onChange(fn) {
    theme.listeners.add(fn);
    return () => theme.listeners.delete(fn);
  },

  emit() {
    for (const fn of theme.listeners) fn(theme.resolved());
  },

  init() {
    theme.apply();
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const handler = () => {
      if (theme.get() === 'system') {
        theme.apply();
        theme.emit();
      }
    };
    if (mq.addEventListener) mq.addEventListener('change', handler);
    else if (mq.addListener) mq.addListener(handler);
  },
};

function narrowDark() {
  return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

/* ===== §3 轻量状态 store ================================================== */

export function createStore(initial = {}) {
  let state = { ...initial };
  const listeners = new Set();
  return {
    get() {
      return state;
    },
    set(patch) {
      const next = typeof patch === 'function' ? patch(state) : patch;
      state = { ...state, ...next };
      for (const fn of listeners) fn(state);
      return state;
    },
    subscribe(fn) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
  };
}

/** 当前登录会话（/api/auth/me 的结果）。 */
export const session = createStore({
  ready: false,
  user: null,
  home: '/',
  version: '',
  spaceDir: '',
  sessionTTL: '',
  officeAvailable: false,
  permissionLabels: [],
});

/* ===== §4 API 封装 ======================================================== */

export class ApiError extends Error {
  constructor(message, status, code, payload) {
    super(message || '请求失败');
    this.name = 'ApiError';
    this.status = Number(status) || 0;
    this.code = code || `http_${status}`;
    this.payload = payload || null;
  }
}

/** 由页面注册的全局回调（401 / 必须改凭据 / 账号被禁用）。 */
export const authEvents = {
  onUnauthorized: null,
  onMustChange: null,
  onDisabled: null,
};

export function withQuery(path, params) {
  const url = new URL(path, window.location.origin);
  for (const [key, value] of Object.entries(params || {})) {
    if (value === null || value === undefined || value === '') continue;
    url.searchParams.set(key, String(value));
  }
  return url.pathname + (url.search ? `?${url.searchParams.toString()}` : '');
}

/** 拼接文件下载/内联预览地址。 */
export function fileUrl(path, { user, inline, download } = {}) {
  return withQuery('/api/fs/download', {
    path,
    user,
    inline: inline ? 1 : undefined,
    download: download ? 1 : undefined,
  });
}

function statusMessage(status) {
  const table = {
    400: '请求参数有误',
    401: '未登录或会话已过期',
    403: '没有执行该操作的权限',
    404: '资源不存在',
    409: '操作冲突',
    413: '文件超过单文件上限',
    428: '该操作需要再次输入管理员密码',
    429: '操作过于频繁，请稍后再试',
    500: '服务端内部错误',
    507: '空间不足，操作被拒绝',
  };
  return table[status] || `请求失败（HTTP ${status}）`;
}

async function buildError(response, preset) {
  let payload = preset;
  if (payload === undefined) {
    try {
      payload = await response.json();
    } catch {
      payload = null;
    }
  }
  const code = payload && payload.code ? payload.code : `http_${response.status}`;
  const message = (payload && payload.error) || statusMessage(response.status);
  return new ApiError(message, response.status, code, payload);
}

function notifyAuth(err) {
  if (err.status === 401 && authEvents.onUnauthorized) authEvents.onUnauthorized(err);
  else if (err.code === 'must_change_credentials' && authEvents.onMustChange) authEvents.onMustChange(err);
  else if (err.code === 'disabled' && authEvents.onDisabled) authEvents.onDisabled(err);
}

/**
 * 统一请求封装。
 * @param {string} path
 * @param {object} [opts]
 * @param {string} [opts.method]
 * @param {object|FormData|Blob|string} [opts.body]
 * @param {object} [opts.headers]
 * @param {string} [opts.password] 管理员二次密码：同时进 body 字段与 X-LD-Password-B64 头
 * @param {string} [opts.passwordField] 密码字段名，默认 password（用户/设置接口用 adminPassword）
 * @param {string} [opts.user] 管理员代操作目标账号 → 追加 ?user=
 * @param {boolean} [opts.raw] 返回原始 Response
 * @param {AbortSignal} [opts.signal]
 */
export async function api(path, opts = {}) {
  const {
    method = 'GET',
    body,
    headers = {},
    password,
    passwordField = 'password',
    user,
    raw = false,
    signal,
  } = opts;

  const url = user ? withQuery(path, { user }) : path;
  const init = { method, headers: { ...headers }, credentials: 'same-origin' };
  if (signal) init.signal = signal;

  let payload = body;
  if (password) {
    init.headers['X-LD-Password-B64'] = b64(password);
    const plain = payload && typeof payload === 'object'
      && !(payload instanceof FormData) && !(payload instanceof Blob)
      && !(payload instanceof ArrayBuffer) && !(payload instanceof URLSearchParams);
    if (plain) payload = { ...payload, [passwordField]: password };
  }

  const isRawBody = typeof payload === 'string' || payload instanceof FormData
    || payload instanceof Blob || payload instanceof ArrayBuffer || payload instanceof URLSearchParams;

  if (isRawBody) {
    init.body = payload;
  } else if (payload !== undefined && payload !== null) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(payload);
  }

  let response;
  try {
    response = await fetch(url, init);
  } catch (err) {
    if (err && err.name === 'AbortError') throw err;
    throw new ApiError('网络请求失败，请检查连接', 0, 'network_error', null);
  }

  if (raw) {
    if (!response.ok) throw await buildError(response);
    return response;
  }
  if (response.status === 204) return null;

  const text = await response.text();
  let data = null;
  if (text) {
    const type = response.headers.get('Content-Type') || '';
    if (type.includes('json')) {
      try {
        data = JSON.parse(text);
      } catch {
        data = null;
      }
    } else {
      data = text;
    }
  }
  if (!response.ok) {
    const err = await buildError(response, data);
    notifyAuth(err);
    throw err;
  }
  return data;
}

/**
 * 管理员二次密码：每次调用都会重新询问（不做任何时间窗缓存）。
 * run(password) 抛出的 428 / reauth_failed 会回到输入框重试。
 * 返回 null 表示用户主动取消。
 */
export async function withAdminPassword(run, opts = {}) {
  let errorText = opts.error || '';
  for (;;) {
    const password = await promptPassword({
      title: opts.title || '管理员二次验证',
      message: opts.message || '该操作会修改数据，请输入当前管理员密码确认。',
      error: errorText,
      confirmText: opts.confirmText || '确认执行',
    });
    if (password === null) return null;
    try {
      return await run(password);
    } catch (err) {
      if (err && (err.code === 'reauth_failed' || err.code === 'reauth_required' || err.code === 'invalid_password')) {
        errorText = err.message || '管理员密码不正确';
        continue;
      }
      throw err;
    }
  }
}

/** 只读请求的便捷封装（自动带 user 参数）。 */
export function apiGet(path, { user, params } = {}) {
  const full = params ? withQuery(path, params) : path;
  return api(full, { user });
}

/* ===== §5 自定义元素 ====================================================== */

/**
 * 所有 ld-* 组件的基类（轻量 DOM）。
 * 子类实现 render()，用 update() 请求下一帧重绘（合并同一帧内的多次修改）。
 */
export class LdElement extends HTMLElement {
  static defaults = {};
  static observedAttributes = [];

  constructor() {
    super();
    this.props = { ...(this.constructor.defaults || {}) };
    this._mounted = false;
    this._frame = 0;
    this._frameFn = () => {
      this._frame = 0;
      this.render();
    };
  }

  connectedCallback() {
    if (!this._mounted) {
      this._mounted = true;
      this._syncAttrsToProps();
      this.render();
    }
    if (this.onMount) this.onMount();
  }

  disconnectedCallback() {
    if (this.onUnmount) this.onUnmount();
  }

  attributeChangedCallback(name, oldValue, newValue) {
    if (oldValue === newValue) return;
    if (this.onAttribute) this.onAttribute(name, newValue);
    else this.update();
  }

  setProp(key, value) {
    if (this.props[key] === value) return;
    this.props[key] = value;
    this.update();
  }

  update() {
    if (!this._mounted || this._frame) return;
    this._frame = requestAnimationFrame(this._frameFn);
  }

  renderNow() {
    if (this._frame) {
      cancelAnimationFrame(this._frame);
      this._frame = 0;
    }
    if (this._mounted) this.render();
    return this;
  }

  render() {}

  emit(type, detail) {
    this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true }));
    return this;
  }

  /** 属性优先、属性器兜底地取布尔值。 */
  flag(name, propName) {
    return this.hasAttribute(name) || this.props[propName || name] === true;
  }

  /** 属性优先、属性器兜底地取字符串。
   *  注意：必须先看 HTML 属性。defaults 里的非空默认值（如 LdButton 的 type='button'）
   *  如果先被读到，会把调用方显式写的 type="submit" 静默吞掉。 */
  val(attr, propName) {
    const fromAttr = this.getAttribute(attr);
    if (fromAttr !== null && fromAttr !== '') return fromAttr;
    const fromProp = this.props[propName || attr];
    if (fromProp !== undefined && fromProp !== null && fromProp !== '') return fromProp;
    return '';
  }

  /**
   * 把 HTML attribute 同步成 prop（挂载前执行一次）。
   * HTML 里的属性名不区分大小写，因此三种写法都要试：
   * rowKey / rowkey / row-key。布尔型与数字型 default 决定解析方式。
   */
  _syncAttrsToProps() {
    const names = this.constructor.propsNames;
    if (!names || !names.length) return;
    const defaults = this.constructor.defaults || {};
    for (const name of names) {
      const candidates = [name, name.toLowerCase(), kebabName(name)];
      let attrName = '';
      for (const candidate of candidates) {
        if (this.hasAttribute(candidate)) {
          attrName = candidate;
          break;
        }
      }
      if (!attrName) continue;
      const raw = this.getAttribute(attrName);
      const fallback = defaults[name];
      if (Array.isArray(fallback) || (fallback !== null && typeof fallback === 'object')) continue;
      if (typeof fallback === 'boolean') this.props[name] = raw !== 'false';
      else if (typeof fallback === 'number') {
        const num = Number(raw);
        this.props[name] = Number.isFinite(num) ? num : fallback;
      } else {
        this.props[name] = raw;
      }
    }
  }
}

/** 为组件批量定义属性器（写属性 → 触发重绘）。 */
export function defineProps(ctor, names) {
  // 记录属性名：元素插入文档时会把同名 attribute 同步成 prop，
  // 这样 el('ld-quota-bar', { used: 10 }) 这种写法也能生效。
  ctor.propsNames = names.slice();
  for (const name of names) {
    Object.defineProperty(ctor.prototype, name, {
      get() {
        return this.props[name];
      },
      set(value) {
        this.setProp(name, value);
      },
      configurable: true,
      enumerable: true,
    });
  }
}

/** camelCase → kebab-case（rowKey → row-key）。 */
function kebabName(name) {
  return String(name).replace(/[A-Z]/g, (ch) => `-${ch.toLowerCase()}`);
}

const ICON_SIZES = { xs: '12px', sm: '14px', md: '16px', lg: '20px', xl: '24px', xxl: '32px' };

/** ld-icon：复用 icons.svg 里的 symbol。 */
export class LdIcon extends LdElement {
  static observedAttributes = ['name', 'size', 'label'];

  render() {
    clearNode(this);
    const name = this.getAttribute('name') || 'file';
    const size = this.getAttribute('size') || '';
    const label = this.getAttribute('label') || '';
    if (size) this.style.fontSize = ICON_SIZES[size] || (/^[0-9.]+$/.test(size) ? `${size}px` : size);
    this.appendChild(iconNode(name));
    if (label) {
      this.setAttribute('role', 'img');
      this.setAttribute('aria-label', label);
    } else {
      this.setAttribute('aria-hidden', 'true');
    }
  }
}

/** ld-spinner：加载指示器。 */
export class LdSpinner extends LdElement {
  static observedAttributes = ['size'];

  render() {
    clearNode(this);
    const size = this.getAttribute('size');
    if (size) this.dataset.size = size;
    this.appendChild(el('span', { class: 'ld-spin' }));
    this.setAttribute('role', 'status');
    if (!this.hasAttribute('aria-label')) this.setAttribute('aria-label', '加载中');
  }
}

/** ld-button：统一按钮。label/icon 走属性，点击由内部原生 <button> 冒泡。 */
export class LdButton extends LdElement {
  static defaults = {
    label: '', icon: '', variant: 'default', size: 'md',
    loading: false, disabled: false, block: false, iconOnly: false,
    type: 'button', title: '', ariaLabel: '', active: false,
  };

  static observedAttributes = [
    'label', 'icon', 'variant', 'size', 'disabled', 'loading', 'block',
    'icon-only', 'type', 'title', 'aria-label', 'active',
  ];

  render() {
    const label = this.val('label');
    const icon = this.val('icon');
    const variant = this.val('variant') || 'default';
    const size = this.val('size') || 'md';
    const loading = this.flag('loading');
    const disabled = this.flag('disabled') || loading;
    const block = this.flag('block');
    const iconOnly = this.flag('icon-only', 'iconOnly') || (!label && !!icon);
    const type = this.val('type') || 'button';
    const title = this.val('title') || label;
    const ariaLabel = this.val('aria-label', 'ariaLabel') || label || title;

    const classes = ['ld-btn'];
    if (variant && variant !== 'default') classes.push(`ld-btn--${variant}`);
    if (size && size !== 'md') classes.push(`ld-btn--${size}`);
    if (block) classes.push('ld-btn--block');
    if (iconOnly) classes.push('ld-btn--icon');
    if (this.props.active) classes.push('is-active');

    const btn = el('button', {
      class: classes.join(' '),
      type,
      title: title || undefined,
      'aria-label': ariaLabel || undefined,
      'aria-busy': loading ? 'true' : undefined,
      disabled: disabled ? true : undefined,
    });
    if (loading) btn.appendChild(el('ld-spinner'));
    else if (icon) btn.appendChild(el('ld-icon', { name: icon, size: 'md' }));
    if (label && !iconOnly) btn.appendChild(el('span', { class: 'ld-btn__label', text: label }));

    clearNode(this);
    this.appendChild(btn);
  }

  /** 只切换忙碌态，避免整块重建造成焦点丢失。 */
  setBusy(on) {
    const btn = this.querySelector('button');
    if (!btn) return;
    if (on) {
      this.setAttribute('loading', '');
      btn.disabled = true;
      btn.setAttribute('aria-busy', 'true');
    } else {
      this.removeAttribute('loading');
      btn.disabled = this.flag('disabled');
      btn.removeAttribute('aria-busy');
    }
  }
}

/** ld-field：表单行 = 标签 + 控件 + 提示 + 错误。 */
export class LdField extends LdElement {
  static observedAttributes = ['label', 'hint', 'required', 'inline', 'error'];

  render() {
    if (!this._built) {
      this._built = true;
      this._controls = Array.from(this.childNodes);
      this._label = el('span', { class: 'ld-field__label' });
      this._body = el('div', { class: 'ld-field__body' });
      this._hint = el('span', { class: 'ld-field__hint' });
      this._err = el('span', { class: 'ld-field__err', role: 'alert' });
      this._labelWrap = el('div', {}, this._label);
      const root = el('div', { class: 'ld-field' }, this._labelWrap, this._body, this._hint, this._err);
      this._root = root;
      clearNode(this);
      this.appendChild(root);
      for (const node of this._controls) this._body.appendChild(node);
    }
    const label = this.getAttribute('label') || '';
    const hint = this.getAttribute('hint') || '';
    const required = this.hasAttribute('required');
    const inline = this.hasAttribute('inline');
    const error = this.getAttribute('error') || '';

    this._label.textContent = label;
    if (required) this._label.appendChild(el('span', { class: 'ld-field__req', text: ' *' }));
    this._labelWrap.classList.toggle('ld-hidden', !label);
    this._hint.textContent = hint;
    this._hint.classList.toggle('ld-hidden', !hint);
    this._err.textContent = error;
    this._root.classList.toggle('ld-field--inline', inline);
    this.dataset.invalid = error ? 'true' : 'false';
  }

  setError(message) {
    if (message) this.setAttribute('error', message);
    else this.removeAttribute('error');
    return this;
  }

  clearError() {
    this.removeAttribute('error');
    return this;
  }
}

/** ld-tabs：页签。 */
export class LdTabs extends LdElement {
  static defaults = { tabs: [], active: '' };
  static observedAttributes = ['active'];

  render() {
    const tabs = this.props.tabs || [];
    const active = this.getAttribute('active') || this.props.active;
    const bar = el('div', { class: 'ld-tabs', role: 'tablist' });
    for (const tab of tabs) {
      const id = tab.id || tab.label;
      const btn = el('button', {
        class: 'ld-tabs__btn',
        type: 'button',
        role: 'tab',
        'aria-selected': String(id === active),
        'data-id': id,
        onclick: () => {
          this.props.active = id;
          this.emit('ld-tab', { id });
        },
      });
      if (tab.icon) btn.appendChild(el('ld-icon', { name: tab.icon }));
      btn.appendChild(el('span', { text: tab.label || id }));
      if (tab.badge !== undefined && tab.badge !== null) {
        btn.appendChild(el('span', { class: 'ld-badge', text: String(tab.badge) }));
      }
      bar.appendChild(btn);
    }
    clearNode(this);
    this.appendChild(bar);
  }
}

defineProps(LdTabs, ['tabs', 'active']);

/** ld-empty：空状态引导。 */
export class LdEmpty extends LdElement {
  static observedAttributes = ['icon', 'title', 'description', 'compact'];

  render() {
    if (!this._built) {
      this._built = true;
      this._actions = el('div', { class: 'ld-empty__actions' });
      const keep = Array.from(this.childNodes);
      this._iconBox = el('div', { class: 'ld-empty__icon' });
      this._title = el('div', { class: 'ld-empty__title' });
      this._desc = el('div', { class: 'ld-empty__desc' });
      this._root = el('div', { class: 'ld-empty' }, this._iconBox, this._title, this._desc, this._actions);
      clearNode(this);
      this.appendChild(this._root);
      for (const node of keep) this._actions.appendChild(node);
    }
    const icon = this.getAttribute('icon') || 'info';
    const title = this.getAttribute('title') || '暂无内容';
    const desc = this.getAttribute('description') || '';
    clearNode(this._iconBox);
    this._iconBox.appendChild(el('ld-icon', { name: icon, size: 'xl' }));
    this._title.textContent = title;
    this._desc.textContent = desc;
    this._desc.classList.toggle('ld-hidden', !desc);
    this._actions.classList.toggle('ld-hidden', !this._actions.childElementCount);
    if (this.hasAttribute('compact')) this._root.style.padding = 'var(--ld-sp-5) var(--ld-sp-4)';
  }
}

/** ld-progress：进度条。 */
export class LdProgress extends LdElement {
  static defaults = { value: 0, max: 100, indeterminate: false, state: '', size: '' };
  static observedAttributes = ['value', 'max', 'indeterminate', 'state', 'size'];

  render() {
    const max = Number(this.getAttribute('max') ?? this.props.max) || 100;
    const value = Number(this.getAttribute('value') ?? this.props.value) || 0;
    const indeterminate = this.flag('indeterminate');
    const state = this.getAttribute('state') || this.props.state || '';
    const size = this.getAttribute('size') || this.props.size || '';
    const pct = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0;

    const classes = ['ld-progress'];
    if (indeterminate) classes.push('ld-progress--indeterminate');
    if (state === 'error') classes.push('ld-progress--error');
    if (state === 'success') classes.push('ld-progress--success');
    if (size === 'lg') classes.push('ld-progress--lg');

    const bar = el('div', {
      class: classes.join(' '),
      role: 'progressbar',
      'aria-valuemin': '0',
      'aria-valuemax': String(max),
      'aria-valuenow': indeterminate ? undefined : String(Math.round(value)),
    }, el('div', { class: 'ld-progress__fill', style: { width: `${pct}%` } }));

    clearNode(this);
    this.appendChild(bar);
  }
}

defineProps(LdProgress, ['value', 'max', 'indeterminate', 'state', 'size']);

/** ld-quota-bar：已用 / 回收站 / 配额。 */
export class LdQuotaBar extends LdElement {
  static defaults = { used: 0, trashUsed: 0, quota: 0, legend: true };
  static observedAttributes = ['used', 'trash-used', 'quota', 'legend'];

  render() {
    const used = Number(this.getAttribute('used') ?? this.props.used) || 0;
    const trash = Number(this.getAttribute('trash-used') ?? this.props.trashUsed) || 0;
    const quota = Number(this.getAttribute('quota') ?? this.props.quota) || 0;
    const total = used + trash;
    const unlimited = quota <= 0;
    const pct = unlimited ? 0 : Math.min(100, (total / quota) * 100);
    const usedPct = unlimited ? 0 : Math.min(100, (used / quota) * 100);
    const warn = !unlimited && pct >= 90;

    const bar = el('div', { class: 'ld-quota__bar' },
      el('div', { class: 'ld-quota__seg ld-quota__seg--used', style: { width: `${usedPct}%` } }),
      el('div', { class: 'ld-quota__seg ld-quota__seg--trash', style: { width: `${Math.max(0, pct - usedPct)}%` } }));

    const legend = el('div', { class: 'ld-quota__legend' },
      el('span', {}, el('i', { class: 'ld-quota__dot', style: { background: 'var(--ld-accent)' } }),
        el('span', { class: 'ld-quota__num', text: `已用 ${fmtBytes(used)}` })),
      trash > 0
        ? el('span', {}, el('i', { class: 'ld-quota__dot', style: { background: 'var(--ld-warn)' } }),
          el('span', { class: 'ld-quota__num', text: `回收站 ${fmtBytes(trash)}` }))
        : null,
      el('span', { class: 'ld-quota__num', text: unlimited ? '不限容量' : `共 ${fmtQuota(quota)}` }));

    const box = el('div', { class: `ld-quota${warn ? ' ld-quota--warn' : ''}` }, bar);
    if (this.getAttribute('legend') !== 'false' && this.props.legend !== false) box.appendChild(legend);
    clearNode(this);
    this.appendChild(box);
  }
}

defineProps(LdQuotaBar, ['used', 'trashUsed', 'quota', 'legend']);

/** ld-file-icon：按类型给出图标 + 配色。 */
export class LdFileIcon extends LdElement {
  static defaults = { kind: 'other', size: 'md' };
  static observedAttributes = ['kind', 'size'];

  render() {
    const kind = this.getAttribute('kind') || this.props.kind || 'other';
    const size = this.getAttribute('size') || this.props.size || 'md';
    this.dataset.kind = kind;
    this.dataset.size = size;
    const icon = KIND_ICONS[kind] || 'file';
    clearNode(this);
    this.appendChild(el('span', { class: 'ld-ficon', 'aria-hidden': 'true' },
      el('ld-icon', { name: icon, size: size === 'sm' ? 'sm' : size === 'lg' ? 'lg' : 'md' })));
  }
}

defineProps(LdFileIcon, ['kind', 'size']);

/** ld-breadcrumb：路径面包屑。 */
export class LdBreadcrumb extends LdElement {
  static defaults = { items: [], rootLabel: '根目录' };
  static observedAttributes = ['root-label'];

  render() {
    const items = this.props.items && this.props.items.length
      ? this.props.items
      : pathCrumbs('', this.val('root-label', 'rootLabel') || '根目录');
    const box = el('nav', { class: 'ld-crumbs', 'aria-label': '当前路径' });
    items.forEach((item, index) => {
      if (index > 0) box.appendChild(el('span', { class: 'ld-crumb__sep' }, el('ld-icon', { name: 'chevron-right', size: 'sm' })));
      const isLast = index === items.length - 1;
      const btn = el('button', {
        class: 'ld-crumb',
        type: 'button',
        'aria-current': isLast ? 'true' : undefined,
        title: item.path === '' ? '回到根目录' : item.path,
        onclick: () => {
          if (!isLast) this.emit('ld-navigate', { path: item.path });
        },
      });
      if (index === 0) btn.appendChild(el('ld-icon', { name: 'home', size: 'sm' }));
      btn.appendChild(el('span', { text: item.label }));
      box.appendChild(btn);
    });
    clearNode(this);
    this.appendChild(box);
  }
}

defineProps(LdBreadcrumb, ['items', 'rootLabel']);

/* ---------- 弹窗基类：遮罩 + 焦点陷阱 + Esc 关闭 ---------- */

let uidSeq = 0;
function uid(prefix) {
  uidSeq += 1;
  return `${prefix}-${uidSeq}`;
}

export class OverlayDialog extends LdElement {
  static defaults = { title: '', size: 'md', dismissible: true, hideClose: false };

  /**
   * 遮罩是挂到 document.body 上的，直接 remove() 只会摘掉宿主元素、
   * 把遮罩留在屏幕上。这里统一先走 requestClose()（Promise 型弹窗会正常
   * resolve）再摘除宿主。
   */
  remove() {
    if (this.isOpen()) this.requestClose();
    return super.remove();
  }

  build() {
    if (this._built) return;
    this._built = true;
    this._content = Array.from(this.childNodes);

    this._titleNode = el('div', { class: 'ld-modal__title', id: uid('ld-dialog-title') });
    this._head = el('div', { class: 'ld-modal__head' }, this._titleNode);
    if (!this.props.hideClose) {
      this._head.appendChild(el('button', {
        class: 'ld-btn ld-btn--ghost ld-btn--icon ld-btn--sm',
        type: 'button',
        'aria-label': '关闭对话框',
        title: '关闭（Esc）',
        onclick: () => this.requestClose(),
      }, el('ld-icon', { name: 'close' })));
    }

    this._body = el('div', { class: 'ld-modal__body' });
    this._foot = el('div', { class: 'ld-modal__foot' });
    this._panel = el('div', {
      class: 'ld-modal__panel',
      role: 'dialog',
      'aria-modal': 'true',
      'aria-labelledby': this._titleNode.id,
    }, this._head, this._body, this._foot);
    this._overlay = el('div', {
      class: 'ld-modal',
      onclick: (event) => {
        if (event.target === this._overlay && this.props.dismissible) this.requestClose();
      },
    }, this._panel);

    for (const node of this._content) this._body.appendChild(node);
    clearNode(this);
    this.appendChild(this._overlay);
  }

  /** 打开：把遮罩挂到 body 上（与组件自身位置无关）。 */
  open(opts = {}) {
    if (opts && typeof opts === 'object') Object.assign(this.props, opts);
    this.build();
    this.refreshChrome();
    if (!this._overlay.isConnected) document.body.appendChild(this._overlay);
    this._lastFocus = document.activeElement;
    this._keyHandler = (event) => this.handleKey(event);
    document.addEventListener('keydown', this._keyHandler, true);
    if (this.onOpen) this.onOpen();
    window.requestAnimationFrame(() => this.focusFirst());
    this.emit('ld-open');
    return this;
  }

  close(result = null) {
    if (this._overlay && this._overlay.isConnected) this._overlay.remove();
    if (this._keyHandler) {
      document.removeEventListener('keydown', this._keyHandler, true);
      this._keyHandler = null;
    }
    if (this._lastFocus && typeof this._lastFocus.focus === 'function') {
      try {
        this._lastFocus.focus();
      } catch {
        /* 焦点元素已移除，忽略 */
      }
    }
    this.emit('ld-close', { result });
    if (this.onClosed) this.onClosed(result);
    return this;
  }

  isOpen() {
    return !!(this._overlay && this._overlay.isConnected);
  }

  /** 子类覆盖：Esc / 点遮罩时的行为。 */
  requestClose() {
    this.close(null);
  }

  handleKey(event) {
    if (!this.isOpen()) return;
    if (event.key === 'Escape' && this.props.dismissible) {
      event.preventDefault();
      event.stopPropagation();
      this.requestClose();
      return;
    }
    if (event.key === 'Tab') trapTab(this._panel, event);
  }

  focusables() {
    if (!this._panel) return [];
    const selector = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';
    return Array.from(this._panel.querySelectorAll(selector)).filter((node) => node.offsetParent !== null || node === document.activeElement);
  }

  focusFirst() {
    const list = this.focusables();
    const target = this._initialFocus && this._initialFocus.isConnected ? this._initialFocus : list[0];
    if (target && typeof target.focus === 'function') {
      target.focus();
      if (target.select && target.type === 'password') target.select();
    }
  }

  /** 同步标题与尺寸。 */
  refreshChrome() {
    if (!this._built) return;
    this._titleNode.textContent = this.props.title || '';
    const size = this.props.size || 'md';
    this._panel.dataset.size = size;
    this.dataset.size = size;
    this._head.classList.toggle('ld-hidden', !this.props.title && this.props.hideClose);
  }

  /** 用节点替换正文。 */
  setBody(node, { flush = false } = {}) {
    this.build();
    clearNode(this._body);
    this._body.classList.toggle('ld-modal__body--flush', !!flush);
    if (node) this._body.appendChild(node);
    return this;
  }

  /** 底部按钮：[{label,value,variant,icon,disabled,danger}]。 */
  setActions(actions = []) {
    this.build();
    clearNode(this._foot);
    for (const action of actions) {
      const value = action.value || action.id || action.label;
      const btn = el('ld-button', {
        label: action.label,
        icon: action.icon || undefined,
        variant: action.variant || (action.danger ? 'danger' : 'default'),
        disabled: action.disabled ? true : undefined,
        'data-value': value,
        onClick: () => this.emit('ld-action', { value, action }),
      });
      this._foot.appendChild(btn);
    }
    return this;
  }

  /** 按钮忙碌态。 */
  setBusy(on, value) {
    const buttons = this._foot ? Array.from(this._foot.querySelectorAll('ld-button')) : [];
    for (const btn of buttons) {
      if (value && btn.dataset.value !== value) continue;
      if (typeof btn.setBusy === 'function') btn.setBusy(on);
      else btn.toggleAttribute('disabled', !!on);
    }
    return this;
  }
}

function trapTab(panel, event) {
  if (!panel) return;
  const selector = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';
  const list = Array.from(panel.querySelectorAll(selector)).filter((node) => node.offsetParent !== null);
  if (!list.length) return;
  const first = list[0];
  const last = list[list.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

/** ld-modal：通用对话框。 */
export class LdModal extends OverlayDialog {
  static defaults = { ...OverlayDialog.defaults, actions: [] };
  static observedAttributes = ['title', 'size', 'open'];

  onMount() {
    if (this.hasAttribute('open') && !this.isOpen()) this.open();
  }

  onAttribute(name, value) {
    if (name === 'open') {
      if (value !== null) {
        if (!this.isOpen()) this.open();
      } else if (this.isOpen()) {
        this.close(null);
      }
      return;
    }
    this.update();
  }

  render() {
    this.build();
    this.refreshChrome();
    if (this.props.actions && this.props.actions.length) this.setActions(this.props.actions);
  }

  onClosed() {
    if (this.hasAttribute('open')) this.removeAttribute('open');
  }
}

defineProps(LdModal, ['title', 'size', 'actions']);

/** ld-confirm：Promise 化确认框。 */
export class LdConfirm extends OverlayDialog {
  static defaults = {
    ...OverlayDialog.defaults,
    size: 'sm',
    danger: false,
    confirmText: '确认',
    cancelText: '取消',
  };

  constructor() {
    super();
    this._resolve = null;
  }

  /** open({title,message,details,confirmText,cancelText,danger}) → Promise<boolean> */
  open(opts = {}) {
    const options = typeof opts === 'string' ? { message: opts } : opts;
    this.props.title = options.title || '请确认';
    this.props.size = options.size || 'sm';
    this.props.danger = !!options.danger;
    this.props.confirmText = options.confirmText || (this.props.danger ? '确认删除' : '确认');
    this.props.cancelText = options.cancelText || '取消';
    this.props.dismissible = options.dismissible !== false;

    const box = el('div', {}, el('p', { class: 'ld-modal__msg', text: options.message || '' }));
    if (Array.isArray(options.details) && options.details.length) {
      const list = el('ul', { class: 'ld-confirm-list' });
      for (const item of options.details) list.appendChild(el('li', { text: String(item) }));
      box.appendChild(list);
    }
    this.setBody(box);
    this.setActions([
      { label: this.props.cancelText, value: 'cancel' },
      { label: this.props.confirmText, value: 'confirm', variant: this.props.danger ? 'danger' : 'primary' },
    ]);
    super.open();
    return new Promise((resolve) => {
      this._resolve = resolve;
    });
  }

  finish(value) {
    const resolve = this._resolve;
    this._resolve = null;
    this.close(value);
    if (resolve) resolve(value);
  }

  requestClose() {
    this.finish(false);
  }

  onOpen() {
    this.addEventListener('ld-action', (event) => this.finish(event.detail.value === 'confirm'));
    this.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') {
        event.preventDefault();
        this.finish(true);
      }
    });
  }
}

/** 便捷函数：确认框。 */
export function confirmDialog(opts) {
  const dialog = document.createElement('ld-confirm');
  document.body.appendChild(dialog);
  return dialog.open(opts).finally(() => dialog.remove());
}

/** ld-password-prompt：Promise 化管理员二次密码输入。 */
export class LdPasswordPrompt extends OverlayDialog {
  static defaults = {
    ...OverlayDialog.defaults,
    size: 'sm',
    error: '',
    confirmText: '确认执行',
    message: '',
  };

  constructor() {
    super();
    this._resolve = null;
  }

  /** open({title,message,error,confirmText}) → Promise<string|null> */
  open(opts = {}) {
    const options = typeof opts === 'string' ? { message: opts } : opts;
    this.props.title = options.title || '管理员二次验证';
    this.props.size = 'sm';
    this.props.dismissible = options.dismissible !== false;
    this.props.error = options.error || '';
    this.props.message = options.message || '该操作会修改数据，请输入当前管理员密码确认。';
    this.props.confirmText = options.confirmText || '确认执行';

    this._input = el('input', {
      class: 'ld-input',
      type: 'password',
      autocomplete: 'current-password',
      spellcheck: 'false',
      'aria-label': '管理员密码',
      placeholder: '管理员密码',
    });
    this._field = el('ld-field', { label: '管理员密码', required: true }, this._input);
    if (this.props.error) this._field.setAttribute('error', this.props.error);

    const box = el('div', {}, el('p', { class: 'ld-modal__msg', text: this.props.message }), this._field);
    this.setBody(box);
    this.setActions([
      { label: '取消', value: 'cancel' },
      { label: this.props.confirmText, value: 'confirm', variant: 'primary' },
    ]);
    this._initialFocus = this._input;
    super.open();
    return new Promise((resolve) => {
      this._resolve = resolve;
    });
  }

  onOpen() {
    this._submit = () => this.submit();
    this._input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') {
        event.preventDefault();
        this.submit();
      }
    });
    this.addEventListener('ld-action', (event) => {
      if (event.detail.value === 'confirm') this.submit();
      else this.finish(null);
    });
  }

  submit() {
    const value = this._input.value;
    if (!value) {
      this._field.setError('请输入管理员密码');
      this._input.focus();
      return;
    }
    this.finish(value);
  }

  finish(value) {
    const resolve = this._resolve;
    this._resolve = null;
    this.close(value);
    if (resolve) resolve(value);
  }

  requestClose() {
    this.finish(null);
  }

  onClosed() {
    if (this._input) this._input.value = '';
  }
}

/** 便捷函数：管理员密码输入（返回值只存在于内存，绝不落盘）。 */
export function promptPassword(opts) {
  const prompt = document.createElement('ld-password-prompt');
  document.body.appendChild(prompt);
  return prompt.open(opts).finally(() => prompt.remove());
}

/* ---------- 提示条 ld-toast ---------- */

const TOAST_DEFAULT_MS = 3000;
const TOAST_ICONS = { success: 'check', error: 'alert', info: 'info' };

export class LdToast extends LdElement {
  constructor() {
    super();
    this._box = null;
  }

  render() {
    if (this._built) return;
    this._built = true;
    this._box = el('div', { class: 'ld-toasts', role: 'status', 'aria-live': 'polite', 'aria-atomic': 'false' });
    clearNode(this);
    this.appendChild(this._box);
  }

  /** push({message,title,type,timeout}) → 关闭函数。 */
  push(opts = {}) {
    this.render();
    const type = opts.type || 'info';
    const timeout = Number.isFinite(opts.timeout) ? opts.timeout : TOAST_DEFAULT_MS;
    const node = el('div', { class: `ld-toast ld-toast--${type}`, role: type === 'error' ? 'alert' : 'status' },
      el('span', { class: 'ld-toast__icon' }, el('ld-icon', { name: TOAST_ICONS[type] || 'info' })),
      el('div', { class: 'ld-toast__body' },
        opts.title ? el('div', { class: 'ld-toast__title', text: opts.title }) : null,
        el('div', { class: 'ld-toast__msg', text: opts.message || '' })),
      el('button', {
        class: 'ld-btn ld-btn--ghost ld-btn--icon ld-btn--sm ld-toast__close',
        type: 'button',
        'aria-label': '关闭提示',
        title: '关闭',
        onclick: () => dismiss(),
      }, el('ld-icon', { name: 'close', size: 'sm' })));

    let timer = 0;
    let closed = false;
    const dismiss = () => {
      if (closed) return;
      closed = true;
      window.clearTimeout(timer);
      node.classList.add('is-out');
      window.setTimeout(() => node.remove(), 180);
    };
    const start = () => {
      if (timeout > 0 && !closed) timer = window.setTimeout(dismiss, timeout);
    };
    node.addEventListener('mouseenter', () => window.clearTimeout(timer));
    node.addEventListener('mouseleave', start);
    this._box.appendChild(node);
    start();
    return dismiss;
  }
}

let toastHost = null;

function ensureToastHost() {
  if (!toastHost || !toastHost.isConnected) {
    toastHost = document.createElement('ld-toast');
    document.body.appendChild(toastHost);
  }
  return toastHost;
}

export function toast(message, type = 'info', opts = {}) {
  return ensureToastHost().push({ message, type, ...opts });
}

export const toastSuccess = (message, opts) => toast(message, 'success', opts);
export const toastError = (message, opts) => toast(message, 'error', opts);
export const toastInfo = (message, opts) => toast(message, 'info', opts);

/** 统一错误提示：ApiError 直接显示服务端中文消息。 */
export function toastApiError(err, fallback = '操作失败') {
  const message = err && err.message ? err.message : fallback;
  return toastError(message, { timeout: 5000 });
}

/* ---------- 菜单 ld-menu ---------- */

export class LdMenu extends LdElement {
  static defaults = { items: [], header: '' };

  render() {
    if (!this._built) {
      this._built = true;
      this._menu = el('div', { class: 'ld-menu', role: 'menu' });
      clearNode(this);
      this.appendChild(this._menu);
    }
    clearNode(this._menu);
    const items = this.props.items || [];
    if (this.props.header) this._menu.appendChild(el('div', { class: 'ld-menu__head', text: this.props.header }));
    for (const item of items) {
      if (item.separator) {
        this._menu.appendChild(el('div', { class: 'ld-menu__sep' }));
        continue;
      }
      const classes = ['ld-menu__item'];
      if (item.danger) classes.push('ld-menu__item--danger');
      const btn = el('button', {
        class: classes.join(' '),
        type: 'button',
        role: 'menuitem',
        disabled: item.disabled ? true : undefined,
        title: item.hint || item.label,
        onclick: () => this.select(item),
      });
      if (item.icon) btn.appendChild(el('ld-icon', { name: item.icon }));
      btn.appendChild(el('span', { class: 'ld-menu__label', text: item.label }));
      if (item.hint) btn.appendChild(el('span', { class: 'ld-menu__hint', text: item.hint }));
      this._menu.appendChild(btn);
    }
  }

  select(item) {
    if (item.disabled) return;
    this.emit('ld-select', { id: item.id || item.label, item });
    this.closeMenu();
  }

  openAt({ x, y, target, align = 'start' } = {}) {
    this.props.items = this.props.items || [];
    this.renderNow();
    if (!this.isConnected) document.body.appendChild(this);
    this._place({ x, y, target, align });
    this._outside = (event) => {
      if (this.contains(event.target)) return;
      this.closeMenu();
    };
    this._escape = (event) => {
      if (event.key === 'Escape') this.closeMenu();
    };
    window.setTimeout(() => {
      document.addEventListener('pointerdown', this._outside, true);
      document.addEventListener('keydown', this._escape, true);
    }, 0);
    return this;
  }

  _place({ x, y, target, align }) {
    const rect = this._menu.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const margin = 8;
    let left = x;
    let top = y;
    if (target) {
      const box = target.getBoundingClientRect();
      left = align === 'end' ? box.right - rect.width : box.left;
      top = box.bottom + 4;
    }
    left = Math.max(margin, Math.min(left, vw - rect.width - margin));
    if (top + rect.height > vh - margin) top = Math.max(margin, (target ? target.getBoundingClientRect().top : top) - rect.height - 4);
    top = Math.max(margin, Math.min(top, vh - rect.height - margin));
    this._menu.style.left = `${Math.round(left)}px`;
    this._menu.style.top = `${Math.round(top)}px`;
  }

  closeMenu() {
    if (this._outside) document.removeEventListener('pointerdown', this._outside, true);
    if (this._escape) document.removeEventListener('keydown', this._escape, true);
    this._outside = null;
    this._escape = null;
    this.emit('ld-close-menu');
    this.remove();
  }
}

defineProps(LdMenu, ['items', 'header']);

/**
 * 弹出一个菜单并等待选择。
 * showMenu({items, x, y, target, header}) → Promise<string|null>
 */
export function showMenu(opts = {}) {
  const menu = document.createElement('ld-menu');
  menu.items = opts.items || [];
  menu.header = opts.header || '';
  document.body.appendChild(menu);
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => {
      if (done) return;
      done = true;
      resolve(value);
      if (menu.isConnected) menu.remove();
    };
    menu.addEventListener('ld-select', (event) => finish(event.detail.id));
    menu.addEventListener('ld-close-menu', () => finish(null));
    menu.openAt(opts);
  });
}

/* ---------- 目录选择器 ld-dir-picker ---------- */

export class LdDirPicker extends LdElement {
  static defaults = { loader: null, path: '', loading: false, error: '', rootLabel: '根目录' };

  constructor() {
    super();
    this._entries = [];
  }

  render() {
    if (!this._built) {
      this._built = true;
      this._crumbs = el('ld-breadcrumb');
      this._upBtn = el('ld-button', { icon: 'arrow-up', variant: 'ghost', size: 'sm', title: '上一级', onClick: () => this.goUp() });
      this._refreshBtn = el('ld-button', { icon: 'refresh', variant: 'ghost', size: 'sm', title: '刷新', onClick: () => this.load() });
      this._pathText = el('div', { class: 'ld-picker__path' });
      this._list = el('div', { class: 'ld-picker__list', role: 'listbox', 'aria-label': '目录列表' });
      this._bar = el('div', { class: 'ld-picker__bar' }, this._upBtn, this._pathText, this._refreshBtn);
      this._root = el('div', { class: 'ld-picker' }, this._crumbs, this._bar, this._list);
      clearNode(this);
      this.appendChild(this._root);
    }
    const path = this.props.path || '';
    this._crumbs.items = pathCrumbs(path, this.props.rootLabel || '根目录');
    this._pathText.textContent = path ? `/${path}` : '/';
    this._pathText.title = path;
    this._upBtn.toggleAttribute('disabled', !path);

    clearNode(this._list);
    if (this.props.loading) {
      this._list.appendChild(el('div', { class: 'ld-picker__state' }, el('ld-spinner'), ' 正在读取目录…'));
      return;
    }
    if (this.props.error) {
      this._list.appendChild(el('div', { class: 'ld-picker__state', text: this.props.error }));
      return;
    }
    if (!this._entries.length) {
      this._list.appendChild(el('div', { class: 'ld-picker__state', text: '该目录下没有子文件夹' }));
      return;
    }
    for (const entry of this._entries) {
      this._list.appendChild(el('button', {
        class: 'ld-picker__row',
        type: 'button',
        role: 'option',
        title: entry.path,
        onclick: () => this.enter(entry.path),
      }, el('ld-file-icon', { kind: 'folder', size: 'sm' }), el('span', { text: entry.name }),
        el('ld-icon', { name: 'chevron-right', size: 'sm' })));
    }
  }

  onMount() {
    this.addEventListener('ld-navigate', (event) => {
      this.props.path = event.detail.path;
      this.load();
    });
  }

  /** 打开并加载指定目录（loader 由页面注入）。 */
  async open(path = '') {
    this.props.path = path;
    this.renderNow();
    await this.load();
    return this;
  }

  async load() {
    if (typeof this.props.loader !== 'function') {
      this.props.error = '未提供目录读取器';
      this.renderNow();
      return;
    }
    this.props.loading = true;
    this.props.error = '';
    this.renderNow();
    try {
      const data = await this.props.loader(this.props.path || '');
      const entries = Array.isArray(data) ? data : (data && data.entries) || [];
      this._entries = entries.filter((entry) => entry.isDir);
      this.props.error = '';
    } catch (err) {
      this._entries = [];
      this.props.error = err && err.message ? err.message : '目录读取失败';
    }
    this.props.loading = false;
    this.renderNow();
  }

  enter(path) {
    this.props.path = path;
    this.emit('ld-navigate', { path });
    this.load();
  }

  goUp() {
    const next = parentPath(this.props.path || '');
    this.props.path = next;
    this.emit('ld-navigate', { path: next });
    this.load();
  }

  /** 当前选中的目录路径。 */
  get current() {
    return this.props.path || '';
  }
}

defineProps(LdDirPicker, ['loader', 'path', 'loading', 'error', 'rootLabel']);

/* ---------- 上传面板 ld-upload-panel ---------- */

const UPLOAD_STATE_TEXT = {
  queued: '等待中',
  running: '上传中',
  paused: '已暂停',
  done: '已完成',
  error: '出错',
  canceled: '已取消',
};

const UPLOAD_STATE_CLASS = {
  queued: '',
  running: '',
  paused: 'ld-up__state--paused',
  done: 'ld-up__state--done',
  error: 'ld-up__state--error',
  canceled: '',
};

export class LdUploadPanel extends LdElement {
  static defaults = { items: [], sessions: [], collapsed: false, progress: null, title: '传输列表' };

  render() {
    const items = this.props.items || [];
    const sessions = this.props.sessions || [];
    const hasContent = items.length > 0 || sessions.length > 0;
    if (!hasContent) {
      clearNode(this);
      return;
    }

    const running = items.filter((item) => item.status === 'running').length;
    const summary = this.props.progress
      ? `${this.props.progress.done}/${this.props.progress.total} 已完成`
      : running > 0 ? `${running} 个上传中` : `${items.length} 个任务`;

    const head = el('div', { class: 'ld-up__head' },
      el('ld-icon', { name: 'upload-cloud', size: 'md' }),
      el('span', { class: 'ld-up__title', text: this.props.title }),
      el('span', { class: 'ld-up__summary', text: summary }),
      el('ld-button', {
        icon: this.props.collapsed ? 'chevron-up' : 'chevron-down',
        variant: 'ghost',
        size: 'sm',
        title: this.props.collapsed ? '展开' : '最小化',
        'aria-label': this.props.collapsed ? '展开传输列表' : '最小化传输列表',
        onClick: () => this.emit('ld-upload-action', { action: 'toggle' }),
      }));

    const panel = el('div', { class: 'ld-up' }, head);
    if (!this.props.collapsed) {
      const list = el('div', { class: 'ld-up__list' });
      if (!items.length) list.appendChild(el('div', { class: 'ld-up__empty', text: '没有正在进行的上传' }));
      for (const item of items) list.appendChild(this._renderItem(item));
      panel.appendChild(list);

      if (sessions.length) {
        const sessionBox = el('div', { class: 'ld-up__list' });
        for (const session of sessions) {
          sessionBox.appendChild(el('div', { class: 'ld-up__item' },
            el('div', { class: 'ld-up__row' },
              el('ld-file-icon', { kind: kindFromName(session.name), size: 'sm' }),
              el('span', { class: 'ld-up__name ld-ellipsis', title: session.name, text: session.name }),
              el('span', { class: 'ld-up__state ld-up__state--paused', text: '未完成（可续传）' })),
            el('div', { class: 'ld-up__meta' },
              el('span', { text: `${fmtBytes(session.bytesReceived || 0)} / ${fmtBytes(session.size)}` }),
              el('span', { text: `剩 ${Math.max(0, (session.chunkCount || 0) - (session.received || []).length)} 片` }),
              el('span', { text: `目录 /${session.dir || ''}` })),
            el('div', { class: 'ld-up__acts' },
              el('ld-button', {
                label: '放弃',
                variant: 'ghost-danger',
                size: 'sm',
                onClick: () => this.emit('ld-upload-action', { action: 'cancel-session', id: session.id }),
              }))));
        }
        panel.appendChild(el('div', { class: 'ld-up__hint', text: '提示：重新选择同一个文件即可从断点继续上传。' }));
        panel.appendChild(sessionBox);
      }

      const foot = el('div', { class: 'ld-up__hint' },
        el('div', { class: 'ld-toolbar' },
          el('ld-button', {
            label: '清除已完成',
            variant: 'ghost',
            size: 'sm',
            onClick: () => this.emit('ld-upload-action', { action: 'clear-finished' }),
          }),
          el('div', { class: 'ld-toolbar__spacer' }),
          el('ld-button', {
            label: '全部取消',
            variant: 'ghost-danger',
            size: 'sm',
            onClick: () => this.emit('ld-upload-action', { action: 'cancel-all' }),
          })));
      panel.appendChild(foot);
    }

    clearNode(this);
    this.appendChild(panel);
  }

  _renderItem(item) {
    const pct = item.size > 0 ? Math.min(100, (item.loaded / item.size) * 100) : 0;
    const state = item.status || 'queued';
    const meta = el('div', { class: 'ld-up__meta' },
      el('span', { text: `${fmtBytes(item.loaded || 0)} / ${fmtBytes(item.size || 0)}` }),
      state === 'running' ? el('span', { text: fmtSpeed(item.speed) }) : null,
      state === 'running' ? el('span', { text: `剩余 ${fmtEta(item.eta)}` }) : null,
      el('span', { class: `ld-up__state ${UPLOAD_STATE_CLASS[state] || ''}`, text: UPLOAD_STATE_TEXT[state] || state }));

    const acts = el('div', { class: 'ld-up__acts' });
    const action = (name, icon, title, variant = 'ghost') => el('ld-button', {
      icon,
      variant,
      size: 'sm',
      title,
      'aria-label': title,
      onClick: () => this.emit('ld-upload-action', { action: name, id: item.id }),
    });

    if (state === 'running' || state === 'queued') acts.appendChild(action('pause', 'pause', '暂停'));
    if (state === 'paused') acts.appendChild(action('resume', 'play', '继续'));
    if (state === 'error') acts.appendChild(action('retry', 'refresh', '重试'));
    if (state === 'running' || state === 'paused' || state === 'queued') {
      acts.appendChild(action('cancel', 'close', '取消', 'ghost-danger'));
    }
    if (state === 'done' || state === 'canceled' || state === 'error') {
      acts.appendChild(action('dismiss', 'trash', '从列表移除', 'ghost-danger'));
    }

    return el('div', { class: 'ld-up__item' },
      el('div', { class: 'ld-up__row' },
        el('ld-file-icon', { kind: kindFromName(item.name), size: 'sm' }),
        el('span', { class: 'ld-up__name ld-ellipsis', title: item.name, text: item.name }),
        acts),
      el('ld-progress', {
        value: pct,
        max: 100,
        state: state === 'error' ? 'error' : state === 'done' ? 'success' : '',
      }),
      meta,
      item.error ? el('div', { class: 'ld-up__state ld-up__state--error', text: item.error }) : null,
      item.resumed && state === 'running' ? el('div', { class: 'ld-up__state', text: '已从断点续传' }) : null);
  }
}

defineProps(LdUploadPanel, ['items', 'sessions', 'collapsed', 'progress', 'title']);

/** 按文件名推断 kind（与后端 core.KindOf 保持一致的分类）。 */
const EXT_KIND = {
  jpg: 'image', jpeg: 'image', png: 'image', gif: 'image', webp: 'image', bmp: 'image',
  svg: 'image', avif: 'image', ico: 'image', heic: 'image',
  mp4: 'video', mkv: 'video', mov: 'video', avi: 'video', webm: 'video', flv: 'video',
  wmv: 'video', m4v: 'video', ogv: 'video',
  mp3: 'audio', wav: 'audio', flac: 'audio', aac: 'audio', ogg: 'audio', m4a: 'audio',
  opus: 'audio', wma: 'audio',
  pdf: 'pdf',
  txt: 'text', md: 'text', markdown: 'text', log: 'text', csv: 'text', tsv: 'text',
  json: 'text', yaml: 'text', yml: 'text', xml: 'text', ini: 'text', conf: 'text',
  toml: 'text', go: 'text', js: 'text', ts: 'text', tsx: 'text', jsx: 'text', py: 'text',
  java: 'text', c: 'text', h: 'text', cpp: 'text', hpp: 'text', cs: 'text', rs: 'text',
  rb: 'text', php: 'text', sh: 'text', ps1: 'text', bat: 'text', sql: 'text',
  html: 'text', css: 'text', scss: 'text',
  doc: 'office', docx: 'office', xls: 'office', xlsx: 'office', ppt: 'office',
  pptx: 'office', odt: 'office', ods: 'office', odp: 'office', rtf: 'office',
  zip: 'archive', tar: 'archive', gz: 'archive', tgz: 'archive', bz2: 'archive',
  xz: 'archive', '7z': 'archive', rar: 'archive',
};

export function kindFromName(name) {
  const clean = String(name || '');
  const idx = clean.lastIndexOf('.');
  if (idx < 0 || idx === clean.length - 1) return 'other';
  return EXT_KIND[clean.slice(idx + 1).toLowerCase()] || 'other';
}

/* ---------- 预览对话框 ld-preview ---------- */

export class LdPreview extends OverlayDialog {
  static defaults = { ...OverlayDialog.defaults, size: 'xl', entry: null, plan: null, text: '', truncated: false, note: '' };

  constructor() {
    super();
    this._entry = null;
    this._plan = null;
    this._text = '';
    this._truncated = false;
    this._note = '';
    this._user = null;
    this._editing = false;
    this._dirty = false;
    this._original = '';
    this._baseModTime = '';
    this._saving = false;
    this._readonly = false;
    this._editor = null;
    this._stageEl = null;
  }

  /** openPreview({entry, plan, text, truncated, note, user, baseModTime, readOnly, editing}) */
  openPreview(data = {}) {
    this._entry = data.entry || null;
    this._plan = data.plan || { mode: 'download', kind: 'other' };
    this._text = data.text || '';
    this._truncated = !!data.truncated;
    this._note = data.note || '';
    this._user = data.user || null;
    this._baseModTime = data.baseModTime || '';
    this._readonly = !!data.readOnly;
    this._original = this._text;
    this._editing = !!data.editing && this._canEdit();
    this._dirty = false;
    this.props.title = this._entry ? this._entry.name : '预览';
    this.props.size = 'xl';

    this._stageEl = el('div', { class: 'ld-prev__stage' });
    const body = el('div', {}, this._paintStage());
    this._bodyWrap = body;
    this.setBody(body, { flush: true });
    this.setActions(this._actions());
    this.addEventListener('ld-action', this._onAction);
    this.addEventListener('ld-download', () => {});
    super.open();
    if (this._editing) this._focusEditor();
    return this;
  }

  /** 是否允许进入编辑：文本类 + 非只读 + 未截断。 */
  _canEdit() {
    const kind = (this._plan && this._plan.kind) || (this._entry && this._entry.kind) || '';
    return kind === 'text' && !this._readonly && !this._truncated && !!this._entry;
  }

  _paintStage() {
    clearNode(this._stageEl);
    this._editor = null;
    if (this._editing) this._stageEl.appendChild(this._renderEditor());
    else this._stageEl.appendChild(this._renderStage());
    return this._stageEl;
  }

  _renderEditor() {
    // 行号槽 + 文本框：textarea 关掉自动换行（wrap=off），
    // 这样一行永远对应一个行号，滚动时逐行对齐。
    const gutter = el('div', { class: 'ld-editor__gutter', 'aria-hidden': 'true' });
    const ta = el('textarea', {
      class: 'ld-prev__editor',
      spellcheck: 'false',
      wrap: 'off',
      'aria-label': `编辑 ${this._entry ? this._entry.name : ''}`,
    });
    ta.value = this._text;

    const paintGutter = () => {
      const count = ta.value.split('\n').length;
      let out = '';
      for (let i = 1; i <= count; i += 1) out += i + '\n';
      gutter.textContent = out;
      // 行号数量变了要重新对齐滚动位置。
      gutter.scrollTop = ta.scrollTop;
    };
    paintGutter();

    ta.addEventListener('input', () => {
      paintGutter();
      this._dirty = ta.value !== this._original;
      this._refreshFoot();
    });
    ta.addEventListener('scroll', () => { gutter.scrollTop = ta.scrollTop; });
    // Tab 插入两个空格而不是跳走焦点；Ctrl/Cmd+S 直接保存。
    ta.addEventListener('keydown', (e) => {
      if (e.key === 'Tab' && !e.shiftKey) {
        e.preventDefault();
        const { selectionStart: s, selectionEnd: en } = ta;
        ta.setRangeText('  ', s, en, 'end');
        paintGutter();
        this._dirty = ta.value !== this._original;
        this._refreshFoot();
      } else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        this._save();
      }
    });
    this._editor = ta;
    this._gutter = gutter;
    return el('div', { class: 'ld-editor' }, gutter, ta);
  }

  _focusEditor() {
    if (!this._editor) return;
    requestAnimationFrame(() => {
      this._editor.focus();
      // 光标放到开头，方便从第一行开始改。
      this._editor.setSelectionRange(0, 0);
      this._editor.scrollTop = 0;
    });
  }

  _actions() {
    const actions = [];
    const isText = ((this._plan && this._plan.kind) || '') === 'text';
    if (isText && this._canEdit()) {
      if (this._editing) {
        actions.push({ label: '保存', icon: 'check', value: 'save', variant: 'primary' });
        actions.push({ label: '取消编辑', value: 'cancel-edit' });
      } else {
        actions.push({ label: '编辑', icon: 'edit', value: 'edit', variant: 'primary' });
      }
    }
    if (this._entry && !this._entry.isDir) {
      actions.push({ label: '下载', icon: 'download', value: 'download', variant: isText && this._canEdit() ? 'default' : 'primary' });
    }
    actions.push({ label: '关闭', value: 'close' });
    return actions;
  }

  /** 保存按钮的可用状态跟着脏标记走。 */
  _refreshFoot() {
    const btns = this.querySelectorAll('.ld-modal__foot button');
    for (const btn of btns) {
      const label = btn.textContent.trim();
      if (label === '保存') btn.disabled = !this._dirty || this._saving;
    }
    const title = this.querySelector('.ld-modal__title');
    if (title) {
      const base = this._entry ? this._entry.name : '预览';
      title.textContent = this._editing ? `${base}${this._dirty ? ' • 有未保存修改' : ''}` : base;
    }
  }

  _onAction = (event) => {
    const value = event.detail.value;
    if (value === 'download') this.emit('ld-download', { entry: this._entry });
    else if (value === 'close') this.close(null);
    else if (value === 'edit') this._enterEdit();
    else if (value === 'cancel-edit') this._cancelEdit();
    else if (value === 'save') this._save();
  };

  _enterEdit() {
    if (!this._canEdit()) return;
    this._editing = true;
    this._dirty = false;
    this._paintStage();
    this.setActions(this._actions());
    this._focusEditor();
  }

  _cancelEdit() {
    if (this._dirty && !window.confirm('放弃未保存的修改？')) return;
    this._editing = false;
    this._dirty = false;
    this._text = this._original;
    this._paintStage();
    this.setActions(this._actions());
    this._refreshFoot();
  }

  async _save() {
    if (!this._editor || this._saving) return;
    if (!this._dirty) {
      toastInfo('没有需要保存的修改');
      return;
    }
    this._saving = true;
    this._refreshFoot();
    try {
      const res = await api('/api/fs/text', {
        method: 'POST',
        user: this._user,
        body: {
          path: this._entry.path,
          content: this._editor.value,
          baseModTime: this._baseModTime,
        },
      });
      this._original = this._editor.value;
      this._text = this._editor.value;
      this._dirty = false;
      if (res && res.modTime) this._baseModTime = res.modTime;
      if (res && typeof res.size === 'number') this._entry.size = res.size;
      toastSuccess('已保存');
      // 内容变了，尺寸/时间都变了：让列表刷新。
      this.emit('ld-saved', { entry: this._entry, size: res && res.size, modTime: res && res.modTime });
    } catch (err) {
      toastApiError(err, '保存失败');
    } finally {
      this._saving = false;
      this._refreshFoot();
    }
  }

  _inlineUrl() {
    if (!this._entry) return '';
    return fileUrl(this._entry.path, { user: this._user, inline: 1 });
  }

  _renderStage() {
    const plan = this._plan || {};
    const entry = this._entry || { name: '', size: 0 };
    const kind = plan.kind || entry.kind || 'other';
    const size = plan.size || entry.size || 0;

    if (plan.mode === 'inline') {
      if (kind === 'image') return el('img', { src: this._inlineUrl(), alt: entry.name });
      if (kind === 'video') {
        return el('video', { src: this._inlineUrl(), controls: true, preload: 'metadata', playsinline: true });
      }
      if (kind === 'audio') return el('audio', { src: this._inlineUrl(), controls: true, preload: 'metadata' });
      if (kind === 'pdf') return el('iframe', { src: this._inlineUrl(), title: `PDF 预览：${entry.name}` });
      if (kind === 'text') {
        const box = el('div', { style: { width: '100%' } });
        box.appendChild(el('pre', { class: 'ld-prev__text', text: this._text || '（空文件）' }));
        if (this._truncated) {
          box.appendChild(el('div', { class: 'ld-note ld-note--warn', style: { margin: 'var(--ld-sp-3)' } },
            el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: 'alert' })),
            el('div', { class: 'ld-note__body', text: '文件超过 1 MiB，这里只显示前 1 MiB 内容，完整内容请下载查看。' })));
        } else if (this._readonly) {
          box.appendChild(el('div', { class: 'ld-note ld-note--info', style: { margin: 'var(--ld-sp-3)' } },
            el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: 'lock' })),
            el('div', { class: 'ld-note__body', text: '当前账号没有修改权限，只能查看。' })));
        }
        return box;
      }
    }

    if (plan.mode === 'convert') {
      if (!plan.builtin) {
        return el('iframe', {
          src: withQuery('/api/preview/office', { path: entry.path, user: this._user }),
          title: `Office 预览：${entry.name}`,
        });
      }
      return this._fallback({
        icon: 'office',
        title: '该 Office 格式暂不支持在线预览',
        desc: this._note || '服务端没有可用的文档转换器（未安装 LibreOffice），这一版先提供文件信息与下载入口。下载后用本地 Office 或 WPS 打开即可查看完整内容。',
        entry,
        size,
      });
    }

    return this._fallback({
      icon: KIND_ICONS[kind] || 'file',
      title: '该格式不支持在线预览',
      desc: this._note || '请下载后使用本地应用打开。',
      entry,
      size,
    });
  }

  _fallback({ icon, title, desc, entry, size }) {
    return el('div', { class: 'ld-prev__fallback' },
      el('ld-file-icon', { kind: entry.kind || this._plan.kind || 'other', size: 'lg' }),
      el('div', { class: 'ld-empty__title', text: title }),
      el('div', { class: 'ld-empty__desc', text: desc }),
      el('dl', { class: 'ld-prev__filemeta' },
        el('dt', { text: '文件名' }), el('dd', { text: entry.name || '—' }),
        el('dt', { text: '大小' }), el('dd', { text: fmtBytes(size) }),
        el('dt', { text: '类型' }), el('dd', { text: `${kindLabel(entry.kind || this._plan.kind)}${entry.ext ? ` (.${entry.ext})` : ''}` })),
      el('ld-button', {
        label: '下载文件',
        icon: 'download',
        variant: 'primary',
        onClick: () => this.emit('ld-download', { entry }),
      }),
      el('div', { class: 'ld-empty__desc', text: '提示：也可以把该文件复制/移动到有转换环境的机器上预览。' }));
  }
}

/* ---------- 预览数据加载（服务端计划 + 文本内容） ---------- */

/**
 * 读取预览计划；文本类会顺带取回前 1 MiB 内容。
 * 返回 {plan, text, truncated, note, baseModTime}
 */
export async function fetchPreview(path, { user } = {}) {
  const plan = await api(withQuery('/api/preview', { path, user }));
  let text = null;
  let truncated = false;
  let note = '';
  let baseModTime = '';

  if (plan.mode === 'inline' && plan.kind === 'text') {
    const limit = 1 << 20;
    // 判断是否超过单页预览上限要按真实大小来，不能用 HTTP 206 判断。
    truncated = (plan.size || 0) > limit;
    if (truncated) {
      const response = await api(fileUrl(path, { user, inline: 1 }), {
        raw: true,
        headers: { Range: `bytes=0-${limit - 1}` },
      });
      text = await response.text();
    } else {
      // 未超限就整文件读取，同时拿到服务端精确的 modTime（纳秒）。
      // Last-Modified 只有秒级精度，拿它做并发校验一定会误判冲突。
      try {
        const file = await api(withQuery('/api/fs/text', { path, user }));
        text = (file && file.content) || '';
        baseModTime = (file && file.modTime) || '';
        if (file && typeof file.size === 'number') plan.size = file.size;
      } catch (err) {
        // 没有修改权限（或非文本）时退回普通内联读取，只读预览照常可用。
        const response = await api(fileUrl(path, { user, inline: 1 }), { raw: true });
        text = await response.text();
        baseModTime = '';
      }
    }
  }

  if (plan.mode === 'convert' && !plan.builtin) {
    try {
      await api(withQuery('/api/preview/office', { path, user }), { raw: true, method: 'HEAD' });
    } catch (err) {
      plan.builtin = true;
      note = err && err.code === 'office_preview_unavailable'
        ? '服务端未安装 LibreOffice，无法转换该格式。'
        : (err && err.message) || '';
    }
  }

  return { plan, text, truncated, note, baseModTime };
}

/**
 * 读取可编辑的文本全文（用于"编辑文本"入口，保证拿到精确 modTime）。
 * 返回 {plan, text, truncated, note, baseModTime}
 */
export async function fetchTextForEdit(path, { user } = {}) {
  const plan = await api(withQuery('/api/preview', { path, user }));
  const file = await api(withQuery('/api/fs/text', { path, user }));
  if (typeof file.size === 'number') plan.size = file.size;
  return {
    plan,
    text: (file && file.content) || '',
    truncated: false,
    note: '',
    baseModTime: (file && file.modTime) || '',
  };
}

/* ---------- 数据表格 ld-data-table ---------- */

/**
 * 通用表格：列定义 + 排序 + 多选 + 分批渲染 + 行内菜单。
 * 事件：ld-sort / ld-select / ld-select-all / ld-row-click / ld-row-open / ld-row-menu
 * 说明：组件不做任何请求，排序只上报意图，排序结果由页面传回 rows。
 */
export class LdDataTable extends LdElement {
  static defaults = {
    columns: [], rows: [], rowKey: 'path', selectable: false, selected: null,
    sort: null, batch: 60, cards: false, loading: false, density: 'comfortable',
    ariaLabel: '数据表格', emptyIcon: 'info', emptyTitle: '暂无数据', emptyDesc: '',
    rowMenu: true, menuLabel: '更多操作',
    // maxHeight 给定时表格内部滚动，页脚/分页条可以留在视口内
    maxHeight: '',
    // 行本身可点击打开（文件列表、搜索结果、用户列表），悬停显示手型指针。
    // 纯展示型表格（日志、回收站）保持默认箭头，避免暗示可以点开。
    clickable: false,
    // 窄屏（≤859px）自动改用卡片布局。表格在手机上会横向溢出，
    // 卡片把「名称 + 若干字段」重新排成两行，比左右滑动看表格友好得多。
    // 显式设了 cards 的场景（文件管理器的视图切换按钮）以 cards 为准。
    autoCards: false,
  };

  constructor() {
    super();
    this._rows = [];
    this._rowNodes = [];
    this._shown = 0;
    this._anchor = -1;
    this._observer = null;
    this._keyHandler = (event) => this._onKeyDown(event);
  }

  render() {
    const rows = this.props.rows || [];
    const cols = this.props.columns || [];
    const cards = this._effectiveCards();
    const density = this.props.density || 'comfortable';
    this.dataset.density = density;
    this.dataset.view = cards ? 'cards' : 'table';

    const sortKey = this.props.sort ? `${this.props.sort.key}:${this.props.sort.dir}` : '';
    const changed = this._lastRows !== rows || this._lastColumns !== cols
      || this._lastCards !== cards || this._lastLoading !== this.props.loading
      || this._lastSort !== sortKey;

    this._lastRows = rows;
    this._lastColumns = cols;
    this._lastCards = cards;
    this._lastLoading = this.props.loading;
    this._lastSort = sortKey;

    if (!changed && this._builtBody) {
      this._paintSelection();
      return;
    }
    this._build(rows, cols, cards);
  }

  /* ----- 构建 ----- */

  /** 实际用卡片还是表格：显式 cards 优先，其次看 autoCards + 窄屏。 */
  _effectiveCards() {
    if (this.props.autoCards && isNarrow()) return true;
    return !!this.props.cards;
  }

  _build(rows, cols, cards) {
    this._rows = rows;
    this._rowNodes = [];
    this._shown = 0;
    this._anchor = -1;
    if (this._observer) {
      this._observer.disconnect();
      this._observer = null;
    }
    clearNode(this);

    if (!rows.length) {
      this._builtBody = false;
      const empty = el('ld-empty', {
        icon: this.props.loading ? 'refresh' : this.props.emptyIcon || 'info',
        title: this.props.loading ? '正在加载…' : this.props.emptyTitle || '暂无数据',
        description: this.props.emptyDesc || '',
      });
      this.appendChild(empty);
      return;
    }

    const wrap = el('div', { class: cards ? 'ld-cards' : 'ld-tablewrap' });
    this._wrap = wrap;
    // 指定了 maxHeight 就固定高度内部滚动（表头 sticky 依旧有效）。
    // 卡片模式不做限高：手机上再套一层内层滚动，手指会分不清滑的是哪一层。
    if (this.props.maxHeight && !cards) wrap.style.maxHeight = this.props.maxHeight;

    if (cards) {
      this._bodyEl = wrap;
      this._wrap.setAttribute('role', 'list');
      this._sentinel = el('div', { class: 'ld-table__more', 'aria-hidden': 'true' });
      wrap.appendChild(this._sentinel);
    } else {
      const headRow = el('tr', { role: 'row' });
      if (this.props.selectable) {
        this._headCheck = el('input', { type: 'checkbox', 'aria-label': '全选当前列表' });
        this._headCheck.addEventListener('click', (event) => {
          event.stopPropagation();
          this.emit('ld-select-all', { checked: this._headCheck.checked });
        });
        headRow.appendChild(el('th', { class: 'ld-col ld-col-check', scope: 'col' }, this._headCheck));
      }
      for (const col of cols) headRow.appendChild(this._renderHeadCell(col));
      if (this.props.rowMenu) headRow.appendChild(el('th', { class: 'ld-col ld-col-act', scope: 'col' }, el('span', { class: 'ld-sr', text: this.props.menuLabel })));
      const table = el('table', { class: 'ld-table', role: 'grid', 'aria-label': this.props.ariaLabel || '数据表格' },
        el('thead', {}, headRow));
      this._tbody = el('tbody');
      this._moreCell = el('td', { colspan: String((cols.length || 1) + 2) });
      this._sentinel = el('tr', { class: 'ld-table__more', 'aria-hidden': 'true' }, this._moreCell);
      this._tbody.appendChild(this._sentinel);
      table.appendChild(this._tbody);
      wrap.appendChild(table);
      this._bodyEl = this._tbody;
    }

    this.appendChild(wrap);
    this._builtBody = true;

    this._appendBatch(this.props.batch || 60);
    this._paintSelection();

    if (typeof IntersectionObserver === 'function') {
      this._observer = new IntersectionObserver((entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) this._appendBatch(this.props.batch || 60);
        }
      }, { root: null, rootMargin: '400px 0px' });
      this._observer.observe(this._sentinel);
    } else {
      this._appendBatch(this._rows.length);
    }
  }

  _renderHeadCell(col) {
    const sorted = this.props.sort && this.props.sort.key === col.key ? this.props.sort.dir : '';
    const th = el('th', {
      class: `ld-col${col.class ? ` ${col.class}` : ''}`,
      scope: 'col',
      style: col.width ? { width: col.width } : undefined,
      'aria-sort': sorted === 'asc' ? 'ascending' : sorted === 'desc' ? 'descending' : 'none',
    });
    if (col.sortable) {
      th.appendChild(el('button', {
        class: 'ld-th-sort',
        type: 'button',
        'data-dir': sorted || undefined,
        title: `按${col.title}排序`,
        onclick: () => this._toggleSort(col),
      }, el('span', { text: col.title }),
        el('ld-icon', { name: sorted ? (sorted === 'desc' ? 'arrow-down' : 'arrow-up') : 'sort', size: 'sm' })));
    } else {
      th.appendChild(el('span', { text: col.title }));
    }
    return th;
  }

  _toggleSort(col) {
    const current = this.props.sort || {};
    const dir = current.key === col.key && current.dir === 'asc' ? 'desc' : 'asc';
    this.emit('ld-sort', { key: col.key, dir });
  }

  /* ----- 行 ----- */

  _keyOf(row) {
    const key = this.props.rowKey;
    const value = typeof key === 'function' ? key(row) : row[key];
    return String(value === undefined || value === null ? '' : value);
  }

  _rowHandlers(row, index) {
    return {
      ondblclick: () => this.emit('ld-row-open', { row, index }),
      oncontextmenu: (event) => {
        event.preventDefault();
        this.emit('ld-row-menu', { row, index, x: event.clientX, y: event.clientY });
      },
      onclick: (event) => {
        if (event.target.closest('.ld-col-check, .ld-row-menu-btn')) return;
        this.emit('ld-row-click', { row, index, event });
      },
    };
  }

  _renderRow(row, index) {
    const node = el('tr', {
      class: 'ld-row',
      role: 'row',
      'data-key': this._keyOf(row),
      'data-index': String(index),
      tabindex: index === 0 ? '0' : '-1',
      'aria-selected': 'false',
      ...this._rowHandlers(row, index),
    });
    if (this.props.selectable) node.appendChild(this._checkCell(row, index));
    for (const col of this.props.columns || []) {
      const td = el('td', { class: `ld-col${col.class ? ` ${col.class}` : ''}`, 'data-label': col.title || '' });
      appendCell(td, col.render ? col.render(row, index) : row[col.key]);
      node.appendChild(td);
    }
    if (this.props.rowMenu) node.appendChild(this._actCell(row, index));
    return node;
  }

  _renderCard(row, index) {
    const node = el('article', {
      class: 'ld-card ld-fcard',
      role: 'listitem',
      'data-key': this._keyOf(row),
      'data-index': String(index),
      tabindex: index === 0 ? '0' : '-1',
      'aria-selected': 'false',
      ...this._rowHandlers(row, index),
    });
    if (this.props.selectable) node.appendChild(this._checkCell(row, index, true));

    const nameBox = el('div', { class: 'ld-fcard__name' });
    const meta = el('div', { class: 'ld-fcard__row' });
    for (const col of this.props.columns || []) {
      const content = col.render ? col.render(row, index) : row[col.key];
      // 身份列（文件名 / 账号名）做卡片标题：不加「账号：」前缀，字号更大。
      if (col.key === 'name' || col.cardHead) {
        appendCell(nameBox, content);
        continue;
      }
      // 每个字段带上列名：窄屏卡片里只有值没有标签，会分不清「120 KiB」是大小还是流量。
      // 宽屏（桌面）卡片视图里标签由 CSS 隐藏，保持原来的清爽排布。
      const value = el('span', { class: 'ld-fcard__field-value' });
      appendCell(value, content);
      meta.appendChild(el('span', {
        class: col.cardWide ? 'ld-fcard__field ld-fcard__field--wide' : 'ld-fcard__field',
      },
      el('span', { class: 'ld-fcard__field-label', text: col.title || '' }),
      value));
    }
    node.appendChild(el('div', { class: 'ld-fcard__main' }, nameBox, meta));
    if (this.props.rowMenu) node.appendChild(this._actButton(row, index));
    return node;
  }

  _checkBox(row, index) {
    const selected = this.props.selected;
    const checkbox = el('input', {
      type: 'checkbox',
      tabindex: '-1',
      'aria-label': `选择 ${row.name || this._keyOf(row)}`,
    });
    checkbox.checked = selected instanceof Set ? selected.has(this._keyOf(row)) : false;
    checkbox.addEventListener('click', (event) => {
      event.stopPropagation();
      this.emit('ld-select', {
        row,
        index,
        checked: checkbox.checked,
        shift: event.shiftKey,
        anchor: this._anchor,
      });
      this._anchor = index;
    });
    return checkbox;
  }

  _checkCell(row, index, plain) {
    const checkbox = this._checkBox(row, index);
    return plain
      ? el('span', { class: 'ld-col-check' }, checkbox)
      : el('td', { class: 'ld-col ld-col-check' }, checkbox);
  }

  _actButton(row, index) {
    return el('button', {
      class: 'ld-btn ld-btn--ghost ld-btn--icon ld-btn--sm ld-row-menu-btn',
      type: 'button',
      title: this.props.menuLabel || '更多操作',
      'aria-label': this.props.menuLabel || '更多操作',
      'aria-haspopup': 'menu',
      onclick: (event) => {
        event.stopPropagation();
        this.emit('ld-row-menu', { row, index, target: event.currentTarget });
      },
    }, el('ld-icon', { name: 'more' }));
  }

  _actCell(row, index) {
    return el('td', { class: 'ld-col ld-col-act' }, this._actButton(row, index));
  }

  /* ----- 分批渲染 ----- */

  _appendBatch(count) {
    if (!this._builtBody) return;
    const end = Math.min(this._rows.length, this._shown + Math.max(1, count));
    const cards = this._effectiveCards();
    for (let i = this._shown; i < end; i += 1) {
      const node = cards ? this._renderCard(this._rows[i], i) : this._renderRow(this._rows[i], i);
      this._bodyEl.insertBefore(node, this._sentinel);
      this._rowNodes.push(node);
    }
    this._shown = end;
    if (this._sentinel) {
      const remain = this._rows.length - this._shown;
      const text = remain > 0 ? `已显示 ${this._shown} / ${this._rows.length} 项，继续滚动加载…` : '';
      if (this._moreCell) this._moreCell.textContent = text;
      else this._sentinel.textContent = text;
      this._sentinel.classList.toggle('ld-hidden', remain <= 0);
    }
    this._paintSelection();
  }

  /* ----- 选中态 ----- */

  _paintSelection() {
    const selected = this.props.selected instanceof Set ? this.props.selected : null;
    for (const node of this._rowNodes) {
      if (!node.isConnected) continue;
      const on = !!selected && selected.has(node.dataset.key);
      node.classList.toggle('is-selected', on);
      node.setAttribute('aria-selected', String(on));
      const checkbox = node.querySelector('input[type="checkbox"]');
      if (checkbox) checkbox.checked = on;
    }
    if (this._headCheck) {
      const total = this._rows.length;
      let count = 0;
      if (selected) {
        for (const row of this._rows) if (selected.has(this._keyOf(row))) count += 1;
      }
      this._headCheck.checked = total > 0 && count === total;
      this._headCheck.indeterminate = count > 0 && count < total;
    }
  }

  /** 只刷新选中态（不重建行）。 */
  setSelected(selected) {
    this.props.selected = selected;
    this._paintSelection();
    return this;
  }

  /** 找到指定索引的行并聚焦（必要时继续追加批次）。 */
  focusIndex(index) {
    if (!this._rows.length) return;
    const target = Math.max(0, Math.min(this._rows.length - 1, index));
    while (this._shown <= target && this._shown < this._rows.length) {
      this._appendBatch(Math.max(this.props.batch || 60, target - this._shown + 1));
    }
    const node = this._rowNodes[target];
    if (node) {
      for (const other of this._rowNodes) if (other.isConnected) other.tabIndex = -1;
      node.tabIndex = 0;
      node.focus();
      node.scrollIntoView({ block: 'nearest' });
    }
  }

  onMount() {
    this.addEventListener('keydown', this._keyHandler);
    // autoCards 的表在旋转屏幕 / 拖动窗口宽度时需要重建（表格 ⇄ 卡片）
    if (this.props.autoCards) {
      this._offNarrow = onNarrowChange(() => {
        if (this._lastCards === this._effectiveCards()) return;
        this._builtBody = false;
        this.update();
      });
    }
  }

  onUnmount() {
    this.removeEventListener('keydown', this._keyHandler);
    if (this._observer) this._observer.disconnect();
    if (this._offNarrow) this._offNarrow();
  }

  _onKeyDown(event) {
    const row = event.target.closest ? event.target.closest('.ld-row, .ld-fcard') : null;
    if (!row) return;
    const index = Number(row.dataset.index);
    const record = this._rows[index];
    if (!record) return;
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        this.focusIndex(index + 1);
        break;
      case 'ArrowUp':
        event.preventDefault();
        this.focusIndex(index - 1);
        break;
      case 'Home':
        event.preventDefault();
        this.focusIndex(0);
        break;
      case 'End':
        event.preventDefault();
        this.focusIndex(this._rows.length - 1);
        break;
      case 'Enter':
        event.preventDefault();
        this.emit('ld-row-open', { row: record, index });
        break;
      case ' ':
        if (this.props.selectable) {
          event.preventDefault();
          this.emit('ld-select', { row: record, index, checked: !(this.props.selected instanceof Set && this.props.selected.has(this._keyOf(record))), shift: event.shiftKey, anchor: this._anchor });
          this._anchor = index;
        }
        break;
      case 'ContextMenu':
        event.preventDefault();
        this.emit('ld-row-menu', { row: record, index, target: row.querySelector('.ld-row-menu-btn') || row, x: event.clientX, y: event.clientY });
        break;
      default:
        break;
    }
  }
}

defineProps(LdDataTable, ['columns', 'rows', 'rowKey', 'selectable', 'selected', 'sort', 'batch', 'cards', 'loading', 'density', 'rowMenu', 'menuLabel', 'emptyIcon', 'emptyTitle', 'emptyDesc', 'ariaLabel', 'maxHeight', 'clickable', 'autoCards']);

function appendCell(td, content) {
  if (content === null || content === undefined || content === false) return;
  if (Array.isArray(content)) {
    for (const item of content) appendCell(td, item);
    return;
  }
  if (content instanceof Node) td.appendChild(content);
  else td.appendChild(document.createTextNode(String(content)));
}

/* ===== §6 共享服务 ======================================================== */

/* ---------- 上传管理：小文件直传 + 大文件分片续传 ---------- */

/**
 * UploadManager
 * - ≤ simpleLimit（默认 4 MiB）走 POST /api/fs/upload（multipart）
 * - 更大的文件走 /api/upload/init → PUT 分片 → /complete，会话由服务端复用，
 *   因此刷新页面后重新选择同一个文件即可续传。
 * - 并发：最多 concurrency（默认 3）个文件同时传输，每个文件的分片按序发送。
 * - 管理员代操作时 requirePassword=true：**每一批**新上传都会重新询问一次密码，
 *   密码只留在内存中的 item.secret 上，绝不写入 localStorage。
 */
export class UploadManager extends EventTarget {
  constructor(opts = {}) {
    super();
    this.user = opts.user || null;
    this.getPassword = opts.getPassword || (async () => null);
    this.requirePassword = !!opts.requirePassword;
    this.chunkSize = opts.chunkSize || 8 << 20;
    this.simpleLimit = opts.simpleLimit || 4 << 20;
    this.concurrency = opts.concurrency || 3;
    this.dir = '';
    this.items = [];
    this.sessions = [];
    this.sessionError = '';
    this._seq = 0;
    this._active = 0;
    this._queue = [];
  }

  setUser(user) {
    this.user = user || null;
    return this;
  }

  setPasswordProvider(fn) {
    this.getPassword = fn || (async () => null);
    return this;
  }

  setDir(dir) {
    this.dir = dir || '';
    return this;
  }

  /** 加入一批文件（同一批只询问一次管理员密码）。 */
  add(files, dir = this.dir) {
    const list = Array.from(files || []).filter((file) => file && typeof file.size === 'number');
    if (!list.length) return [];
    const secretPromise = this.requirePassword
      ? Promise.resolve(this.getPassword()).catch(() => null)
      : Promise.resolve('');
    const added = [];
    for (const file of list) {
      this._seq += 1;
      const item = {
        id: `u${this._seq}`,
        file,
        name: file.name,
        size: file.size,
        dir: dir || '',
        status: 'queued',
        loaded: 0,
        speed: 0,
        eta: 0,
        error: '',
        sessionId: null,
        chunkCount: 0,
        chunkSize: this.chunkSize,
        received: new Set(),
        resumed: false,
        abort: null,
        secret: null,
        secretPromise,
        lastTime: 0,
        lastBytes: 0,
      };
      this.items.push(item);
      this._queue.push(item.id);
      added.push(item);
    }
    this._changed();
    this._pump();
    return added;
  }

  find(id) {
    return this.items.find((item) => item.id === id) || null;
  }

  stats() {
    const stats = { total: this.items.length, done: 0, running: 0, failed: 0, paused: 0, bytes: 0, loaded: 0 };
    for (const item of this.items) {
      stats.bytes += item.size || 0;
      stats.loaded += item.loaded || 0;
      if (item.status === 'done') stats.done += 1;
      else if (item.status === 'running' || item.status === 'queued') stats.running += 1;
      else if (item.status === 'error') stats.failed += 1;
      else if (item.status === 'paused') stats.paused += 1;
    }
    return stats;
  }

  hasWork() {
    return this.items.some((item) => item.status === 'running' || item.status === 'queued' || item.status === 'paused');
  }

  activeCount() {
    return this.items.filter((item) => item.status === 'running' || item.status === 'queued').length;
  }

  pause(id) {
    const item = this.find(id);
    if (!item) return;
    if (item.status === 'running' || item.status === 'queued') {
      item.status = 'paused';
      if (item.abort) item.abort.abort();
    }
    this._changed();
  }

  resume(id) {
    const item = this.find(id);
    if (!item) return;
    if (item.status !== 'paused' && item.status !== 'error') return;
    item.error = '';
    item.status = 'queued';
    if (!this._queue.includes(item.id)) this._queue.push(item.id);
    this._changed();
    this._pump();
  }

  /** 取消：中断当前请求并尽量删掉服务端会话。 */
  async cancel(id) {
    const item = this.find(id);
    if (!item) return;
    item.status = 'canceled';
    if (item.abort) item.abort.abort();
    if (item.sessionId) {
      const sessionId = item.sessionId;
      item.sessionId = null;
      const password = await this._secretFor(item, { allowPrompt: true });
      try {
        await api(`/api/upload/${encodeURIComponent(sessionId)}`, {
          method: 'DELETE',
          body: {},
          password: password || undefined,
          user: this.user,
        });
      } catch {
        /* 会话可能已过期，忽略 */
      }
    }
    this._changed();
  }

  dismiss(id) {
    this.items = this.items.filter((item) => item.id !== id);
    this._queue = this._queue.filter((entry) => entry !== id);
    this._changed();
  }

  clearFinished() {
    this.items = this.items.filter((item) => item.status !== 'done' && item.status !== 'canceled');
    this._changed();
  }

  cancelAll() {
    for (const item of this.items.slice()) {
      if (item.status === 'running' || item.status === 'queued' || item.status === 'paused') this.cancel(item.id);
    }
  }

  /** 列出服务端未完成的上传会话（不含本地正在进行的）。 */
  async refreshSessions() {
    try {
      const data = await api(withQuery('/api/upload', { user: this.user }));
      const list = Array.isArray(data) ? data : (data && data.sessions) || [];
      const localIds = new Set(this.items.map((item) => item.sessionId).filter(Boolean));
      this.sessions = list.filter((session) => !localIds.has(session.id));
      this.sessionError = '';
    } catch (err) {
      this.sessions = [];
      this.sessionError = (err && err.message) || '读取未完成上传失败';
    }
    this._changed();
  }

  async cancelSession(id) {
    const password = await this._secretFor(null);
    try {
      await api(`/api/upload/${encodeURIComponent(id)}`, {
        method: 'DELETE',
        body: {},
        password: password || undefined,
        user: this.user,
      });
      toastSuccess('已放弃该上传会话');
    } catch (err) {
      toastApiError(err, '放弃上传会话失败');
    }
    await this.refreshSessions();
  }

  /* ----- 内部 ----- */

  _changed() {
    this.dispatchEvent(new CustomEvent('change', { detail: { items: this.items, sessions: this.sessions } }));
  }

  _pump() {
    while (this._active < this.concurrency && this._queue.length) {
      const id = this._queue.shift();
      const item = this.find(id);
      if (!item || item.status !== 'queued') continue;
      this._active += 1;
      this._runItem(item).finally(() => {
        this._active -= 1;
        this._changed();
        this._pump();
      });
    }
  }

  async _runItem(item) {
    item.status = 'running';
    item.error = '';
    item.lastTime = Date.now();
    item.lastBytes = item.loaded;
    this._changed();
    try {
      if (item.size <= this.simpleLimit) await this._uploadSimple(item);
      else await this._uploadChunked(item);
    } catch (err) {
      if (item.status === 'paused' || item.status === 'canceled' || (err && err.name === 'AbortError')) return;
      item.status = 'error';
      item.error = (err && err.message) || '上传失败';
      if (err && (err.code === 'quota_exceeded' || err.code === 'too_large')) {
        item.error = `${err.message}（该文件未上传）`;
      }
    }
    this._changed();
  }

  async _uploadSimple(item) {
    const password = await this._secretFor(item);
    if (password === null) {
      item.status = 'canceled';
      item.error = '已取消（未输入管理员密码）';
      return;
    }
    const body = new FormData();
    body.append('file', item.file, item.name);
    item.abort = new AbortController();
    const result = await api(withQuery('/api/fs/upload', { path: item.dir, user: this.user }), {
      method: 'POST',
      body,
      password: password || undefined,
      signal: item.abort.signal,
    });
    item.abort = null;
    item.loaded = item.size;
    item.status = 'done';
    item.result = result || null;
  }

  async _uploadChunked(item) {
    const password = await this._secretFor(item);
    if (password === null) {
      item.status = 'canceled';
      item.error = '已取消（未输入管理员密码）';
      return;
    }
    const init = await api('/api/upload/init', {
      method: 'POST',
      body: { dir: item.dir || '', name: item.name, size: item.size, chunkSize: this.chunkSize },
      password: password || undefined,
      user: this.user,
    });
    item.sessionId = init.id;
    item.chunkSize = init.chunkSize || this.chunkSize;
    item.chunkCount = init.chunkCount || Math.max(1, Math.ceil(item.size / item.chunkSize));
    item.received = new Set(init.received || []);
    item.resumed = item.received.size > 0;
    item.loaded = 0;
    for (const index of item.received) item.loaded += this._chunkLength(item, index);
    this._changed();

    for (let index = 0; index < item.chunkCount; index += 1) {
      if (item.status !== 'running') return;
      if (item.received.has(index)) continue;
      const start = index * item.chunkSize;
      const end = Math.min(item.size, start + item.chunkSize);
      const blob = item.file.slice(start, end);
      item.abort = new AbortController();
      try {
        await api(`/api/upload/${encodeURIComponent(item.sessionId)}/${index}`, {
          method: 'PUT',
          body: blob,
          password: password || undefined,
          user: this.user,
          signal: item.abort.signal,
        });
      } finally {
        item.abort = null;
      }
      item.received.add(index);
      item.loaded += end - start;
      this._tick(item);
    }

    if (item.status !== 'running') return;
    const done = await api(`/api/upload/${encodeURIComponent(item.sessionId)}/complete`, {
      method: 'POST',
      body: {},
      password: password || undefined,
      user: this.user,
    });
    item.status = 'done';
    item.loaded = item.size;
    item.result = done || null;
    if (done && done.name && done.name !== item.name) item.renamedTo = done.name;
  }

  _chunkLength(item, index) {
    const size = item.chunkSize || this.chunkSize;
    const start = index * size;
    return Math.max(0, Math.min(item.size, start + size) - start);
  }

  _tick(item) {
    const now = Date.now();
    const dt = (now - item.lastTime) / 1000;
    if (dt >= 0.4) {
      const instant = (item.loaded - item.lastBytes) / dt;
      item.speed = item.speed > 0 ? (item.speed * 0.6 + instant * 0.4) : instant;
      item.lastBytes = item.loaded;
      item.lastTime = now;
      item.eta = item.speed > 0 ? (item.size - item.loaded) / item.speed : 0;
      this._changed();
    }
  }

  /** 取本批密码；返回 null 表示用户放弃输入。 */
  async _secretFor(item, { allowPrompt = false } = {}) {
    if (!this.requirePassword) return '';
    if (item && item.secret) return item.secret;
    if (item && item.secretPromise) {
      const value = await item.secretPromise;
      if (value === null || value === undefined) return null;
      item.secret = String(value);
      return item.secret;
    }
    if (!allowPrompt) return null;
    const value = await this.getPassword();
    if (value === null || value === undefined) return null;
    return String(value);
  }
}

/* ---------- 文件管理器（用户端与管理员端复用） ---------- */

/**
 * FileBrowser 负责一屏"当前目录"的读取、排序、筛选、多选、拖拽上传与各类写操作。
 * opts:
 *   user            管理员代操作的目标账号（普通用户为 null）
 *   isAdmin         当前登录者是否为管理员
 *   permissions     当前账号的权限集合
 *   requirePassword 写操作前是否需要管理员二次密码
 *   uploads         UploadManager 实例（可选）
 *   showQuota       是否在底部显示配额条
 */
export class FileBrowser extends EventTarget {
  constructor(host, opts = {}) {
    super();
    this.host = host;
    this.user = opts.user || null;
    this.isAdmin = !!opts.isAdmin;
    this.requirePassword = !!opts.requirePassword;
    this.permissions = opts.permissions || [];
    this.uploads = opts.uploads || null;
    this.rootLabel = opts.rootLabel || '根目录';
    this.showQuota = opts.showQuota !== false;

    this.path = opts.path || '';
    this.entries = [];
    this.usage = null;
    this.loading = false;
    this.error = '';
    this.filter = '';
    this.selected = new Set();
    this.sortKey = opts.sortKey || 'name';
    this.sortDir = opts.sortDir || 'asc';
    this.cards = opts.cards !== undefined ? !!opts.cards : isNarrow();
    this.density = opts.density || 'comfortable';
    this._dragDepth = 0;
    this._mounted = false;
    this._columns = this._buildColumns();
    // 已处理过的上传条目 id（避免同一条目反复触发刷新）与合并刷新的定时器
    this._uploaded = new Set();
    this._refreshTimer = null;

    this._onDragEnter = this._onDragEnter.bind(this);
    this._onDragOver = this._onDragOver.bind(this);
    this._onDragLeave = this._onDragLeave.bind(this);
    this._onDrop = this._onDrop.bind(this);
  }

  /* ----- 权限 ----- */

  can(permission) {
    if (this.user) return true;
    if (this.isAdmin) return true;
    return this.permissions.includes(permission);
  }

  /* ----- 生命周期 ----- */

  mount() {
    if (this._mounted) return this;
    this._mounted = true;

    this._fileInput = el('input', {
      type: 'file',
      multiple: true,
      class: 'ld-sr',
      tabindex: '-1',
      'aria-hidden': 'true',
      onchange: (event) => {
        const files = Array.from(event.target.files || []);
        event.target.value = '';
        this.upload(files);
      },
    });

    this._dropOverlay = el('div', { class: 'ld-drop', 'aria-hidden': 'true' },
      el('div', { class: 'ld-drop__inner' },
        el('ld-icon', { name: 'upload-cloud', size: 'xxl' }),
        el('div', { class: 'ld-drop__title', text: '松开鼠标开始上传' }),
        el('div', { class: 'ld-drop__sub' })));
    this._dropDesc = this._dropOverlay.querySelector('.ld-drop__sub');

    // clickable：单击整行就打开（文件夹进入 / 文件预览），悬停显示手型指针。
    this.table = el('ld-data-table', { selectable: true, rowMenu: true, clickable: true, ariaLabel: '文件列表' });
    this.table.addEventListener('ld-sort', (event) => this._onSort(event.detail));
    this.table.addEventListener('ld-select', (event) => this._onSelect(event.detail));
    this.table.addEventListener('ld-select-all', (event) => this._onSelectAll(event.detail));
    this.table.addEventListener('ld-row-open', (event) => this._open(event.detail.row));
    this.table.addEventListener('ld-row-menu', (event) => this._rowMenu(event.detail));
    this.table.addEventListener('ld-row-click', (event) => this._onRowClick(event.detail));

    this.crumbs = el('ld-breadcrumb', { rootLabel: this.rootLabel });
    this.crumbs.addEventListener('ld-navigate', (event) => this.load(event.detail.path));

    // 返回上一级：根目录时禁用，避免“点了没反应”。
    this._upBtn = el('ld-button', {
      icon: 'arrow-up',
      variant: 'soft',
      size: 'sm',
      title: '返回上一级',
      'aria-label': '返回上一级',
      onClick: () => this.goUp(),
    });

    this.quota = el('ld-quota-bar');
    this.body = el('div', { class: 'ld-files__body' }, this.table);
    if (this.showQuota) this.body.appendChild(el('div', { class: 'ld-quota-wrap' }, this.quota));

    this.toolbar = el('div', { class: 'ld-toolbar ld-files__toolbar' });
    this.selbar = el('div', { class: 'ld-selbar ld-hidden', role: 'region', 'aria-label': '选中项操作' });
    this._buildToolbar();

    this.root = el('div', { class: 'ld-files' },
      el('div', { class: 'ld-files__top' }, this._upBtn, this.crumbs),
      this.toolbar,
      this.selbar,
      this.body,
      this._fileInput,
      this._dropOverlay);

    clearNode(this.host);
    this.host.appendChild(this.root);

    window.addEventListener('dragenter', this._onDragEnter, true);
    window.addEventListener('dragover', this._onDragOver, true);
    window.addEventListener('dragleave', this._onDragLeave, true);
    window.addEventListener('drop', this._onDrop, true);

    // 上传队列每次变化都刷新工具栏统计，并检查是否有新文件已上传完成
    this._onUploadChange = () => {
      this._paintToolbar();
      this._syncUploads();
    };
    if (this.uploads) this.uploads.addEventListener('change', this._onUploadChange);

    this._paintToolbar();
    return this;
  }

  destroy() {
    if (this._refreshTimer) {
      clearTimeout(this._refreshTimer);
      this._refreshTimer = null;
    }
    window.removeEventListener('dragenter', this._onDragEnter, true);
    window.removeEventListener('dragover', this._onDragOver, true);
    window.removeEventListener('dragleave', this._onDragLeave, true);
    window.removeEventListener('drop', this._onDrop, true);
    if (this.uploads && this._onUploadChange) this.uploads.removeEventListener('change', this._onUploadChange);
    if (this.root) this.root.remove();
    this._mounted = false;
  }

  /** 切换管理员代操作的目标账号。 */
  setUser(user, { reload = true } = {}) {
    this.user = user || null;
    if (this.uploads) this.uploads.setUser(this.user);
    if (reload) return this.load('');
    return Promise.resolve(this.entries);
  }

  /* ----- 数据 ----- */

  async load(path = this.path) {
    this.path = typeof path === 'string' ? path : this.path;
    this.loading = true;
    this.error = '';
    this.selected = new Set();
    this._paintToolbar();
    this._paintTable();
    await this._fetchList();
    this.loading = false;
    this._afterListChange();
    return this.entries;
  }

  /**
   * 只拉目录数据并回填字段，不碰 loading / 选中态。
   * silent=true 时失败不清空列表（用于上传后的后台静默刷新）。
   */
  async _fetchList(silent = false) {
    try {
      const data = await api(withQuery('/api/fs/list', { path: this.path, user: this.user }));
      this.path = data && data.path ? data.path : '';
      this.entries = data && Array.isArray(data.entries) ? data.entries : [];
      this.usage = (data && data.usage) || null;
      this.error = '';
    } catch (err) {
      if (!silent) {
        this.entries = [];
        this.error = (err && err.message) || '目录读取失败';
      }
    }
  }

  _afterListChange() {
    if (this.uploads && this.uploads.setDir) this.uploads.setDir(this.path);
    this._paintToolbar();
    this._paintTable();
    this._paintSelbar();
    if (this.usage) {
      this.quota.used = this.usage.used || 0;
      this.quota.trashUsed = this.usage.trashUsed || 0;
      this.quota.quota = this.usage.quota || 0;
    }
    this.dispatchEvent(new CustomEvent('ld-usage', { detail: { path: this.path, usage: this.usage } }));
  }

  /**
   * 上传成功后的静默刷新：不显示加载骨架、不清空选中项，
   * 新文件直接出现在列表里（选中项只保留仍然存在的那些）。
   */
  async refreshQuiet() {
    if (this.loading) return this.entries;
    const prevSelected = new Set(this.selected);
    await this._fetchList(true);
    const alive = new Set(this.entries.map((entry) => entry.path));
    this.selected = new Set([...prevSelected].filter((item) => alive.has(item)));
    this._afterListChange();
    return this.entries;
  }

  /**
   * 扫描上传队列，发现“已完成且落在当前目录”的新条目就安排一次静默刷新。
   * 同一批多个文件几乎同时完成，靠防抖合并成一次请求。
   */
  _syncUploads() {
    if (!this.uploads) return;
    const items = this.uploads.items || [];
    let dirty = false;
    for (const item of items) {
      if (item.status !== 'done') continue;
      if (this._uploaded.has(item.id)) continue;
      this._uploaded.add(item.id);
      if ((item.dir || '') === (this.path || '')) dirty = true;
    }
    if (!dirty) return;
    if (this._refreshTimer) clearTimeout(this._refreshTimer);
    this._refreshTimer = setTimeout(() => {
      this._refreshTimer = null;
      this.refreshQuiet();
    }, 320);
  }

  refresh() {
    return this.load(this.path);
  }

  /** 回到上一级目录；已在根目录时无动作。 */
  goUp() {
    if (!this.path) return Promise.resolve(this.entries);
    return this.load(parentPath(this.path));
  }

  _displayEntries() {
    let list = this.entries;
    const keyword = this.filter.trim().toLowerCase();
    if (keyword) list = list.filter((entry) => String(entry.name || '').toLowerCase().includes(keyword));
    return sortEntries(list, this.sortKey, this.sortDir);
  }

  selectedRows() {
    return this.entries.filter((entry) => this.selected.has(entry.path));
  }

  /* ----- 工具栏 ----- */

  _buildToolbar() {
    clearNode(this.toolbar);

    this._filterInput = el('input', {
      class: 'ld-input ld-input--search',
      type: 'search',
      placeholder: '在当前文件夹中筛选',
      'aria-label': '在当前文件夹中筛选',
      oninput: debounce(() => {
        this.filter = this._filterInput.value;
        this._paintTable();
      }, 180),
    });

    this._sortSelect = el('select', {
      class: 'ld-select ld-select--sm',
      'aria-label': '排序字段',
      onchange: () => {
        this.sortKey = this._sortSelect.value;
        this._paintTable();
      },
    },
      el('option', { value: 'name', text: '名称' }),
      el('option', { value: 'size', text: '大小' }),
      el('option', { value: 'modTime', text: '修改时间' }),
      el('option', { value: 'kind', text: '类型' }));
    this._sortSelect.value = this.sortKey;

    this._dirBtn = el('ld-button', {
      icon: 'arrow-up',
      variant: 'ghost',
      size: 'sm',
      title: '切换排序方向',
      'aria-label': '切换排序方向',
      onClick: () => {
        this.sortDir = this.sortDir === 'asc' ? 'desc' : 'asc';
        this._paintToolbar();
        this._paintTable();
      },
    });

    this._densityBtn = el('ld-button', {
      icon: 'list',
      variant: 'ghost',
      size: 'sm',
      title: '切换行高',
      'aria-label': '切换行高',
      onClick: () => {
        this.density = this.density === 'comfortable' ? 'compact' : 'comfortable';
        this._paintToolbar();
        this._paintTable();
      },
    });

    this._viewBtn = el('ld-button', {
      icon: 'grid',
      variant: 'ghost',
      size: 'sm',
      title: '切换列表 / 卡片视图',
      'aria-label': '切换视图',
      onClick: () => {
        this.cards = !this.cards;
        this._paintToolbar();
        this._paintTable();
      },
    });

    this._refreshBtn = el('ld-button', {
      icon: 'refresh',
      variant: 'ghost',
      size: 'sm',
      title: '刷新',
      'aria-label': '刷新当前目录',
      onClick: () => this.refresh(),
    });

    this._mkdirBtn = el('ld-button', {
      icon: 'folder-plus',
      label: '新建文件夹',
      variant: 'soft',
      size: 'sm',
      class: 'ld-toolbar__mkdir',
      onClick: () => this.createFolder(),
    });

    this._uploadBtn = el('ld-button', {
      icon: 'upload',
      label: '上传',
      variant: 'primary',
      size: 'sm',
      class: 'ld-toolbar__upload',
      onClick: () => this.pickFiles(),
    });

    this.toolbar.append(
      el('div', { class: 'ld-toolbar__row ld-toolbar__row--browse' },
        el('div', { class: 'ld-toolbar__group ld-toolbar__group--search' }, this._filterInput),
        el('div', { class: 'ld-toolbar__group' },
          this._sortSelect, this._dirBtn, this._densityBtn, this._viewBtn, this._refreshBtn)),
      el('div', { class: 'ld-toolbar__row ld-toolbar__row--actions' },
        el('div', { class: 'ld-toolbar__group' },
          this._mkdirBtn, this._uploadBtn)));
  }

  _paintToolbar() {
    this._dirBtn.setAttribute('icon', this.sortDir === 'asc' ? 'arrow-up' : 'arrow-down');
    this._densityBtn.setAttribute('icon', this.density === 'comfortable' ? 'list' : 'grid');
    this._viewBtn.setAttribute('icon', this.cards ? 'list' : 'grid');
    this._viewBtn.title = this.cards ? '切换为列表视图' : '切换为卡片视图';
    this._mkdirBtn.toggleAttribute('disabled', !(this.can('modify') || this.can('upload')));
    this._uploadBtn.toggleAttribute('disabled', !this.can('upload'));
    this._refreshBtn.toggleAttribute('disabled', this.loading);
    if (this._dropDesc) {
      this._dropDesc.textContent = `上传到 /${this.path || ''}${this.can('upload') ? '' : '（当前账号没有上传权限）'}`;
    }
  }

  _paintSelbar() {
    clearNode(this.selbar);
    const rows = this.selectedRows();
    if (!rows.length) {
      this.selbar.classList.add('ld-hidden');
      return;
    }
    this.selbar.classList.remove('ld-hidden');
    const hasFile = rows.some((row) => !row.isDir);
    this.selbar.append(
      el('span', { class: 'ld-selbar__count', text: `已选中 ${rows.length} 项` }),
      el('ld-button', {
        label: '下载', icon: 'download', size: 'sm', variant: 'soft',
        disabled: !hasFile,
        title: hasFile ? '下载选中的文件' : '文件夹无法直接下载',
        onClick: () => this.download(rows),
      }),
      el('ld-button', {
        label: '移动', icon: 'move', size: 'sm', variant: 'soft',
        disabled: !this.can('modify'),
        onClick: () => this.moveOrCopy('move', rows),
      }),
      el('ld-button', {
        label: '复制', icon: 'copy', size: 'sm', variant: 'soft',
        disabled: !this.can('modify'),
        onClick: () => this.moveOrCopy('copy', rows),
      }),
      el('ld-button', {
        label: '删除', icon: 'trash', size: 'sm', variant: 'ghost-danger',
        disabled: !this.can('delete'),
        onClick: () => this.remove(rows),
      }),
      el('span', { class: 'ld-toolbar__spacer' }),
      el('ld-button', {
        label: '取消选择', variant: 'ghost', size: 'sm',
        onClick: () => this._setSelection(new Set()),
      }));
  }

  _paintTable() {
    const rows = this._displayEntries();
    // 面包屑必须跟着当前路径刷新：否则永远停在“根目录”那一节。
    this.crumbs.items = pathCrumbs(this.path, this.rootLabel);
    // LdButton 没有 defineProps，直接改 .disabled 不会触发重绘，必须走 attribute。
    if (this.path) this._upBtn.removeAttribute('disabled');
    else this._upBtn.setAttribute('disabled', '');
    this.table.columns = this._columns;
    this.table.rows = rows;
    this.table.cards = this.cards;
    this.table.density = this.density;
    this.table.loading = this.loading;
    this.table.selected = this.selected;
    this.table.sort = { key: this.sortKey, dir: this.sortDir };
    this.table.ariaLabel = `${this.path || '根目录'} 的文件列表`;
    if (this.error) {
      this.table.emptyIcon = 'alert';
      this.table.emptyTitle = '读取失败';
      this.table.emptyDesc = this.error;
    } else if (this.filter.trim() && !rows.length) {
      this.table.emptyIcon = 'search';
      this.table.emptyTitle = '没有匹配的项目';
      this.table.emptyDesc = `当前文件夹里没有名称包含“${this.filter.trim()}”的项目。`;
    } else {
      this.table.emptyIcon = 'folder';
      this.table.emptyTitle = this.path ? '这个文件夹是空的' : '还没有任何文件';
      this.table.emptyDesc = '把文件拖到页面上，或点击右上角“上传”按钮开始使用。';
    }
    this._paintSelbar();
  }

  _buildColumns() {
    return [
      { key: 'name', title: '名称', sortable: true, render: (row) => this._nameCell(row) },
      { key: 'size', title: '大小', sortable: true, width: '110px', class: 'ld-col-size', render: (row) => (row.isDir ? '—' : fmtBytes(row.size)) },
      { key: 'kind', title: '类型', sortable: true, width: '120px', class: 'ld-col-kind', render: (row) => kindLabel(row.kind) },
      { key: 'modTime', title: '修改时间', sortable: true, width: '170px', class: 'ld-col-time', render: (row) => fmtDate(row.modTime) },
    ];
  }

  _nameCell(row) {
    return el('div', { class: 'ld-cell-name' },
      el('ld-file-icon', { kind: row.isDir ? 'folder' : row.kind || 'other', size: 'md' }),
      el('span', { class: 'ld-cell-name__text ld-ellipsis', title: row.name, text: row.name }));
  }

  /* ----- 选择 ----- */

  _setSelection(next) {
    this.selected = next;
    this.table.setSelected(next);
    this._paintSelbar();
  }

  _onSelect({ row, index, checked, shift, anchor }) {
    const next = new Set(this.selected);
    if (shift && anchor >= 0 && anchor !== index) {
      const list = this._displayEntries();
      const from = Math.min(anchor, index);
      const to = Math.max(anchor, index);
      for (let i = from; i <= to; i += 1) {
        const item = list[i];
        if (!item) continue;
        if (checked) next.add(item.path);
        else next.delete(item.path);
      }
    } else if (checked) {
      next.add(row.path);
    } else {
      next.delete(row.path);
    }
    this._setSelection(next);
  }

  _onSelectAll({ checked }) {
    const rows = this._displayEntries();
    this._setSelection(checked ? new Set(rows.map((row) => row.path)) : new Set());
  }

  _onRowClick({ row, event }) {
    const modified = !!(event && (event.ctrlKey || event.metaKey || event.shiftKey));
    // 不带修饰键的单击直接打开（文件夹进入 / 文件预览），
    // 多选留给 Ctrl / Cmd / Shift 或左侧复选框。
    if (!modified) {
      this._open(row);
      return;
    }
    const next = new Set(this.selected);
    if (event.ctrlKey || event.metaKey) {
      if (next.has(row.path)) next.delete(row.path);
      else next.add(row.path);
    } else {
      next.add(row.path);
    }
    this._setSelection(next);
  }

  _onSort({ key, dir }) {
    this.sortKey = key;
    this.sortDir = dir;
    this._paintToolbar();
    this._paintTable();
  }

  /* ----- 打开 / 预览 ----- */

  async _open(row) {
    if (row.isDir) {
      await this.load(row.path);
      return;
    }
    await this.preview(row);
  }

  /** 打开预览；options.edit = true 时直接进入编辑模式。 */
  async preview(row, options = {}) {
    const editable = this.can('modify') && !row.isDir && row.kind === 'text';
    try {
      const data = editable && options.edit
        ? await fetchTextForEdit(row.path, { user: this.user })
        : await fetchPreview(row.path, { user: this.user });
      const dialog = document.createElement('ld-preview');
      document.body.appendChild(dialog);
      dialog.addEventListener('ld-download', (event) => this.download([event.detail.entry]));
      dialog.addEventListener('ld-close', () => dialog.remove());
      // 保存成功后刷新列表，让大小/修改时间立即更新。
      dialog.addEventListener('ld-saved', () => { this.refresh().catch(() => {}); });
      dialog.openPreview({
        entry: row,
        plan: data.plan,
        text: data.text,
        truncated: data.truncated,
        note: data.note,
        user: this.user,
        baseModTime: data.baseModTime || '',
        readOnly: !this.can('modify'),
        editing: !!options.edit,
      });
    } catch (err) {
      if (err && err.code === 'not_text') {
        toastError('该文件不是可编辑的文本类型');
        return;
      }
      if (err && err.code === 'text_too_large') {
        toastError(err.message || '文件过大，无法在线编辑');
        return;
      }
      toastApiError(err, '无法预览该文件');
    }
  }

  download(rows) {
    const files = rows.filter((row) => !row.isDir);
    if (!files.length) {
      toastInfo('文件夹无法直接下载，请进入文件夹后选择内容');
      return;
    }
    files.forEach((row, index) => {
      window.setTimeout(() => {
        const link = el('a', {
          href: fileUrl(row.path, { user: this.user, download: 1 }),
          download: row.name,
          class: 'ld-sr',
        });
        document.body.appendChild(link);
        link.click();
        link.remove();
      }, index * 300);
    });
    toastInfo(files.length > 1 ? `开始下载 ${files.length} 个文件` : `开始下载 ${files[0].name}`);
  }

  /* ----- 行内菜单 ----- */

  async _rowMenu({ row, target, x, y }) {
    const targets = this.selected.has(row.path) && this.selected.size > 1 ? this.selectedRows() : [row];
    const items = [];
    items.push(row.isDir
      ? { id: 'open', label: '打开', icon: 'folder-open' }
      : { id: 'preview', label: '预览', icon: 'eye' });
    // 文本类文件给一个直接进入编辑的入口。
    const canEditText = !row.isDir && row.kind === 'text' && this.can('modify');
    if (canEditText) items.push({ id: 'edit', label: '编辑文本', icon: 'edit' });
    if (!row.isDir) items.push({ id: 'download', label: '下载', icon: 'download' });
    items.push({ separator: true });
    if (this.can('modify')) {
      items.push({ id: 'rename', label: '重命名', icon: 'rename', disabled: targets.length > 1 });
      items.push({ id: 'move', label: '移动到…', icon: 'move' });
      items.push({ id: 'copy', label: '复制到…', icon: 'copy' });
    }
    if (this.can('delete')) {
      items.push({ id: 'delete', label: '移入回收站', icon: 'trash', danger: true });
    }
    items.push({ separator: true });
    items.push({ id: 'select', label: this.selected.has(row.path) ? '取消选中' : '只选中此项', icon: 'check' });
    items.push({ id: 'path', label: '复制路径', icon: 'link' });

    const choice = await showMenu({ items, target, x, y, header: row.name });
    if (!choice) return;
    if (choice === 'open') this.load(row.path);
    else if (choice === 'preview') this.preview(row);
    else if (choice === 'edit') this.preview(row, { edit: true });
    else if (choice === 'download') this.download(targets);
    else if (choice === 'rename') this.rename(row);
    else if (choice === 'move') this.moveOrCopy('move', targets);
    else if (choice === 'copy') this.moveOrCopy('copy', targets);
    else if (choice === 'delete') this.remove(targets);
    else if (choice === 'select') this._setSelection(new Set([row.path]));
    else if (choice === 'path') this.copyPath(row.path);
  }

  async copyPath(path) {
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(path);
      else throw new Error('clipboard unavailable');
      toastSuccess('路径已复制到剪贴板');
    } catch {
      toastInfo(`路径：${path}`);
    }
  }

  /* ----- 写操作 ----- */

  /** 写操作统一入口：管理员代操作时先要二次密码（每批一次）。 */
  async _runWrite(run, opts = {}) {
    if (!this.requirePassword) return run(null);
    return withAdminPassword(run, {
      title: '管理员二次验证',
      message: opts.message || `正在以管理员身份操作用户 ${this.user || ''} 的文件，请输入管理员密码确认。`,
      confirmText: opts.confirmText || '执行',
    });
  }

  pickFiles() {
    if (!this.can('upload')) {
      toastInfo('当前账号没有上传权限');
      return;
    }
    if (this._fileInput) this._fileInput.click();
  }

  upload(files) {
    const list = Array.from(files || []);
    if (!list.length) return;
    if (!this.can('upload')) {
      toastInfo('当前账号没有上传权限');
      return;
    }
    if (!this.uploads) {
      toastInfo('当前页面未启用上传');
      return;
    }
    this.uploads.setDir(this.path);
    this.uploads.add(list, this.path);
    this.dispatchEvent(new CustomEvent('ld-upload-open'));
  }

  async createFolder() {
    const name = await askText({
      title: '新建文件夹',
      label: '文件夹名称',
      placeholder: '例如：项目资料',
      required: true,
      confirmText: '创建',
    });
    if (name === null) return;
    const result = await this._runWrite((password) => api('/api/fs/mkdir', {
      method: 'POST',
      body: { dir: this.path, name },
      password,
      user: this.user,
    }), { confirmText: '创建' }).catch((err) => {
      toastApiError(err, '新建文件夹失败');
      return null;
    });
    if (!result) return;
    toastSuccess(`已创建文件夹 ${name}`);
    await this.load(this.path);
  }

  async rename(row) {
    const name = await askText({
      title: '重命名',
      label: '新名称',
      value: row.name,
      required: true,
      confirmText: '保存',
      hint: row.isDir ? '文件夹' : `当前文件：${row.name}`,
    });
    if (name === null || name === row.name) return;
    const result = await this._runWrite((password) => api('/api/fs/rename', {
      method: 'POST',
      body: { path: row.path, newName: name },
      password,
      user: this.user,
    }), { confirmText: '重命名' }).catch((err) => {
      toastApiError(err, '重命名失败');
      return null;
    });
    if (!result) return;
    toastSuccess(`已重命名为 ${name}`);
    await this.load(this.path);
  }

  async moveOrCopy(mode, rows) {
    const list = Array.from(rows || []);
    if (!list.length) return;
    const picker = el('ld-dir-picker', { rootLabel: this.rootLabel });
    picker.loader = (path) => api(withQuery('/api/fs/list', { path, user: this.user }));
    const dialog = document.createElement('ld-modal');
    dialog.title = mode === 'move' ? `移动到…（${list.length} 项）` : `复制到…（${list.length} 项）`;
    dialog.size = 'lg';
    dialog.append(picker);
    document.body.appendChild(dialog);
    dialog.setActions([
      { label: '取消', value: 'cancel' },
      { label: mode === 'move' ? '移动到这里' : '复制到这里', value: 'ok', variant: 'primary' },
    ]);
    dialog.open();
    picker.open(this.path);
    const confirmed = await new Promise((resolve) => {
      dialog.addEventListener('ld-action', (event) => resolve(event.detail.value === 'ok'));
      dialog.addEventListener('ld-close', () => resolve(false));
    });
    const dest = picker.current;
    dialog.remove();
    if (!confirmed) return;
    const endpoint = mode === 'move' ? '/api/fs/move' : '/api/fs/copy';
    const result = await this._runWrite((password) => api(endpoint, {
      method: 'POST',
      body: { paths: list.map((row) => row.path), dest },
      password,
      user: this.user,
    }), { confirmText: mode === 'move' ? '移动' : '复制' }).catch((err) => {
      toastApiError(err, mode === 'move' ? '移动失败' : '复制失败');
      return null;
    });
    if (!result) return;
    toastSuccess(`${mode === 'move' ? '已移动' : '已复制'} ${list.length} 项到 /${dest}`);
    this._setSelection(new Set());
    if (mode === 'move') await this.load(this.path);
    else await this.load(this.path);
  }

  async remove(rows) {
    const list = Array.from(rows || []);
    if (!list.length) return;
    const names = list.map((row) => row.name);
    const ok = await confirmDialog({
      title: '移入回收站',
      message: `确定要把选中的 ${list.length} 项移入回收站吗？之后仍可在回收站恢复。`,
      details: names.slice(0, 8).concat(names.length > 8 ? [`…以及另外 ${names.length - 8} 项`] : []),
      danger: true,
      confirmText: '移入回收站',
    });
    if (!ok) return;
    const result = await this._runWrite((password) => api('/api/fs/delete', {
      method: 'POST',
      body: { paths: list.map((row) => row.path) },
      password,
      user: this.user,
    }), { confirmText: '删除' }).catch((err) => {
      toastApiError(err, '删除失败');
      return null;
    });
    if (!result) return;
    toastSuccess(`已移入回收站 ${list.length} 项`);
    this._setSelection(new Set());
    await this.load(this.path);
  }

  /* ----- 拖拽 ----- */

  _hasFiles(event) {
    const types = event.dataTransfer ? Array.from(event.dataTransfer.types || []) : [];
    return types.includes('Files');
  }

  _showDrop(on) {
    if (!this._dropOverlay) return;
    this._dropOverlay.classList.toggle('is-on', !!on);
    this._dropOverlay.setAttribute('aria-hidden', on ? 'false' : 'true');
  }

  _onDragEnter(event) {
    if (!this._hasFiles(event)) return;
    event.preventDefault();
    this._dragDepth += 1;
    if (this.can('upload')) this._showDrop(true);
  }

  _onDragOver(event) {
    if (!this._hasFiles(event)) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = this.can('upload') ? 'copy' : 'none';
  }

  _onDragLeave(event) {
    if (!this._hasFiles(event)) return;
    this._dragDepth = Math.max(0, this._dragDepth - 1);
    if (this._dragDepth === 0) this._showDrop(false);
  }

  _onDrop(event) {
    if (!this._hasFiles(event)) return;
    event.preventDefault();
    this._dragDepth = 0;
    this._showDrop(false);
    if (!this.can('upload')) {
      toastInfo('当前账号没有上传权限');
      return;
    }
    const files = Array.from(event.dataTransfer ? event.dataTransfer.files : []);
    if (!files.length) {
      toastInfo('暂不支持拖入文件夹，请拖入具体文件，或进入该文件夹后再上传');
      return;
    }
    this.upload(files);
  }
}

/* ---------- 应用外壳（侧边导航 / 顶栏 / 移动端标签栏 / 悬浮上传按钮） ---------- */

export function mountShell(opts = {}) {
  const brandMark = el('span', { class: 'ld-brand__mark' }, el('ld-icon', { name: opts.brandIcon || 'disk', size: 'lg' }));
  const brandSub = el('div', { class: 'ld-brand__sub', text: opts.brandSub || '轻量文件托管' });
  const brand = el('div', { class: 'ld-brand' }, brandMark,
    el('div', { class: 'ld-brand__text' },
      el('div', { class: 'ld-brand__name', text: opts.brandName || 'HomeVault' }),
      brandSub));

  const navBox = el('nav', { class: 'ld-nav', 'aria-label': '主导航' });
  const footBox = el('div', { class: 'ld-side__foot' });
  const side = el('aside', { class: 'ld-side' }, brand, navBox, footBox);
  const topLeft = el('div', { class: 'ld-top__left' });
  const topRight = el('div', { class: 'ld-top__right' });
  const main = el('main', { class: 'ld-main', id: 'ld-main' });
  const shell = el('div', { class: 'ld-shell' }, side, el('div', { class: 'ld-main-wrap' },
    el('header', { class: 'ld-top' }, topLeft, topRight), main));
  const tabbar = el('nav', { class: 'ld-tabbar', 'aria-label': '主导航（移动端）' });
  const fab = el('button', {
    class: 'ld-fab',
    type: 'button',
    'aria-label': '上传文件',
    title: '上传文件',
  }, el('ld-icon', { name: 'upload', size: 'lg' }));
  fab.hidden = true;

  document.body.append(shell, tabbar, fab);

  return {
    shell, side, navBox, footBox, topLeft, topRight, main, tabbar, fab,

    /** 同时渲染桌面侧边导航与移动端标签栏。 */
    setNav(items, activeId, onSelect) {
      clearNode(navBox);
      clearNode(tabbar);
      for (const item of items) {
        if (!item || item.hidden) continue;
        const active = item.id === activeId;
        navBox.appendChild(el('button', {
          class: `ld-nav-item${active ? ' is-active' : ''}`,
          type: 'button',
          'aria-current': active ? 'page' : undefined,
          title: item.hint || item.label,
          onclick: () => onSelect(item.id),
        },
          el('ld-icon', { name: item.icon || 'file', size: 'md' }),
          el('span', { class: 'ld-nav-item__label', text: item.label }),
          item.badge ? el('span', { class: 'ld-nav-item__badge', text: String(item.badge) }) : null));

        tabbar.appendChild(el('button', {
          class: 'ld-tabbar__item',
          type: 'button',
          'aria-current': active ? 'page' : undefined,
          onclick: () => onSelect(item.id),
        },
          el('ld-icon', { name: item.icon || 'file', size: 'lg' }),
          el('span', { text: item.label })));
      }
    },

    setAccount(node) {
      clearNode(footBox);
      if (node) footBox.appendChild(node);
    },

    setBrandSub(text) {
      brandSub.textContent = text;
    },

    setTitle(text) {
      document.title = text || 'HomeVault';
    },
  };
}

/** 主题切换按钮（图标随主题变化）。 */
export function themeToggleButton() {
  const button = el('ld-button', {
    icon: theme.resolved() === 'dark' ? 'sun' : 'moon',
    variant: 'ghost',
    size: 'sm',
    title: '切换浅色 / 深色主题',
    'aria-label': '切换主题',
    onClick: () => theme.toggle(),
  });
  theme.onChange((value) => button.setAttribute('icon', value === 'dark' ? 'sun' : 'moon'));
  return button;
}

/** 侧边栏底部账号块（点击弹出菜单）。 */
export function buildAccountBlock(user, handlers = {}) {
  const name = (user && (user.displayName || user.username)) || '未登录';
  const initial = String(name).trim().slice(0, 1).toUpperCase() || '?';
  const sub = user && user.isAdmin
    ? '管理员'
    : (user && user.displayName ? user.username : '普通用户');

  const button = el('button', {
    class: 'ld-account',
    type: 'button',
    'aria-haspopup': 'menu',
    'aria-label': `账号菜单：${name}`,
    onclick: async () => {
      const dark = theme.resolved() === 'dark';
      const items = [];
      if (handlers.onSettings) items.push({ id: 'settings', label: '账号设置', icon: 'settings' });
      items.push({ id: 'theme', label: dark ? '切换为浅色主题' : '切换为深色主题', icon: dark ? 'sun' : 'moon' });
      items.push({ separator: true });
      items.push({ id: 'logout', label: '退出登录', icon: 'logout', danger: true });
      const choice = await showMenu({ items, target: button, align: 'end' });
      if (!choice) return;
      if (choice === 'theme') theme.toggle();
      else if (handlers.onSelect) handlers.onSelect(choice);
    },
  },
    el('span', { class: 'ld-avatar', 'aria-hidden': 'true', text: initial }),
    el('div', { class: 'ld-account__text' },
      el('div', { class: 'ld-account__name ld-ellipsis', text: name }),
      el('div', { class: 'ld-account__meta', text: sub })),
    el('ld-icon', { name: 'more', size: 'sm' }));
  return button;
}

/* ---------- 交互式输入对话框 ---------- */

/** askText({title,label,value,placeholder,required,hint,confirmText,validate}) → Promise<string|null> */
export function askText(opts = {}) {
  return new Promise((resolve) => {
    const input = el(opts.multiline ? 'textarea' : 'input', {
      class: opts.multiline ? 'ld-textarea' : 'ld-input',
      type: opts.multiline ? undefined : (opts.type || 'text'),
      value: opts.value || '',
      placeholder: opts.placeholder || '',
      autocomplete: 'off',
      spellcheck: 'false',
    });
    const field = el('ld-field', {
      label: opts.label || '内容',
      hint: opts.hint || '',
      required: opts.required ? true : undefined,
    }, input);

    const dialog = document.createElement('ld-modal');
    dialog.title = opts.title || '请输入';
    dialog.size = opts.size || 'sm';
    dialog.append(field);
    if (opts.description) dialog.append(el('p', { class: 'ld-modal__msg', text: opts.description }));
    document.body.appendChild(dialog);
    dialog.setActions([
      { label: '取消', value: 'cancel' },
      { label: opts.confirmText || '确定', value: 'ok', variant: 'primary' },
    ]);

    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      dialog.close(value);
      resolve(value);
      window.setTimeout(() => dialog.remove(), 0);
    };

    dialog.addEventListener('ld-action', (event) => {
      if (event.detail.value !== 'ok') {
        finish(null);
        return;
      }
      const value = String(input.value || '').trim();
      field.clearError();
      if (opts.required && !value) {
        field.setError('请填写内容');
        input.focus();
        return;
      }
      if (typeof opts.validate === 'function') {
        const message = opts.validate(value);
        if (message) {
          field.setError(message);
          input.focus();
          return;
        }
      }
      finish(value);
    });
    dialog.addEventListener('ld-close', () => finish(null));
    if (!opts.multiline) {
      input.addEventListener('keydown', (event) => {
        if (event.key === 'Enter') {
          event.preventDefault();
          const okButton = dialog.querySelector('ld-button[data-value="ok"] button');
          if (okButton) okButton.click();
        }
      });
    }
    dialog._initialFocus = input;
    dialog.open();
    window.requestAnimationFrame(() => {
      input.focus();
      if (typeof input.select === 'function') input.select();
    });
  });
}

/* ---------- 登录与首次凭据修改 ---------- */

function noteBlock(type, icon, text) {
  return el('div', { class: `ld-note ld-note--${type}` },
    el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: icon })),
    el('div', { class: 'ld-note__body', text }));
}

/**
 * 凭据修改面板（修改账号 / 密码 / 显示名），登录页与强制修改页共用。
 * 返回 { node, focus() }。
 */
export function credentialPanel(opts = {}) {
  const current = el('input', { class: 'ld-input', type: 'password', autocomplete: 'current-password', 'aria-label': '当前密码' });
  const newUser = el('input', { class: 'ld-input', type: 'text', autocomplete: 'off', 'aria-label': '新账号', placeholder: (opts.user && opts.user.username) || '' });
  const display = el('input', { class: 'ld-input', type: 'text', autocomplete: 'off', 'aria-label': '新显示名', placeholder: (opts.user && opts.user.displayName) || '' });
  const pass = el('input', { class: 'ld-input', type: 'password', autocomplete: 'new-password', 'aria-label': '新密码' });
  const confirm = el('input', { class: 'ld-input', type: 'password', autocomplete: 'new-password', 'aria-label': '确认新密码' });

  const currentField = el('ld-field', { label: '当前密码', required: true, hint: opts.forced ? '首次登录请先用当前密码验证身份' : '' }, current);
  const userField = el('ld-field', { label: '新账号（可选）', hint: '留空表示不修改；修改后请使用新账号登录' }, newUser);
  const displayField = el('ld-field', { label: '新显示名（可选）' }, display);
  const passField = el('ld-field', { label: opts.forced ? '新密码' : '新密码（可选）', required: opts.forced ? true : undefined, hint: '至少 8 个字符；不修改密码请留空' }, pass);
  const confirmField = el('ld-field', { label: '确认新密码' }, confirm);
  const submit = el('ld-button', { label: opts.submitLabel || '保存修改', variant: 'primary', block: true, type: 'submit' });
  const error = el('div', { class: 'ld-note ld-note--danger ld-hidden' },
    el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: 'alert' })),
    el('div', { class: 'ld-note__body' }));

  const form = el('form', { class: 'ld-form', novalidate: true },
    currentField, userField, displayField, passField, confirmField, error, submit);

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const currentValue = current.value;
    const newUsername = newUser.value.trim();
    const newDisplay = display.value.trim();
    const newPassword = pass.value;
    const confirmValue = confirm.value;

    currentField.clearError();
    userField.clearError();
    passField.clearError();
    confirmField.clearError();
    error.classList.add('ld-hidden');

    if (!currentValue) {
      currentField.setError('请输入当前密码');
      current.focus();
      return;
    }
    if (!newUsername && !newPassword && !newDisplay) {
      passField.setError('请设置新密码（至少 8 位）或填写新账号');
      pass.focus();
      return;
    }
    if (newPassword) {
      if (newPassword.length < 8) {
        passField.setError('密码至少 8 个字符');
        pass.focus();
        return;
      }
      if (newPassword !== confirmValue) {
        confirmField.setError('两次输入的新密码不一致');
        confirm.focus();
        return;
      }
      if (newPassword === currentValue) {
        passField.setError('新密码不能与当前密码相同');
        pass.focus();
        return;
      }
    }

    const body = { currentPassword: currentValue };
    if (newUsername) body.newUsername = newUsername;
    if (newPassword) body.newPassword = newPassword;
    if (newDisplay) body.newDisplayName = newDisplay;

    submit.setBusy(true);
    try {
      const result = await api('/api/auth/change-credentials', { method: 'POST', body });
      session.set({ user: result.user, home: result.home || '/' });
      toastSuccess('凭据已更新');
      if (opts.onDone) opts.onDone(result);
    } catch (err) {
      if (err.code === 'invalid_password') currentField.setError('当前密码不正确');
      else if (err.code === 'weak_password' || err.code === 'same_password') passField.setError(err.message);
      else if (err.code === 'already_exists' || err.code === 'invalid_username') userField.setError(err.message);
      error.querySelector('.ld-note__body').textContent = err.message || '修改失败';
      error.classList.remove('ld-hidden');
      current.value = '';
      pass.value = '';
      confirm.value = '';
      current.focus();
    } finally {
      submit.setBusy(false);
    }
  });

  return {
    node: form,
    focus: () => window.requestAnimationFrame(() => current.focus()),
  };
}

/**
 * 登录页（用户端与管理端共用，只是文案不同）。
 * 首次登录（mustChangeCredentials）时会就地切换成凭据修改面板，完成后才回调 onSuccess。
 */
export function showLoginView(opts = {}) {
  const usernameInput = el('input', { class: 'ld-input', type: 'text', autocomplete: 'username', 'aria-label': '账号' });
  const passwordInput = el('input', { class: 'ld-input', type: 'password', autocomplete: 'current-password', 'aria-label': '密码' });
  const usernameField = el('ld-field', { label: '账号', required: true }, usernameInput);
  const passwordField = el('ld-field', { label: '密码', required: true }, passwordInput);
  const submit = el('ld-button', { label: '登录', variant: 'primary', block: true, type: 'submit' });
  const error = el('div', { class: 'ld-note ld-note--danger ld-hidden' },
    el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: 'alert' })),
    el('div', { class: 'ld-note__body' }));
  const form = el('form', { class: 'ld-form', novalidate: true },
    usernameField, passwordField, error, submit);

  const brand = el('div', { class: 'ld-auth__brand' },
    el('span', { class: 'ld-auth__logo' }, el('ld-icon', { name: opts.brandIcon || 'disk', size: 'xl' })),
    el('div', { class: 'ld-auth__title', text: opts.title || 'HomeVault' }),
    el('div', { class: 'ld-auth__sub', text: opts.subtitle || '轻量文件托管' }));

  const notice = noteBlock('info', 'info', opts.notice
    || '首次使用请用默认账号 admin / admin 登录，登录后会要求立即修改初始凭据。');

  const card = el('div', { class: 'ld-auth__card' }, brand, notice, form);
  const host = el('div', { class: 'ld-auth' }, el('div', { class: 'ld-auth__inner' }, card));
  document.body.appendChild(host);

  const showError = (message) => {
    error.querySelector('.ld-note__body').textContent = message;
    error.classList.remove('ld-hidden');
  };

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const username = usernameInput.value.trim();
    const password = passwordInput.value;
    usernameField.clearError();
    passwordField.clearError();
    error.classList.add('ld-hidden');
    if (!username) {
      usernameField.setError('请输入账号');
      usernameInput.focus();
      return;
    }
    if (!password) {
      passwordField.setError('请输入密码');
      passwordInput.focus();
      return;
    }
    submit.setBusy(true);
    try {
      const result = await api('/api/auth/login', { method: 'POST', body: { username, password } });
      session.set({
        user: result.user,
        home: result.home || ((result.user && result.user.isAdmin) ? '/admin/' : '/'),
        ready: true,
      });
      if (result.user && result.user.mustChangeCredentials) {
        const panel = credentialPanel({
          user: result.user,
          forced: true,
          submitLabel: '保存并继续',
          onDone: () => {
            host.remove();
            if (opts.onSuccess) opts.onSuccess(result);
          },
        });
        clearNode(card);
        card.append(brand,
          noteBlock('warn', 'shield', '为了账号安全，首次登录必须修改账号或密码，完成后才能继续使用。'),
          panel.node);
        panel.focus();
        return;
      }
      host.remove();
      if (opts.onSuccess) opts.onSuccess(result);
    } catch (err) {
      if (err.code === 'invalid_credentials') passwordField.setError('账号或密码错误');
      else if (err.code === 'disabled') usernameField.setError('账号已被禁用');
      showError(err.message || '登录失败');
      passwordInput.value = '';
      passwordInput.focus();
    } finally {
      submit.setBusy(false);
    }
  });

  window.requestAnimationFrame(() => {
    usernameInput.focus();
    if (usernameInput.value) passwordInput.focus();
  });
  return { element: host, destroy: () => host.remove() };
}

/** 覆盖全屏、不可关闭的凭据修改页（会话中途检测到 must_change_credentials 时使用）。 */
export function showCredentialGate(opts = {}) {
  const panel = credentialPanel({
    user: opts.user,
    forced: true,
    submitLabel: '保存并继续',
    onDone: (result) => {
      host.remove();
      if (opts.onDone) opts.onDone(result);
    },
  });
  const card = el('div', { class: 'ld-auth__card' },
    el('div', { class: 'ld-auth__brand' },
      el('span', { class: 'ld-auth__logo' }, el('ld-icon', { name: 'shield', size: 'xl' })),
      el('div', { class: 'ld-auth__title', text: '请先修改初始凭据' }),
      el('div', { class: 'ld-auth__sub', text: '首次登录必须修改账号或密码' })),
    noteBlock('warn', 'info', `当前账号：${(opts.user && opts.user.username) || '—'}。密码至少 8 个字符，修改后需要用新凭据重新登录。`),
    panel.node,
    opts.onLogout
      ? el('div', { class: 'ld-auth__foot' }, el('ld-button', { label: '退出登录', variant: 'ghost', block: true, onClick: () => opts.onLogout() }))
      : null);
  const host = el('div', { class: 'ld-auth ld-auth--gate' }, el('div', { class: 'ld-auth__inner' }, card));
  document.body.appendChild(host);
  panel.focus();
  return { element: host, destroy: () => host.remove() };
}

/* ---------- 上传面板与 UploadManager 的绑定 ---------- */

/** 创建上传面板并接上 UploadManager 的动作与状态同步。 */
export function createUploadPanel(uploads) {
  const panel = document.createElement('ld-upload-panel');
  panel.items = [];
  panel.sessions = [];
  document.body.appendChild(panel);

  const sync = () => {
    const list = uploads.items.slice();
    const show = list.length > 0 || (uploads.sessions && uploads.sessions.length > 0);
    panel.hidden = !show;
    panel.items = list;
    panel.sessions = (uploads.sessions || []).slice();
    const stats = uploads.stats();
    panel.progress = { done: stats.done, total: stats.total };
  };

  panel.addEventListener('ld-upload-action', (event) => {
    const { action, id } = event.detail;
    if (action === 'toggle') panel.collapsed = !panel.collapsed;
    else if (action === 'pause') uploads.pause(id);
    else if (action === 'resume' || action === 'retry') uploads.resume(id);
    else if (action === 'cancel') uploads.cancel(id);
    else if (action === 'dismiss') uploads.dismiss(id);
    else if (action === 'clear-finished') uploads.clearFinished();
    else if (action === 'cancel-all') uploads.cancelAll();
    else if (action === 'cancel-session') uploads.cancelSession(id);
  });

  uploads.addEventListener('change', sync);
  sync();
  return {
    panel,
    sync,
    destroy() {
      uploads.removeEventListener('change', sync);
      panel.remove();
    },
  };
}

/* ---------- 视图骨架小工具（用户端与管理端共用） ---------- */

/** 视图头部：标题 + 说明 + 右侧操作区。 */
export function viewHead(title, sub, actions) {
  return el('div', { class: 'ld-view__head' },
    el('div', {},
      el('h2', { text: title }),
      sub ? el('div', { class: 'ld-view__sub', text: sub }) : null),
    actions && actions.length ? el('div', { class: 'ld-view__actions' }, ...actions) : null);
}

/** 带标题的卡片区块。 */
export function cardSection(title, ...children) {
  return el('section', { class: 'ld-card' },
    el('div', { class: 'ld-card__head' }, el('h3', { text: title })),
    el('div', { class: 'ld-card__body' }, ...children));
}

/**
 * 可限高滚动的卡片：内容多时在卡片内部滚动，不把整页拉长。
 * bodyClass 可传 'ld-card__body--scroll' / 'ld-card__body--scroll-lg' / 'ld-card__body--flush'。
 */
export function cardScrollSection(title, body, options = {}) {
  const bodyClass = options.bodyClass || 'ld-card__body--scroll';
  const head = el('div', { class: 'ld-card__head' }, el('h3', { text: title }));
  if (options.action) head.appendChild(options.action);
  return el('section', { class: `ld-card${options.class ? ' ' + options.class : ''}` },
    head,
    el('div', { class: `ld-card__body ${bodyClass}` }, body));
}

/** 提示条：kind = info / warn / danger / ok。 */
export function noteRow(kind, text) {
  const icon = kind === 'danger' ? 'alert' : kind === 'warn' ? 'shield' : kind === 'ok' ? 'check' : 'info';
  return el('div', { class: `ld-note ld-note--${kind}` },
    el('span', { class: 'ld-note__icon' }, el('ld-icon', { name: icon })),
    el('div', { class: 'ld-note__body', text }));
}

/** 本地排序：数字按数值比较，其余按中文/数字友好的字符串序。 */
export function sortRows(rows, key, dir) {
  const sign = dir === 'desc' ? -1 : 1;
  return rows.slice().sort((a, b) => {
    const av = a ? a[key] : null;
    const bv = b ? b[key] : null;
    if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * sign;
    return String(av ?? '').localeCompare(String(bv ?? ''), 'zh-CN', { numeric: true }) * sign;
  });
}

/** 给 ld-data-table 接上本地排序（只排当前已取回的数据）。 */
export function enableLocalSort(table, state, getRows) {
  table.sort = state.sort;
  table.addEventListener('ld-sort', (event) => {
    state.sort = { key: event.detail.key, dir: event.detail.dir };
    table.sort = state.sort;
    table.rows = sortRows(getRows(), state.sort.key, state.sort.dir);
  });
}

/* ---------- 可搜索下拉（ld-combobox） ---------- */

/**
 * ld-combobox：可输入过滤的下拉选择器。
 * 用于"用户选择""动作筛选"这类可能有很多选项的地方，避免一长串列表逐个找。
 *
 * options: [{ value, label, hint?, icon? }]
 * 事件：'change' → detail = { value, option }
 */
export class LdCombobox extends LdElement {
  static defaults = { options: [], value: '', placeholder: '请选择', searchPlaceholder: '输入关键字过滤…', disabled: false };
  static observedAttributes = ['value', 'placeholder', 'disabled'];

  render() {
    if (!this._built) {
      this._built = true;
      this._button = el('button', { class: 'ld-combo__btn', type: 'button' });
      this._label = el('span', { class: 'ld-combo__label' });
      this._caret = el('ld-icon', { name: 'chevron-down', size: 'sm' });
      this._button.append(this._label, this._caret);

      this._search = el('input', {
        class: 'ld-input ld-combo__search', type: 'search', 'aria-label': '搜索选项',
        placeholder: this.getAttribute('search-placeholder') || '输入关键字过滤…',
      });
      this._list = el('div', { class: 'ld-combo__list', role: 'listbox' });
      this._pop = el('div', { class: 'ld-combo__pop' }, this._search, this._list);
      this._pop.hidden = true;

      this._root = el('div', { class: 'ld-combo' }, this._button, this._pop);
      clearNode(this);
      this.appendChild(this._root);

      this._button.addEventListener('click', (event) => {
        event.stopPropagation();
        if (this.flag('disabled')) return;
        this.toggle(!this._open);
      });
      this._search.addEventListener('input', () => this.renderOptions());
      this._search.addEventListener('keydown', (event) => {
        if (event.key === 'Escape') { this.toggle(false); this._button.focus(); }
        if (event.key === 'Enter') {
          const first = this._list.querySelector('.ld-combo__opt');
          if (first) first.click();
        }
      });
      this._onDocClick = (event) => {
        if (this._open && !this._root.contains(event.target)) this.toggle(false);
      };
      document.addEventListener('click', this._onDocClick);
      this.onUnmount = () => document.removeEventListener('click', this._onDocClick);
    }
    this._label.textContent = this.currentLabel();
    this._button.disabled = this.flag('disabled');
    this._button.classList.toggle('is-empty', !this.value);
    if (this._open) this.renderOptions();
  }

  get value() {
    const fromAttr = this.getAttribute('value');
    if (fromAttr !== null && fromAttr !== '') return fromAttr;
    return this.props.value || '';
  }

  set value(v) {
    this.setAttribute('value', v === null || v === undefined ? '' : String(v));
    if (this._mounted) this.render();
  }

  // 注意：不要把这个方法叫 options()——defineProps 会给原型装一个 options 取值器，
  // 同名方法会被覆盖成属性读取，调用 this.options() 直接抛 TypeError。
  optionList() {
    return Array.isArray(this.props.options) ? this.props.options : [];
  }

  currentLabel() {
    const opt = this.optionList().find((o) => String(o.value) === String(this.value));
    if (opt) return opt.label;
    return this.getAttribute('placeholder') || this.props.placeholder || '请选择';
  }

  toggle(open) {
    this._open = open === undefined ? !this._open : !!open;
    this._pop.hidden = !this._open;
    this._root.classList.toggle('is-open', this._open);
    if (this._open) {
      this._search.value = '';
      this.renderOptions();
      this._search.focus();
    }
  }

  renderOptions() {
    const q = this._search.value.trim().toLowerCase();
    clearNode(this._list);
    const list = this.optionList().filter((o) => {
      if (!q) return true;
      return String(o.label).toLowerCase().includes(q) || String(o.value).toLowerCase().includes(q);
    });
    if (!list.length) {
      this._list.appendChild(el('div', { class: 'ld-combo__empty', text: '没有匹配的选项' }));
      return;
    }
    for (const opt of list) {
      const active = String(opt.value) === String(this.value);
      const row = el('button', {
        class: 'ld-combo__opt' + (active ? ' is-active' : ''), type: 'button', role: 'option',
      },
      opt.icon ? el('ld-icon', { name: opt.icon, size: 'sm' }) : null,
      el('span', { class: 'ld-combo__opt-label', text: opt.label }),
      opt.hint ? el('span', { class: 'ld-combo__opt-hint', text: opt.hint }) : null);
      row.addEventListener('click', () => {
        this.value = String(opt.value);
        this.toggle(false);
        this.emit('change', { value: String(opt.value), option: opt });
      });
      this._list.appendChild(row);
    }
  }
}
defineProps(LdCombobox, ['options', 'value']);

/* ---------- 配额输入（数值 + 单位） ---------- */

/**
 * quotaInput：数字 + 单位选择器，返回字节数。
 * 单位用 MiB / GiB（界面上显示 M / GB），避免用户对着 10737418240 数零。
 */
export function quotaInput(opts = {}) {
  const units = [
    { id: 'MiB', label: 'M', mul: 1 << 20 },
    { id: 'GiB', label: 'GB', mul: 1 << 30 },
  ];
  let initial = Number(opts.value) || 0;
  let unit = units[1];
  if (initial > 0) {
    for (const u of units) {
      if (initial >= u.mul && initial % u.mul === 0) unit = u;
    }
  }
  const num = el('input', {
    class: 'ld-input ld-quota-input__num', type: 'number', min: '0', step: '1',
    'aria-label': opts.label || '容量数值',
    placeholder: '0',
  });
  num.value = initial > 0 ? String(initial / unit.mul) : '';
  const unitSelect = el('select', { class: 'ld-select ld-quota-input__unit', 'aria-label': '容量单位' },
    units.map((u) => el('option', { value: u.id, text: u.label, selected: u.id === unit.id ? true : undefined })));
  unitSelect.value = unit.id;
  const wrap = el('div', { class: 'ld-quota-input' }, num, unitSelect);

  const api = {
    node: wrap,
    getValue() {
      const n = Number(num.value);
      if (!Number.isFinite(n) || n <= 0) return 0;
      const u = units.find((x) => x.id === unitSelect.value) || units[1];
      return Math.round(n * u.mul);
    },
    setValue(bytes) {
      const v = Number(bytes) || 0;
      let u = units[1];
      if (v > 0) {
        for (const cand of units) {
          if (v >= cand.mul && v % cand.mul === 0) u = cand;
        }
      }
      unitSelect.value = u.id;
      num.value = v > 0 ? String(v / u.mul) : '';
    },
    focus() { num.focus(); },
  };
  if (typeof opts.onChange === 'function') {
    num.addEventListener('input', () => opts.onChange(api.getValue()));
    unitSelect.addEventListener('change', () => opts.onChange(api.getValue()));
  }
  return api;
}

/* ---------- 轻量 SVG 图表 ---------- */

const SVG_NS_CHART = 'http://www.w3.org/2000/svg';

function svgEl(tag, attrs = {}) {
  const node = document.createElementNS(SVG_NS_CHART, tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined) continue;
    node.setAttribute(k, String(v));
  }
  return node;
}

/** 环形图：segments = [{ value, color, label }]，中间显示主数字。 */
export function donutChart(opts = {}) {
  const size = opts.size || 148;
  const thickness = opts.thickness || 14;
  const r = (size - thickness) / 2;
  const c = size / 2;
  const total = (opts.segments || []).reduce((sum, s) => sum + Math.max(0, Number(s.value) || 0), 0);
  const svg = svgEl('svg', {
    class: 'ld-chart ld-chart--donut', viewBox: `0 0 ${size} ${size}`,
    width: size, height: size, role: 'img',
    'aria-label': opts.ariaLabel || '容量占用',
  });
  svg.appendChild(svgEl('circle', {
    cx: c, cy: c, r, fill: 'none', stroke: 'var(--ld-surface-3)', 'stroke-width': thickness,
  }));
  const circ = 2 * Math.PI * r;
  let offset = 0;
  for (const seg of opts.segments || []) {
    const value = Math.max(0, Number(seg.value) || 0);
    if (!value || !total) continue;
    const len = (value / total) * circ;
    const arc = svgEl('circle', {
      cx: c, cy: c, r, fill: 'none', stroke: seg.color || 'var(--ld-accent)',
      'stroke-width': thickness, 'stroke-linecap': 'butt',
      'stroke-dasharray': `${len} ${circ - len}`,
      'stroke-dashoffset': -offset,
      transform: `rotate(-90 ${c} ${c})`,
    });
    svg.appendChild(arc);
    offset += len;
  }
  const label = svgEl('text', {
    x: c, y: c - 2, 'text-anchor': 'middle', 'dominant-baseline': 'middle',
    class: 'ld-chart__value',
  });
  label.textContent = opts.centerLabel || '';
  svg.appendChild(label);
  if (opts.centerSub) {
    const sub = svgEl('text', {
      x: c, y: c + 18, 'text-anchor': 'middle', 'dominant-baseline': 'middle',
      class: 'ld-chart__sub',
    });
    sub.textContent = opts.centerSub;
    svg.appendChild(sub);
  }
  return svg;
}

/** 柱状图：items = [{ label, value, color? }]。 */
export function barChart(opts = {}) {
  const items = opts.items || [];
  const width = opts.width || 520;
  const height = opts.height || 160;
  const padX = 28;
  const padBottom = 26;
  const plotH = height - padBottom - 8;
  const max = Math.max(1, ...items.map((i) => Number(i.value) || 0));
  const gap = items.length > 1 ? 12 : 0;
  const slot = (width - padX * 2 - gap * Math.max(0, items.length - 1)) / Math.max(1, items.length);
  // 柱子不拉满：限制最大宽度并整体居中，避免只剩一项时变成一大块色板。
  const barW = Math.max(6, Math.min(56, slot));
  const totalW = barW * items.length + gap * Math.max(0, items.length - 1);
  const startX = padX + Math.max(0, (width - padX * 2 - totalW) / 2);
  const svg = svgEl('svg', {
    class: 'ld-chart ld-chart--bars', viewBox: `0 0 ${width} ${height}`,
    preserveAspectRatio: 'xMidYMid meet', role: 'img', 'aria-label': opts.ariaLabel || '柱状图',
  });
  svg.appendChild(svgEl('line', {
    x1: padX, y1: height - padBottom, x2: width - padX, y2: height - padBottom,
    stroke: 'var(--ld-border)', 'stroke-width': 1,
  }));
  items.forEach((item, index) => {
    const value = Number(item.value) || 0;
    const h = Math.max(value > 0 ? 3 : 0, (value / max) * plotH);
    const x = startX + index * (barW + gap);
    const y = height - padBottom - h;
    svg.appendChild(svgEl('rect', {
      x, y, width: barW, height: h, rx: Math.min(5, barW / 2),
      fill: item.color || 'var(--ld-accent)', opacity: value > 0 ? '1' : '0.25',
    }));
    if (value > 0) {
      const t = svgEl('text', {
        x: x + barW / 2, y: y - 5, 'text-anchor': 'middle', class: 'ld-chart__tick',
      });
      t.textContent = String(value);
      svg.appendChild(t);
    }
    const label = svgEl('text', {
      x: x + barW / 2, y: height - 8, 'text-anchor': 'middle', class: 'ld-chart__label',
    });
    label.textContent = item.label;
    svg.appendChild(label);
  });
  return svg;
}

/** 折线图：points = [{ label, value }]。 */
export function lineChart(opts = {}) {
  const points = opts.points || [];
  const width = opts.width || 520;
  const height = opts.height || 160;
  const padX = 30;
  const padBottom = 26;
  const plotH = height - padBottom - 12;
  const max = Math.max(1, ...points.map((p) => Number(p.value) || 0));
  const stepX = points.length > 1 ? (width - padX * 2) / (points.length - 1) : 0;
  const svg = svgEl('svg', {
    class: 'ld-chart ld-chart--line', viewBox: `0 0 ${width} ${height}`,
    preserveAspectRatio: 'none', role: 'img', 'aria-label': opts.ariaLabel || '趋势图',
  });
  for (let i = 0; i <= 3; i++) {
    const y = 12 + (plotH / 3) * i;
    svg.appendChild(svgEl('line', {
      x1: padX, y1: y, x2: width - padX, y2: y, stroke: 'var(--ld-border)', 'stroke-width': 1,
      'stroke-dasharray': i === 3 ? '0' : '3 5',
    }));
  }
  const coords = points.map((p, i) => ({
    x: padX + i * stepX,
    y: 12 + plotH - ((Number(p.value) || 0) / max) * plotH,
    label: p.label,
    value: Number(p.value) || 0,
  }));
  if (coords.length > 1) {
    const d = coords.map((c, i) => `${i === 0 ? 'M' : 'L'}${c.x.toFixed(1)} ${c.y.toFixed(1)}`).join(' ');
    svg.appendChild(svgEl('path', {
      d, fill: 'none', stroke: 'var(--ld-accent)', 'stroke-width': 2.2,
      'stroke-linejoin': 'round', 'stroke-linecap': 'round',
    }));
    const area = `${d} L${coords[coords.length - 1].x.toFixed(1)} ${12 + plotH} L${coords[0].x.toFixed(1)} ${12 + plotH} Z`;
    svg.appendChild(svgEl('path', { d: area, fill: 'var(--ld-accent-soft)', opacity: '0.7' }));
  }
  for (const c of coords) {
    svg.appendChild(svgEl('circle', { cx: c.x, cy: c.y, r: 3.2, fill: 'var(--ld-accent)' }));
    const label = svgEl('text', {
      x: c.x, y: height - 8, 'text-anchor': 'middle', class: 'ld-chart__label',
    });
    label.textContent = c.label;
    svg.appendChild(label);
  }
  return svg;
}

/* ---------- 组件注册 ---------- */

export const COMPONENTS = {
  'ld-icon': LdIcon,
  'ld-spinner': LdSpinner,
  'ld-button': LdButton,
  'ld-field': LdField,
  'ld-tabs': LdTabs,
  'ld-empty': LdEmpty,
  'ld-progress': LdProgress,
  'ld-quota-bar': LdQuotaBar,
  'ld-file-icon': LdFileIcon,
  'ld-breadcrumb': LdBreadcrumb,
  'ld-modal': LdModal,
  'ld-confirm': LdConfirm,
  'ld-password-prompt': LdPasswordPrompt,
  'ld-toast': LdToast,
  'ld-menu': LdMenu,
  'ld-dir-picker': LdDirPicker,
  'ld-upload-panel': LdUploadPanel,
  'ld-preview': LdPreview,
  'ld-data-table': LdDataTable,
  'ld-combobox': LdCombobox,
};

export function registerComponents() {
  for (const [name, ctor] of Object.entries(COMPONENTS)) {
    if (!customElements.get(name)) customElements.define(name, ctor);
  }
}

registerComponents();



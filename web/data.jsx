// Mock data for the dashboard prototype.
// Realistic-feeling values for a MagiReco-style private server admin.

const NOW = Date.now();

const initialServerState = {
  status: "ok", // ok | warn | err
  maintenanceMessage: "服务器正在进行例行维护,预计 30 分钟后恢复。期间无法登录游戏。",
  estimatedEnd: null, // ms timestamp
  onlineDownloadEnabled: true,
  offlinePackageEnabled: true,
  disabledMessage: "下载服务暂时关闭,请稍后再试。",
};

const initialVersions = {
  allowed: ["1.4.7", "1.4.8", "1.5.0-test"],
  fakeVersion: "1.4.7",
  fakeAppName: "マギアレコード",
  updateUrl: "https://magireco-revival.example.cn/dist/update.json",
  testUpdateUrl: "https://magireco-revival.example.cn/dist/update-internal.json",
};

// services:握手期下发的运行时地址。客户端 API 变动说明 §1。
const initialServices = {
  cap_worker_url:   "",
  proxy_backends:   [],
  game_server_host: "",
};

const initialMirrors = [
  { kind: "http", group: "线路A · 国内 CDN", url: "https://cdn1.magireco-revival.example.cn/res/" },
  { kind: "http", group: "线路A · 国内 CDN", url: "https://cdn2.magireco-revival.example.cn/res/" },
  { kind: "s3",   group: "线路B · S3 兜底",  url: "https://s3.ap-east-1.amazonaws.com",
    bucket: "magireco-res", region: "ap-east-1" },
  { kind: "http", group: "线路C · 海外镜像", url: "https://mirror.akihabara-jp.example.net/magireco/res/",
    files: [
      { key: "data/master_2024.bin", size: 1048576 },
      { key: "data/scenario_001.zip", size: 524288 },
    ],
  },
];

/* ---------- Auto-package offline zip ---------- */
const initialAutoPackage = {
  enabled: true,
  intervalSec: 21600,           // 6h
  triggerOn: "new-release",      // 'interval' | 'new-release' | 'both'
  retainVersions: 3,
  compress: "zstd",
  lastRunAt: NOW - 1000 * 60 * 60 * 5 - 1000 * 60 * 22,
  lastResult: "ok",
  inProgress: false,
};

/* ---------- Server-side scheduled tasks ---------- */
const initialTasks = {
  heartbeatTimeoutSec: 15,
  banAutoExpireScanSec: 60,
  banSweepSec: 3600,
  metricsFlushSec: 30,
  sessionGcSec: 900,
  capWorkerHealthSec: 120,
};

/* ---------- Unverified account auto-purge ---------- */
const initialUnverifiedPurge = {
  enabled: false,
  retainDays: 30,
  scanSec: 86400,
};

/* ---------- Size limits (runtime-tunable) ---------- */
const initialLimits = {
  globalBodyMB: 8,    // 全局请求体上限(MiB)
  hotUpdateMB: 1024,  // 热更新包上传/下载上限(MiB)
};

/* ---------- Current admin (logged-in) ---------- */
const initialCurrentAdmin = {
  username: "admin_homura",
  email: "homura@magireco-revival.cn",
  role: "super_admin",   // super_admin | admin | readonly
  avatarLetter: "H",
};

/* ---------- Admin roster (for permissions page) ---------- */
const initialAdminRoster = [
  { id: "adm_1", username: "admin_homura",  email: "homura@magireco-revival.cn",  role: "super_admin", lastLoginAt: NOW - 1000 * 60 * 2,       createdAt: NOW - 1000 * 60 * 60 * 24 * 380 },
  { id: "adm_2", username: "admin_madoka",  email: "madoka@magireco-revival.cn",  role: "admin",        lastLoginAt: NOW - 1000 * 60 * 35,      createdAt: NOW - 1000 * 60 * 60 * 24 * 280 },
  { id: "adm_3", username: "admin_kyoko",   email: "kyoko@magireco-revival.cn",   role: "admin",        lastLoginAt: NOW - 1000 * 60 * 60 * 8,  createdAt: NOW - 1000 * 60 * 60 * 24 * 170 },
  { id: "adm_4", username: "audit_sayaka",  email: "sayaka@magireco-revival.cn",  role: "readonly",     lastLoginAt: NOW - 1000 * 60 * 60 * 30, createdAt: NOW - 1000 * 60 * 60 * 24 * 95  },
  { id: "adm_5", username: "ops_mami",      email: "mami@magireco-revival.cn",    role: "admin",        lastLoginAt: NOW - 1000 * 60 * 60 * 50, createdAt: NOW - 1000 * 60 * 60 * 24 * 40  },
];

const ROLE_LABELS = {
  super_admin: { label: "超级管理员", desc: "全部权限,可管理其他管理员", color: "info" },
  admin:       { label: "管理员",     desc: "运维 / 资源 / 用户操作",   color: "ok"   },
  readonly:    { label: "只读审计",   desc: "仅可查看,不可修改",        color: "muted" },
};

/* ---------- Nodes (primary + secondary) ---------- */
const initialNodes = {
  primary: {
    id: "magireco-srv-tokyo-01",
    role: "primary",
    roleLabel: "主节点 · 面板 / API / 数据",
    region: "ap-northeast-1",
    regionLabel: "东京",
    publicIP: "203.0.113.42",
    status: "ok",
    uptimeSec: 71 * 86400 + 4 * 3600 + 22 * 60,
    cpuPct: 18,
    memPct: 42,
    activeConns: 312,
    qps: 142,
    services: ["api", "auth", "/init", "ws"],
    maintenance: false,
  },
  secondary: {
    id: "magireco-cdn-shanghai-01",
    role: "secondary",
    roleLabel: "副节点 · 资源下载 / 转发主节点",
    region: "cn-east-shanghai",
    regionLabel: "上海",
    publicIP: "198.51.100.7",
    status: "ok",
    uptimeSec: 23 * 86400 + 16 * 3600 + 11 * 60,
    cpuPct: 9,
    memPct: 31,
    activeDownloads: 47,
    egressBps: 142_000_000,
    upstreamLatencyMs: 38,
    cacheHitRate: 0.943,
    forwardingDataRequests: true,
    maintenance: false,
  },
};

/* ---------- GitHub Release → S3 → CDN pipeline ---------- */
const initialPipeline = {
  // GitHub 数据源
  github_owner: "magireco-revival",
  github_repo: "client-assets",
  github_tag_pattern: "v*",
  auto_sync: true,
  poll_interval_sec: 300,
  github_token_env: "GH_TOKEN",

  // S3 资源上传
  s3_enabled: true,
  s3_endpoint: "",
  s3_bucket: "magireco-assets-ap-east",
  s3_region: "ap-east-1",
  s3_key_id: "AKIAXXX",
  s3_secret_env: "CNV_S3_SECRET",

  // CDN 缓存刷新
  cdn_enabled: true,
  cdn_provider: "cloudflare",
  cdn_zone: "",
  cdn_purge_url: "",
  cdn_auth_env: "CNV_CDN_TOKEN",

  // 离线包上传（独立配置）
  offline_upload_enabled: false,
  offline_s3_endpoint: "",
  offline_s3_bucket: "",
  offline_s3_region: "",
  offline_s3_key_id: "",
  offline_s3_secret_env: "",

  // 运行时状态
  in_progress: false,
  last_sync_at: NOW - 1000 * 60 * 2,
  last_sync_result: "ok",

  // 历史同步记录（由后端 /admin/pipeline/runs 提供；此处 mock）
  releases: [
    {
      tag: "v2.4.1",
      title: "v2.4.1 · 复刻活动「Magia Festa 2026」",
      releasedAt: NOW - 1000 * 60 * 60 * 3 - 1000 * 60 * 8,
      assetCount: 47,
      totalBytes: 1_923_584_923,
      stages: [
        { id: "detect",   label: "检测新版本", state: "done", at: NOW - 1000 * 60 * 60 * 3 - 1000 * 60 * 4, note: "GitHub webhook · push tag v2.4.1" },
        { id: "download", label: "下载 Release 资源", state: "done", at: NOW - 1000 * 60 * 60 * 3 - 1000 * 60 * 1, durationMs: 142_000, note: "47 / 47 assets · 1.79 GB" },
        { id: "s3",       label: "上传至 S3", state: "done", at: NOW - 1000 * 60 * 60 * 2 - 1000 * 60 * 50, durationMs: 612_000, note: "magireco-assets-ap-east" },
        { id: "cdn",      label: "刷新 CDN 缓存", state: "done", at: NOW - 1000 * 60 * 60 * 2 - 1000 * 60 * 40, durationMs: 12_400, note: "Cloudflare · 47 URLs purged" },
      ],
    },
    {
      tag: "v2.4.0",
      title: "v2.4.0 · 第三章主线追加",
      releasedAt: NOW - 1000 * 60 * 60 * 24 * 11,
      assetCount: 38,
      totalBytes: 1_647_239_104,
      stages: "all-done",
    },
    {
      tag: "v2.3.9",
      title: "v2.3.9 · 卡牌平衡性调整",
      releasedAt: NOW - 1000 * 60 * 60 * 24 * 23,
      assetCount: 12,
      totalBytes: 287_492_096,
      stages: "all-done",
    },
  ],
};

const initialOfflinePackage = {
  url: "https://offline.magireco-revival.example.cn/pkg/magireco_full_2.4.0_20260418.zip",
  version: "2.4.0",
  sha256: "9f8e7d6c5b4a392817f6e5d4c3b2a190817263544536271809a8b7c6d5e4f3a2",
  size: 1872452096,
  uploadedAt: NOW - 1000 * 60 * 60 * 28,
  // min_version:服务端要求客户端必须安装的最低离线包版本号(策略,非元数据)。
  // 空表示不下发版本门槛,客户端跳过版本检查(server-offline-pack-validation.md §3.1)。
  min_version: "",
};

const initialJsBundle = {
  version: 47,
  sha256: "3c0a9b8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a09",
  size: 4128492,
  url: "https://hot.magireco-revival.example.cn/js/bundle-47.js",
  publishedAt: NOW - 1000 * 60 * 60 * 13,
};

const initialScenarioBundle = {
  version: 23,
  sha256: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
  size: 81923584,
  url: "https://hot.magireco-revival.example.cn/scn/scenario-23.dat",
  publishedAt: NOW - 1000 * 60 * 60 * 80,
};

// cap-worker 为内置(无外部端点 URL);形态与后端 /admin/captcha 一致(snake_case)。
const initialCaptcha = {
  enabled: true,
  difficulty: 12,
  challenge_count: 3,
  last_tested_at: NOW - 1000 * 60 * 45,
  last_result: "ok",
};

// 自动封禁阈值:形态与后端 autoban.Config / DefaultConfig() 一致(秒)。
const initialAutoban = {
  enabled: true,
  tamper:        { max: 3,  window_sec: 1800, ttl_sec: 0 },
  heartbeat:     { max: 90, window_sec: 60,   ttl_sec: 86400 },
  resource:      { max: 30, window_sec: 300,  ttl_sec: 86400 },
  captcha:       { max: 3,  window_sec: 900,  ttl_sec: 3600 },
  multi_account: { max: 5,  window_sec: 1800, ttl_sec: 86400 },
};

/* ---------- Accounts ---------- */
const ACCOUNT_NAMES = [
  "kyubey_no_thanks","homurahomerun","sayaka_apple","madoka_g","mami_t",
  "kyoko_redhead","nagisa_cheese","yachiyo_n","tsuruno_yui","felicia_m",
  "iroha_t","kuroe","kanagi","alina_g","sana_f","mifuyu_a","rena_m",
  "momoko_t","tart_pucell","ria_d","mitama_y","kako_n","yuna_k",
  "ui_t","touka_s","nemu_h","mel_a","seika_h","ren_i","ai_k",
];
const initialAccounts = ACCOUNT_NAMES.map((u, i) => ({
  id: `acc_${1000 + i}`,
  username: u,
  email: `${u}@user.example.com`,
  createdAt: NOW - 1000 * 60 * 60 * 24 * (3 + (i * 7) % 280),
  lastLoginAt: NOW - 1000 * 60 * (5 + (i * 31) % 4000),
  status: i === 3 || i === 17 ? "disabled" : "active",
}));

/* ---------- Bans ---------- */
function makeDeviceId(seed) {
  const hex = "0123456789abcdef";
  let s = "";
  let x = seed;
  for (let i = 0; i < 40; i++) {
    x = (x * 9301 + 49297) % 233280;
    s += hex[Math.floor(x / 233280 * 16)];
  }
  return s;
}

const BAN_REASONS = [
  "多账号异常切换",
  "心跳包高频伪造",
  "客户端篡改检测命中",
  "异常资源请求频率",
  "未通过 cap-worker 校验 3 次",
  "管理员手工封禁",
];

const initialActiveBans = Array.from({ length: 17 }, (_, i) => ({
  id: `ban_${2000 + i}`,
  deviceId: makeDeviceId(i + 11),
  reason: BAN_REASONS[i % BAN_REASONS.length],
  issuedAt: NOW - 1000 * 60 * (10 + i * 90),
  expireTime: i % 5 === 0 ? null : NOW + 1000 * 60 * 60 * (3 + i * 17),
  issuedBy: i % 3 === 0 ? "system" : (i % 2 === 0 ? "admin_homura" : "admin_madoka"),
  auto: i % 3 === 0,
}));

const initialBanHistory = Array.from({ length: 26 }, (_, i) => ({
  id: `bh_${3000 + i}`,
  deviceId: makeDeviceId(i + 200),
  reason: BAN_REASONS[(i + 2) % BAN_REASONS.length],
  issuedAt: NOW - 1000 * 60 * 60 * 24 * (1 + i * 2),
  expiredAt: NOW - 1000 * 60 * 60 * (i % 24),
  issuedBy: i % 4 === 0 ? "system" : "admin_homura",
  auto: i % 4 === 0,
  liftedBy: i % 5 === 0 ? "admin_madoka" : null,
}));

/* ---------- Heartbeat (live) ----------
 * 形状对齐 /admin/heartbeats(见 api.jsx fetchHeartbeats):
 *   type=online|hotupdate|game;下载阶段(online/hotupdate)有逐文件
 *   {name,status,percent,speedBps},游戏阶段(game)files 为空、无进度与速度。
 * 离线整包由浏览器下载、客户端不上报心跳,故 MOCK 里也不存在该类。
 */
const HB_FILES = [
  "scenario/main_chapter_8.dat",
  "scenario/main_chapter_9.dat",
  "movie/op_2026_spring.mp4",
  "card/mitama_yakumo_4star.bin",
  "card/iroha_5star_arcana.bin",
  "audio/bgm_battle_extra.acb",
  "audio/voice_kyoko_2026.acb",
  "live2d/homura_school.moc3",
  "ui/event_2026_fes.atlas",
  "movie/ed_arc_2.mp4",
];

function hbFile(name, status, percent, speedBps) {
  return { name, status, percent, speedBps };
}

// 与服务端 aggregateHBFiles 同口径:进度=各文件 percent 均值,速度=下载中文件
// speedBps 之和,当前文件=首个 downloading(回退首个未完成)。
function aggregateMockFiles(files) {
  if (!files.length) return { progress: 0, speedBps: 0, currentFile: "" };
  let sum = 0, speed = 0, cur = "";
  for (const f of files) {
    sum += f.percent;
    if (f.status === "downloading") { speed += f.speedBps; if (!cur) cur = f.name; }
  }
  if (!cur) { const u = files.find(f => f.status !== "done"); cur = u ? u.name : ""; }
  return { progress: Math.round((sum / files.length) * 10) / 10, speedBps: speed, currentFile: cur };
}

function makeLiveDevice(i) {
  // 类型分布:大部分在线下载,少量热更新,少量在线游戏(无下载)。
  const GAME_IDX = [2, 7, 10];
  const HOT_IDX = [4, 9];
  let type = "online";
  if (GAME_IDX.includes(i)) type = "game";
  else if (HOT_IDX.includes(i)) type = "hotupdate";

  let files = [];
  if (type === "hotupdate") {
    // 热更新固定两文件:js 已完成,scenario 下载中。
    files = [
      hbFile("cn_js_update.zip", "done", 100, 0),
      hbFile("cn_scenario_update.zip", "downloading", 30 + ((i * 13) % 60), (300 + ((i * 47) % 1500)) * 1024),
    ];
  } else if (type === "online") {
    const total = 6 + (i % 4);
    const done = Math.floor((i * 0.7) % total);
    files = Array.from({ length: total }, (_, k) => {
      const name = HB_FILES[(i + k) % HB_FILES.length];
      if (i === 1 && k === 2) return hbFile(name, "failed", 12 + k, 0);
      if (k < done) return hbFile(name, "done", 100, 0);
      if (k === done) return hbFile(name, "downloading", 5 + ((i * 17 + k * 9) % 85), (200 + ((i * 53) % 1800)) * 1024);
      return hbFile(name, "pending", 0, 0);
    });
  }
  const agg = aggregateMockFiles(files);
  return {
    id: `live_${i}`,
    deviceId: makeDeviceId(500 + i),
    type,
    phase: type === "game" ? "game" : "download",
    progress: agg.progress,
    speedBps: agg.speedBps,
    currentFile: agg.currentFile,
    lastHeartbeat: NOW - 1000 * (i % 8),
    files,
  };
}
const initialHeartbeats = Array.from({ length: 12 }, (_, i) => makeLiveDevice(i));

/* ---------- Audit log ---------- */
const ACTION_TYPES = [
  "server.status.change","feature.toggle","version.allowed.add","version.allowed.remove",
  "version.fake.update","mirror.reorder","mirror.add","offline.publish",
  "hotupdate.js.publish","hotupdate.scn.publish","account.create","account.disable","account.reset_pwd","account.delete",
  "device.ban","device.lift","captcha.config.update","captcha.test",
];

// 每个样本的 type/target/details 与服务端 h.audit(...) 实际写入的形态对齐,
// 这样审计页按 type 翻译操作名、按属性展示目标(如离线包版本)时,离线/截图预览也真实。
// system=自动触发(自动打包 / 心跳风控),其余为管理员控制台操作。
const AUDIT_SAMPLES = [
  { type: "offline.publish",        target: "2.4.0",  details: { sha256_prefix: "a1b2c3d4e5f6", size: 824 * 1024 * 1024 } },
  { type: "hotupdate.js.publish",   target: "v48" },
  { type: "hotupdate.scn.publish",  target: "v24" },
  { type: "server.status.change",   target: "maintenance", details: { message: "例行维护,预计 30 分钟", estimated_end: NOW + 30 * 60000 } },
  { type: "server.status.change",   target: "ok" },
  { type: "feature.toggle",         target: "",       details: { online: true, offline: true, account: false } },
  { type: "version.allowed.add",    target: "1.4.7" },
  { type: "version.allowed.remove", target: "1.4.5" },
  { type: "version.fake.update",    target: "" },
  { type: "mirror.reorder",         target: "",       details: { count: 5 } },
  { type: "mirror.add",             target: "线路C · CDN 东京" },
  { type: "services.update",        target: "",       details: { cap_worker_url: "https://captcha.magireco.top", game_server_host: "game.magi-reco.top", proxy_backends: 2 } },
  { type: "account.create",         target: "iroha_tamaki" },
  { type: "account.disable",        target: "spam_acct_77" },
  { type: "account.reset_pwd",      target: "yachiyo_n" },
  { type: "account.delete",         target: "dup_acct_12" },
  { type: "device.ban",             target: makeDeviceId(3).slice(0, 16), details: { reason: "多开 / 改包检测" } },
  { type: "device.lift",            target: makeDeviceId(7).slice(0, 16) },
  { type: "device.ban",             target: makeDeviceId(9).slice(0, 16), system: true, details: { reason: "下载速率异常", source: "heartbeat" } },
  { type: "captcha.config.update",  target: "",       details: { enabled: true, difficulty: 12 } },
  { type: "captcha.test",           target: "",       system: true, details: { result: "通过" } },
  { type: "admin.create",           target: "admin_kyoko",   details: { role: "admin" } },
  { type: "admin.role.update",      target: "audit_sayaka",  details: { role: "readonly" } },
  { type: "admin.remove",           target: "retired_admin" },
  { type: "task.interval.update",   target: "" },
  { type: "pipeline.manual_sync",   target: "",       system: true },
  { type: "offline.auto_package.update", target: "" },
];

const initialAuditLog = Array.from({ length: 84 }, (_, i) => {
  const s = AUDIT_SAMPLES[i % AUDIT_SAMPLES.length];
  return {
    id: `log_${10000 + i}`,
    ts: NOW - 1000 * 60 * (i * 23 + 2),
    actor: s.system ? "system" : (i % 2 === 0 ? "admin_homura" : "admin_madoka"),
    type: s.type,
    target: s.target,
    details: { ip: `10.${(i * 7) % 256}.${(i * 13) % 256}.${(i * 31) % 256}`, ...(s.details || {}) },
  };
});

/* ---------- Recent events for dashboard ---------- */
const EVENT_LIST = [
  { kind: "login",   text: "玩家 iroha_t 登录",            actor: "iroha_t",       icon: "User",    color: "teal" },
  { kind: "ban",     text: "设备 ab48…f02e 被自动封禁",     actor: "system",         icon: "Ban",     color: "red" },
  { kind: "publish", text: "热更新 JS Bundle v47 推送",     actor: "admin_homura",   icon: "Zap",     color: "purple" },
  { kind: "login",   text: "玩家 yachiyo_n 登录",          actor: "yachiyo_n",     icon: "User",    color: "teal" },
  { kind: "lift",    text: "设备 91dc…0cae 解除封禁",       actor: "admin_madoka",   icon: "Shield",  color: "teal" },
  { kind: "publish", text: "剧本 Bundle v23 推送",          actor: "admin_homura",   icon: "Zap",     color: "purple" },
  { kind: "ban",     text: "设备 4f23…91ab 被管理员封禁",   actor: "admin_homura",   icon: "Ban",     color: "amber" },
  { kind: "login",   text: "玩家 felicia_m 登录",          actor: "felicia_m",     icon: "User",    color: "teal" },
  { kind: "publish", text: "新离线包 2.4.0 发布",           actor: "admin_madoka",   icon: "Package", color: "purple" },
  { kind: "login",   text: "玩家 nagisa_cheese 登录",       actor: "nagisa_cheese", icon: "User",    color: "teal" },
].map((e, i) => ({ ...e, id: `evt_${i}`, ts: NOW - 1000 * 60 * (i * 7 + 3) }));

/* ---------- 24h session sparkline ---------- */
const sessions24h = Array.from({ length: 24 }, (_, h) => {
  const base = 280;
  const wave = Math.sin((h / 24) * Math.PI * 2 + 1.2) * 110 + 110;
  const noise = (Math.sin(h * 13.37) + 1) * 40;
  return Math.round(base + wave + noise);
});

/* ---------- Export ---------- */
Object.assign(window, {
  MOCK: {
    NOW,
    initialServerState,
    initialVersions,
    initialServices,
    initialMirrors,
    initialNodes,
    initialPipeline,
    initialAutoPackage,
    initialTasks,
    initialUnverifiedPurge,
    initialLimits,
    initialCurrentAdmin,
    initialAdminRoster,
    ROLE_LABELS,
    initialOfflinePackage,
    initialJsBundle,
    initialScenarioBundle,
    initialCaptcha,
    initialAutoban,
    initialAccounts,
    initialActiveBans,
    initialBanHistory,
    initialHeartbeats,
    initialAuditLog,
    EVENT_LIST,
    sessions24h,
    ACTION_TYPES,
    BAN_REASONS,
    HB_FILES,
    makeDeviceId,
  },
});

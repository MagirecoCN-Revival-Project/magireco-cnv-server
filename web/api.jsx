/* Web 前端的 API 客户端 + reducer 适配层。
 *
 * 设计:页面 JSX 仍然调用 dispatch({type:"server.set", patch}),
 * 我们用 wrapDispatch 包一层,把每个 type 映射到对应 HTTP 调用。
 * Reducer 本地立即生效(乐观更新),失败弹错并按需回滚。
 *
 * 鉴权:cookie mr_session 自动随同源请求带上;localStorage 兜底存
 * Bearer token,服务端有 cookie 后会忽略 Bearer。
 */
(function () {
  const TOKEN_KEY = "mr_token";

  // API 基址:面板托管前端时由 /app-config.js 注入 window.MR_API_BASE,指向业务节点;
  // 为空(同源部署 / 节点回落)时所有请求走相对路径。绝对 URL(http 开头)原样透传。
  const API_BASE = ((typeof window !== "undefined" && window.MR_API_BASE) || "").replace(/\/+$/, "");
  function apiURL(path) {
    if (/^https?:\/\//i.test(path)) return path;
    return API_BASE + path;
  }

  const Api = {
    getToken() {
      try { return localStorage.getItem(TOKEN_KEY) || ""; } catch (_) { return ""; }
    },
    setToken(t) {
      try { if (t) localStorage.setItem(TOKEN_KEY, t); else localStorage.removeItem(TOKEN_KEY); } catch (_) {}
    },

    async request(method, path, body) {
      const headers = { "Accept": "application/json" };
      const t = Api.getToken();
      if (t) headers["Authorization"] = "Bearer " + t;
      let init = { method, headers, credentials: "same-origin" };
      if (body !== undefined && body !== null) {
        headers["Content-Type"] = "application/json";
        init.body = JSON.stringify(body);
      }
      const resp = await fetch(apiURL(path), init);
      const txt = await resp.text();
      let data = null;
      try { data = txt ? JSON.parse(txt) : null; } catch (_) { data = { raw: txt }; }
      if (resp.status === 401 && !path.startsWith("/auth/")) {
        Api.setToken("");
        if (!location.pathname.endsWith("login.html")) {
          location.href = "login.html";
        }
        throw new Error("unauthorized");
      }
      if (!resp.ok) {
        // 服务端 respond.Fail 规范形状:{success:false, error:"<code>", message:"<msg>"}。
        // error 是字符串错误码、message 在顶层(早期误当对象读 .message 会丢信息)。
        // 仍兼容 error 为 {code,message} 对象的历史形态。
        const code = (data && (typeof data.error === "string" ? data.error : (data.error && data.error.code))) || ("http_" + resp.status);
        const message = (data && (data.message || (data.error && data.error.message))) || resp.statusText;
        const e = new Error(message || code);
        e.code = code;
        e.status = resp.status;
        e.data = data;
        throw e;
      }
      return data;
    },

    get(p) { return Api.request("GET", p); },
    post(p, b) { return Api.request("POST", p, b || {}); },
    put(p, b) { return Api.request("PUT", p, b || {}); },
    patch(p, b) { return Api.request("PATCH", p, b || {}); },
    del(p) { return Api.request("DELETE", p); },
  };

  /* ── PoW 验证码:浏览器内求解 ──────────────────────────────────────
   * 协议:POST /api/challenge → { token, challenge:{c,s,d}, expires }
   * 对每个 i∈[0,c) 求最小 nonce 使 SHA-256(token+"."+i+"."+nonce) 前 d 位为 0。
   * POST /api/redeem { token, solutions } → { success, token }
   */
  async function sha256Bytes(str) {
    const buf = new TextEncoder().encode(str);
    const digest = await crypto.subtle.digest("SHA-256", buf);
    return new Uint8Array(digest);
  }
  function leadingZeroBits(bytes) {
    let n = 0;
    for (let i = 0; i < bytes.length; i++) {
      const b = bytes[i];
      if (b === 0) { n += 8; continue; }
      for (let m = 0x80; m > 0; m >>= 1) {
        if (b & m) return n;
        n++;
      }
      break;
    }
    return n;
  }
  async function solveOne(token, i, d) {
    for (let nonce = 0; ; nonce++) {
      const h = await sha256Bytes(token + "." + i + "." + nonce);
      if (leadingZeroBits(h) >= d) return nonce;
      // 让出主线程
      if ((nonce & 1023) === 0) await new Promise(r => setTimeout(r, 0));
    }
  }
  async function solveCapToken(onProgress) {
    const ch = await Api.post("/api/challenge", {});
    const { c, d } = ch.challenge;
    const sols = [];
    for (let i = 0; i < c; i++) {
      if (onProgress) onProgress(i, c);
      sols.push(await solveOne(ch.token, i, d));
    }
    const res = await Api.post("/api/redeem", { token: ch.token, solutions: sols });
    if (!res || !res.success) throw new Error("captcha_failed");
    return res.token;
  }

  /* ── 管理后台 reducer dispatch 中间件 ─────────────────────────────
   * 用法:
   *   const [state, rawDispatch] = useReducer(appReducer, initialState);
   *   const stateRef = useRef(state); useEffect(()=>{stateRef.current=state;},[state]);
   *   const dispatch = wrapDispatch(rawDispatch, () => stateRef.current, toast);
   *
   * mirrors.* 这一类需要拿"动作执行后的状态"再 PUT 整张表的 action,通过
   * getState() 拿到 reducer 已经更新过的 state。
   */
  function wrapDispatch(rawDispatch, getState, toast) {
    return (action) => {
      rawDispatch(action);
      // queueMicrotask 等 React commit 完(reducer 已运行,getState 才能拿到新值)
      Promise.resolve().then(() =>
        adminApiCall(action, getState && getState()),
      ).catch(err => {
        if (toast) toast("操作失败: " + (err.message || err.code || "未知错误"), "err");
      });
    };
  }

  async function adminApiCall(action, state) {
    const t = action.type;
    switch (t) {
      case "server.set":     return Api.put("/admin/server/status", action.patch);
      case "versions.set":   return Api.put("/admin/versions", action.value);
      case "services.set":   return Api.put("/admin/services", action.patch);
      case "mirrors.add":
      case "mirrors.remove":
      case "mirrors.reorder":
      case "mirrors.update": {
        const list = (state && state.mirrors) || [];
        return Api.put("/admin/mirrors", { mirrors: list });
      }
      case "mirrors.setLimits":
        return Api.put("/admin/mirrors/limits", {
          url: action.url,
          daily_limit_bytes: action.dailyLimitBytes,
          speed_limit_bps: action.speedLimitBps,
        });
      case "pipeline.set":           return Api.put("/admin/pipeline", action.patch);
      case "pipeline.triggerSync":   return Api.post("/admin/pipeline/sync");
      case "autoPackage.set":        return Api.put("/admin/auto-package", action.patch);
      case "tasks.set":              return Api.put("/admin/tasks", action.patch);
      case "unverifiedPurge.set":    return Api.put("/admin/unverified-purge", action.patch);
      case "limits.set":             return Api.put("/admin/limits", { global_body_mb: action.value.globalBodyMB, hotupdate_mb: action.value.hotUpdateMB });
      case "admin.add":              return Api.post("/admin/admins", action.value);
      case "admin.update":           return Api.patch("/admin/admins/" + action.id, action.patch);
      case "admin.remove":           return Api.del("/admin/admins/" + action.id);
      case "offlinePackage.set":     return Api.put("/admin/offline-package", action.value);
      case "jsBundle.set":           return Api.post("/admin/hot-update/js/publish", action.value);
      case "scenarioBundle.set":     return Api.post("/admin/hot-update/scenario/publish", action.value);
      case "captcha.set":            return Api.put("/admin/captcha", action.patch);
      case "captcha.test":           return Api.post("/admin/captcha/test");
      case "autoban.set":            return Api.put("/admin/autoban", action.value);
      case "accounts.add":           return Api.post("/admin/accounts", action.value);
      case "accounts.update":        return Api.patch("/admin/accounts/" + action.id, action.patch);
      case "accounts.remove":        return Api.del("/admin/accounts/" + action.id);
      case "bans.add":               return Api.post("/admin/bans", {
        device_id: action.value.deviceId, reason: action.value.reason, expire_time: action.value.expireTime,
      });
      case "bans.lift":              return Api.del("/admin/bans/" + action.id);
      case "heartbeat.switchMirror": return Api.post("/admin/heartbeats/" + action.deviceId + "/switch-mirror", action.assignment);
      case "heartbeat.ban":          return Api.post("/admin/heartbeats/" + action.deviceId + "/ban", action.reason);
      // hydrate / 客户端无副作用的 action 直接跳过
      default: return;
    }
  }

  /* 把后端 bans 行(snake_case)归一成设备封禁页用的 camelCase。
   * 历史记录的"结束时间"取 lifted_at(被解除)否则 expire_time(到期)。 */
  function mapBan(b) {
    return {
      id: b.id,
      deviceId: b.device_id || "",
      reason: b.reason || "",
      issuedAt: b.issued_at,
      expireTime: b.expire_time ?? null,
      issuedBy: b.issued_by || "system",
      auto: !!b.auto,
      expiredAt: b.lifted_at || b.expire_time || b.issued_at,
      liftedBy: b.lifted_by || null,
    };
  }

  // mapBundleState 把 /admin/state 的 hot_bundles 项(PascalCase)归一成热更页用的
  // camelCase。无已发布版本时后端返回 {Version:0, Size:-1},映射为 v0 占位。
  function mapBundleState(b) {
    b = b || {};
    const sizeRaw = b.Size != null ? b.Size : b.size;
    return {
      version: b.Version != null ? b.Version : (b.version || 0),
      sha256: b.SHA256 || b.sha256 || "",
      size: sizeRaw != null && sizeRaw >= 0 ? sizeRaw : 0,
      url: b.DownloadURL || b.download_url || b.url || "",
      publishedAt: b.PublishedAt != null ? b.PublishedAt
        : (b.published_at != null ? b.published_at : (b.publishedAt || null)),
    };
  }

  /* ── /admin/state 拉取:把后端返回的聚合结构映射到 reducer initialState ── */
  async function fetchAdminState() {
    const s = await Api.get("/admin/state");
    // 兼容老格式:mirrors 早期是 string[],现在是 {kind,url,bucket?,region?}[]
    const mirrors = (s.mirrors || []).map(m =>
      typeof m === "string" ? { kind: "http", url: m } : m
    );
    return {
      server: s.server || { status: "ok" },
      // API 使用 allowed_versions (snake_case)，前端 JSX 用 allowed；在此统一映射。
      versions: s.versions
        ? { ...s.versions, allowed: s.versions.allowed_versions || [] }
        : { allowed: [] },
      services: s.services || { cap_worker_url: "", proxy_backends: [], game_server_host: "" },
      mirrors,
      nodes: (function() {
        const def = { services: [], maintenance: false, status: "ok",
          uptimeSec: 0, cpuPct: 0, memPct: 0, activeConns: 0, qps: 0,
          activeDownloads: 0, egressBps: 0, upstreamLatencyMs: 0, cacheHitRate: 0 };
        const n = s.nodes || {};
        return {
          primary:   { ...def, ...(n.primary   || {}) },
          secondary: { ...def, ...(n.secondary || {}) },
        };
      })(),
      pipeline: s.pipeline
        ? { ...s.pipeline, releases: (s.pipeline.releases && s.pipeline.releases.length > 0) ? s.pipeline.releases : [] }
        : { releases: [] },
      autoPackage: s.auto_package || {},
      tasks: s.tasks || {},
      currentAdmin: s.current_admin
        ? { ...s.current_admin, avatarLetter: (s.current_admin.username || "?")[0].toUpperCase() }
        : { username: "—", role: "admin", avatarLetter: "?" },
      adminRoster: s.admins || s.admin_roster || [],
      // 合并离线包元数据 + offline_pack 版本策略(min_version),便于前端
      // 一个对象就能完整展示与编辑。后端字段分两块下发(offline_package /
      // offline_pack),前端拼。
      offlinePackage: (() => {
        const op = s.offline_package || {};
        return {
          url:       op.url        || op.DownloadURL    || "",
          version:   op.version    || op.PackageVersion || "",
          sha256:    op.sha256     || op.SHA256         || "",
          size:      op.size       != null ? op.size : (op.Size ?? 0),
          uploadedAt: op.uploadedAt != null ? op.uploadedAt : (op.UploadedAt ?? null),
          min_version: (s.offline_pack && s.offline_pack.min_version) || op.min_version || "",
        };
      })(),
      // 后端 /admin/state 下发 hot_bundles:{js,scenario},store.HotBundle 无 json tag
      // 故为 PascalCase(Version/SHA256/DownloadURL/Size/PublishedAt);前端用 camelCase。
      jsBundle: mapBundleState((s.hot_bundles || {}).js),
      scenarioBundle: mapBundleState((s.hot_bundles || {}).scenario),
      captcha: s.captcha || {},
      autoban: s.autoban || {},
      accounts: s.accounts || [],
      // 后端 bans 走 snake_case;设备封禁页用 camelCase,这里归一(含自动封禁)。
      activeBans: (s.active_bans || []).map(mapBan),
      banHistory: (s.ban_history || []).map(mapBan),
      // 心跳是高频动态数据,不放进一次性的 /admin/state 聚合;由心跳页用
      // fetchHeartbeats() 每 5 秒单独轮询 /admin/heartbeats。此处不下发,
      // 从而保留 initialHeartbeats 作为首帧/离线演示的种子,不被清空。
      auditLog: s.audit || s.audit_log || [],
      events: s.events || [],
      unverifiedPurge: (() => {
        const up = s.unverified_purge || {};
        return {
          enabled:    up.enabled    || false,
          retainDays: up.retain_days != null ? up.retain_days : 30,
          scanSec:    up.scan_sec   != null ? up.scan_sec   : 86400,
        };
      })(),
      limits: (() => {
        const lm = s.limits || {};
        return {
          globalBodyMB: lm.global_body_mb != null ? lm.global_body_mb : 8,
          hotUpdateMB:  lm.hotupdate_mb   != null ? lm.hotupdate_mb   : 1024,
        };
      })(),
    };
  }

  // fetchHeartbeats 拉取在线设备心跳并归一成心跳页用的 camelCase 形状。
  // 后端 /admin/heartbeats 字段对齐客户端上报:type=online|hotupdate|game,
  // 下载阶段(online/hotupdate)有 progress/speed_bps/files;游戏阶段(game)
  // 这些为零值/空——客户端在游戏内只发空 files 心跳,无下载进度与速度。
  async function fetchHeartbeats() {
    const r = await Api.get("/admin/heartbeats");
    return (r.heartbeats || []).map(hb => ({
      id:           hb.device_id,
      deviceId:     hb.device_id,
      type:         hb.type || (hb.phase === "game" ? "game" : "online"),
      phase:        hb.phase || "game",
      progress:     hb.progress || 0,
      speedBps:     hb.speed_bps || 0,
      currentFile:  hb.current_file || "",
      lastHeartbeat: hb.last_heartbeat || Date.now(),
      files: (hb.files || []).map(f => ({
        name: f.name, status: f.status,
        percent: f.percent || 0, speedBps: f.speed_bps || 0,
      })),
    }));
  }

  /* ── 热更新包发布:两种来源,服务端都自托管并回算 sha256/size/url ────────
   * kind: "js" | "scenario"(注意:页面里 scenario 标签用 "scn",调用前需转成 "scenario")
   * 返回归一后的 bundle:{version, sha256, size, url, publishedAt}
   */
  function mapHotBundle(r) {
    return {
      version: r.version,
      sha256: r.sha256 || "",
      size: r.size || 0,
      url: r.download_url || "",
      publishedAt: r.published_at || Date.now(),
    };
  }
  // 直链:服务端下载该 URL → 校验 zip → 自托管。
  async function publishHotBundle(kind, downloadURL) {
    const r = await Api.post("/admin/hot-update/" + kind + "/publish", { download_url: downloadURL });
    return mapHotBundle(r);
  }
  // 上传:multipart 上传 zip → 服务端校验 → 自托管。不能走 Api.request(它 JSON 序列化)。
  async function uploadHotBundle(kind, file) {
    const fd = new FormData();
    fd.append("bundle", file);
    const headers = { "Accept": "application/json" };
    const t = Api.getToken();
    if (t) headers["Authorization"] = "Bearer " + t;
    const resp = await fetch(apiURL("/admin/hot-update/" + kind + "/upload"), {
      method: "POST", headers, body: fd, credentials: "same-origin",
    });
    const txt = await resp.text();
    let data = null;
    try { data = txt ? JSON.parse(txt) : null; } catch (_) { data = { raw: txt }; }
    if (!resp.ok) {
      const code = (data && (typeof data.error === "string" ? data.error : (data.error && data.error.code))) || ("http_" + resp.status);
      const message = (data && (data.message || (data.error && data.error.message))) || resp.statusText;
      const e = new Error(message || code);
      e.code = code; e.status = resp.status;
      throw e;
    }
    return mapHotBundle(data);
  }

  // fetchMirrorStats 单独拉取带 stats 字段的镜像列表（5 秒轮询用）。
  async function fetchMirrorStats() {
    const r = await Api.get("/admin/mirrors");
    return (r.mirrors || []).map(m =>
      typeof m === "string" ? { kind: "http", url: m } : m
    );
  }

  async function fetchUserState() {
    const [profile, devices, saves] = await Promise.all([
      Api.get("/user/api/profile"),
      Api.get("/user/api/devices"),
      Api.get("/user/api/saves"),
    ]);
    return { profile, devices: devices.devices || [], saves: saves.saves || [] };
  }

  Object.assign(window, {
    Api,
    solveCapToken,
    wrapDispatch,
    fetchAdminState,
    fetchHeartbeats,
    fetchMirrorStats,
    publishHotBundle,
    uploadHotBundle,
    fetchUserState,
  });
})();

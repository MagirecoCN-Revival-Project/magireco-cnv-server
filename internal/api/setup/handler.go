// Package setup 提供一次性安装向导，帮助首次部署的管理员完成初始配置。
//
// 安全设计：
//   - 仅在未完成初始化时可访问（config 表 setup_complete=true 后所有路由返回 404）
//   - 已存在超级管理员时同样拒绝访问（双重保护）
//   - 完成安装后立即在 DB 中设置 setup_complete=true，使后续任何访问均失效
//   - 向导本身不依赖外部文件，所有 HTML 内嵌在二进制中，无法被替换
package setup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/api/respond"
	"magirecocn-revival/api-server/internal/auth"
	"magirecocn-revival/api-server/internal/store"
)

// Handler 安装向导处理器。
type Handler struct {
	St *store.Store
}

// Routes 注册路由。每个请求都先过 setupGuard。
func (h *Handler) Routes(r chi.Router) {
	r.Use(h.setupGuard)
	r.Get("/", h.page)
	r.Post("/api/check", h.apiCheck)
	r.Post("/api/complete", h.apiComplete)
}

// setupGuard 若安装已完成或已有超管账号，返回 404 并终止请求链。
func (h *Handler) setupGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.isSetupComplete(r) {
			respond.Fail(w, http.StatusNotFound, "not_found", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isSetupComplete 检查 DB 标志或是否已有超管账号。
func (h *Handler) isSetupComplete(r *http.Request) bool {
	var flag struct{ Done bool `json:"done"` }
	if ok, _ := h.St.ConfigGet(r.Context(), "setup_state", &flag); ok && flag.Done {
		return true
	}
	n, err := h.St.AdminCountSuper(r.Context())
	return err == nil && n > 0
}

// page 返回内嵌的安装向导 HTML。
func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, setupHTML)
}

// checkReq 用于 /setup/api/check：验证给定凭证能否连通（当前仅返回 DB 状态）。
type checkReq struct {
	SMTPHost string `json:"smtp_host"`
	SMTPPort string `json:"smtp_port"`
	SMTPUser string `json:"smtp_user"`
	SMTPPass string `json:"smtp_pass"`
	SMTPFrom string `json:"smtp_from"`
}

// apiCheck 测试当前 DB 连通性，以及（可选）SMTP 配置是否有效。
func (h *Handler) apiCheck(w http.ResponseWriter, r *http.Request) {
	if h.isSetupComplete(r) {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
		return
	}
	var req checkReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	// DB 可用性探测：尝试读取 config 表（若失败返回错误）。
	dbOK := true
	if _, err := h.St.AdminCountSuper(r.Context()); err != nil {
		dbOK = false
	}
	respond.OKRaw(w, map[string]any{
		"db_ok":      dbOK,
		"smtp_setup": req.SMTPHost != "",
	})
}

type completeReq struct {
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password"`
	AdminUsername string `json:"admin_username"`
}

// apiComplete 创建第一个超级管理员并将安装向导标记为已完成。
// 完成后所有 /setup/* 路由立即返回 404。
func (h *Handler) apiComplete(w http.ResponseWriter, r *http.Request) {
	if h.isSetupComplete(r) {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
		return
	}
	var req completeReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if !isValidEmail(req.AdminEmail) {
		respond.Fail(w, http.StatusBadRequest, "bad_email", "邮箱格式不正确")
		return
	}
	if len(req.AdminPassword) < 12 {
		respond.Fail(w, http.StatusBadRequest, "weak_password", "超级管理员密码至少 12 位")
		return
	}
	username := req.AdminUsername
	if username == "" {
		username = strings.SplitN(req.AdminEmail, "@", 2)[0]
	}
	hash, err := auth.HashPassword(req.AdminPassword)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	adminID := "adm-" + hex.EncodeToString(idBytes)
	now := time.Now().UnixMilli()
	if err := h.St.AdminInsert(r.Context(), store.Admin{
		ID: adminID, Username: username, Email: req.AdminEmail,
		PasswordHash: hash, Role: "super_admin", CreatedAt: now,
	}); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "创建管理员失败")
		return
	}
	// 标记安装完成——此后 setupGuard 立即拦截所有 /setup/* 请求。
	_ = h.St.ConfigSet(r.Context(), "setup_state", map[string]any{"done": true})
	respond.OKRaw(w, map[string]any{
		"success":  true,
		"admin_id": adminID,
		"message":  "安装完成！安装向导已自动关闭，请使用管理员账号登录。",
	})
}

func isValidEmail(s string) bool {
	parts := strings.Split(s, "@")
	return len(parts) == 2 && len(parts[0]) > 0 && strings.Contains(parts[1], ".")
}

// setupHTML 安装向导页面（内嵌，不依赖任何外部文件）。
var setupHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>安装向导 · 魔法纪录复兴计划服务端</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'IBM Plex Sans SC','PingFang SC',sans-serif;background:linear-gradient(135deg,#e2f0ff,#d8f7f0 55%,#f0fbfb);min-height:100vh;display:flex;align-items:center;justify-content:center;padding:20px}
.card{background:#fff;border-radius:16px;box-shadow:0 8px 40px rgba(0,0,0,.12);padding:40px;width:100%;max-width:520px}
h1{font-size:22px;font-weight:700;color:#1a1a2e;margin-bottom:4px}
.sub{font-size:13px;color:#666;margin-bottom:28px}
.step{display:none}.step.active{display:block}
.field{margin-bottom:16px}
label{display:block;font-size:13px;font-weight:500;color:#333;margin-bottom:6px}
input{width:100%;padding:9px 12px;border:1.5px solid #dde1e7;border-radius:8px;font-size:14px;outline:none;transition:border-color .2s}
input:focus{border-color:#d6336c}
.hint{font-size:11.5px;color:#888;margin-top:4px}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:6px;padding:10px 20px;border:none;border-radius:8px;font-size:14px;font-weight:600;cursor:pointer;transition:background .2s}
.btn-primary{background:#d6336c;color:#fff}.btn-primary:hover{background:#b52f5e}
.btn-sec{background:#f0f0f5;color:#333}.btn-sec:hover{background:#e2e2eb}
.btn:disabled{opacity:.5;cursor:not-allowed}
.row{display:flex;gap:10px;justify-content:flex-end;margin-top:24px}
.err{background:#fff0f3;border:1px solid rgba(220,38,38,.3);color:#c0392b;padding:10px 14px;border-radius:8px;font-size:13px;margin-bottom:16px}
.ok{background:#f0fdf4;border:1px solid rgba(34,197,94,.3);color:#166534;padding:10px 14px;border-radius:8px;font-size:13px;margin-bottom:16px}
.warn{background:#fffbeb;border:1px solid rgba(234,179,8,.4);color:#854d0e;padding:10px 14px;border-radius:8px;font-size:13px;margin-bottom:16px}
.progress{display:flex;gap:0;margin-bottom:28px}
.progress-step{flex:1;padding:8px 0;text-align:center;font-size:12px;font-weight:500;color:#aaa;border-bottom:3px solid #eee;cursor:default}
.progress-step.done{color:#d6336c;border-color:#d6336c}
.badge{display:inline-block;background:#f3f4f6;color:#374151;border-radius:6px;padding:2px 8px;font-size:12px;font-family:monospace}
</style>
</head>
<body>
<div class="card">
  <h1>🔧 安装向导</h1>
  <div class="sub">魔法纪录复兴计划 · 服务端首次配置</div>

  <div class="progress">
    <div class="progress-step done" id="ps1">① 欢迎</div>
    <div class="progress-step" id="ps2">② 系统检查</div>
    <div class="progress-step" id="ps3">③ 创建管理员</div>
    <div class="progress-step" id="ps4">④ 完成</div>
  </div>

  <!-- 步骤 1：欢迎 -->
  <div class="step active" id="step1">
    <p style="font-size:14px;line-height:1.8;color:#444;margin-bottom:16px">
      欢迎使用服务端安装向导。本向导将引导您完成以下配置：
    </p>
    <ul style="font-size:13px;color:#555;line-height:2;padding-left:20px;margin-bottom:20px">
      <li>检查数据库连接与基础组件状态</li>
      <li>创建超级管理员账号</li>
    </ul>
    <div class="warn">
      ⚠️ <strong>安全提示：</strong>安装完成后向导将<strong>立即自动关闭</strong>，无法再次访问。
      请妥善保管管理员凭证，忘记密码需通过命令行工具重置。
    </div>
    <div class="row">
      <button class="btn btn-primary" onclick="goStep(2)">开始 →</button>
    </div>
  </div>

  <!-- 步骤 2：系统检查 -->
  <div class="step" id="step2">
    <p style="font-size:14px;color:#444;margin-bottom:18px">正在检查系统状态…</p>
    <div id="check-result"></div>
    <div class="row">
      <button class="btn btn-sec" onclick="goStep(1)">← 返回</button>
      <button class="btn btn-primary" id="btn-check" onclick="doCheck()">检查</button>
    </div>
  </div>

  <!-- 步骤 3：创建管理员 -->
  <div class="step" id="step3">
    <div class="err" id="admin-err" style="display:none"></div>
    <div class="field">
      <label>管理员邮箱 <span style="color:#e03">*</span></label>
      <input type="email" id="admin-email" placeholder="admin@example.com"/>
    </div>
    <div class="field">
      <label>用户名（可选，留空自动从邮箱生成）</label>
      <input type="text" id="admin-username" placeholder="admin"/>
    </div>
    <div class="field">
      <label>密码 <span style="color:#e03">*</span>（至少 12 位）</label>
      <input type="password" id="admin-pass" placeholder="强密码，建议 16 位以上"/>
      <div class="hint">建议使用密码管理器生成随机密码并妥善保管</div>
    </div>
    <div class="field">
      <label>确认密码 <span style="color:#e03">*</span></label>
      <input type="password" id="admin-pass2"/>
    </div>
    <div class="row">
      <button class="btn btn-sec" onclick="goStep(2)">← 返回</button>
      <button class="btn btn-primary" id="btn-complete" onclick="doComplete()">完成安装</button>
    </div>
  </div>

  <!-- 步骤 4：完成 -->
  <div class="step" id="step4">
    <div class="ok">✅ 安装完成！超级管理员账号已创建。</div>
    <p style="font-size:14px;color:#444;line-height:1.8;margin-bottom:16px">
      安装向导已<strong>自动关闭</strong>，后续访问此地址将返回 404。<br/>
      请使用管理员账号登录管理后台。
    </p>
    <div class="warn">
      📋 请在关闭此页面前记下您的管理员邮箱：
      <br/><span class="badge" id="done-email"></span>
    </div>
    <div class="row">
      <a href="/"><button class="btn btn-primary">前往登录 →</button></a>
    </div>
  </div>
</div>

<script>
let currentStep = 1;
function goStep(n) {
  document.getElementById('step' + currentStep).classList.remove('active');
  currentStep = n;
  document.getElementById('step' + n).classList.add('active');
  for (let i = 1; i <= 4; i++) {
    const el = document.getElementById('ps' + i);
    if (i <= n) el.classList.add('done'); else el.classList.remove('done');
  }
  if (n === 2) doCheck();
}

async function doCheck() {
  const btn = document.getElementById('btn-check');
  const res = document.getElementById('check-result');
  btn.disabled = true;
  res.innerHTML = '<div style="font-size:13px;color:#666">检查中…</div>';
  try {
    const r = await fetch('/setup/api/check', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({})
    });
    const data = await r.json();
    if (data.db_ok) {
      res.innerHTML = '<div class="ok">✅ 数据库连接正常。</div>' +
        (data.smtp_setup ? '' : '<div class="warn">⚠️ SMTP 未配置——邮箱验证码将仅在 <code>CNV_EMAIL_DEV_MODE=1</code> 时打印到日志。<br/>可在安装完成后设置 <code>CNV_SMTP_*</code> 环境变量并重启服务端。</div>');
      document.getElementById('btn-check').textContent = '下一步 →';
      document.getElementById('btn-check').onclick = () => goStep(3);
    } else {
      res.innerHTML = '<div class="err">❌ 数据库异常：' + (data.message || '未知错误') + '</div>';
    }
  } catch(e) {
    res.innerHTML = '<div class="err">❌ 检查失败：' + e.message + '</div>';
  }
  btn.disabled = false;
}

async function doComplete() {
  const email = document.getElementById('admin-email').value.trim();
  const user  = document.getElementById('admin-username').value.trim();
  const pass  = document.getElementById('admin-pass').value;
  const pass2 = document.getElementById('admin-pass2').value;
  const errEl = document.getElementById('admin-err');
  errEl.style.display = 'none';

  if (!email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    errEl.textContent = '请输入有效的邮箱地址'; errEl.style.display = ''; return;
  }
  if (pass.length < 12) {
    errEl.textContent = '密码至少 12 位'; errEl.style.display = ''; return;
  }
  if (pass !== pass2) {
    errEl.textContent = '两次输入的密码不一致'; errEl.style.display = ''; return;
  }

  const btn = document.getElementById('btn-complete');
  btn.disabled = true; btn.textContent = '安装中…';
  try {
    const r = await fetch('/setup/api/complete', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({admin_email: email, admin_password: pass, admin_username: user})
    });
    const data = await r.json();
    if (data.success) {
      document.getElementById('done-email').textContent = email;
      goStep(4);
    } else {
      errEl.textContent = data.message || '安装失败'; errEl.style.display = '';
      btn.disabled = false; btn.textContent = '完成安装';
    }
  } catch(e) {
    errEl.textContent = '请求失败：' + e.message; errEl.style.display = '';
    btn.disabled = false; btn.textContent = '完成安装';
  }
}
</script>
</body>
</html>`

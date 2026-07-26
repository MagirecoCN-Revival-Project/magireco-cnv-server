// 默认运行时配置。空 API 基址 = 同源(节点回落 / 本地开发)。
// 面板托管前端时,此请求会命中面板的动态 /app-config.js 端点,
// 注入指向业务节点的 window.MR_API_BASE,从而让前端跨域直连节点 API。
window.MR_API_BASE = window.MR_API_BASE || "";

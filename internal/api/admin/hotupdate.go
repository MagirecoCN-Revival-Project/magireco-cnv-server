package admin

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"magirecocn-revival/cnv-server/internal/api/respond"
	"magirecocn-revival/cnv-server/internal/store"
)

// 热更新包发布:两种来源,殊途同归——都由服务端**自托管**(落盘到 CNV_HOTUPDATE_DIR、
// 经 CNV_HOTUPDATE_URL_PATH 对外分发),服务端计算 sha256/size 并校验 zip 格式正确。
//
//   - 直链(hotPublishURL):管理员填 download_url → 服务端下载到本地 → 校验 → 自托管。
//   - 上传(hotUpload):管理员直接上传 zip → 服务端校验 → 自托管。
//
// 客户端契约(/client/hot-update 响应 {version,sha256,download_url,size})不变:
// download_url 改为指向本节点 /dl/hot-update/<kind>-v<n>.zip,对客户端透明。

// hotHTTPClient 给"直链"模式下载远端包用。超时给足大包(剧本包可达数十 MiB)。
var hotHTTPClient = &http.Client{Timeout: 10 * time.Minute}

// hotPublishURL 直链模式:服务端 GET 管理员给的 URL,流式下载并交给 finalize 校验落盘。
//
// SSRF 说明:此端点仅 RequireWritableAdmin(受信任运维)可达,且只放行 http(s);
// 服务端会据此 URL 发起出站请求。部署在能访问内网的环境时,运维需自行确保只填可信
// 来源(见 docs/security 的下载来源说明)。
func (h *Handler) hotPublishURL(w http.ResponseWriter, r *http.Request, kind string) {
	if h.HotUpdate.Dir == "" {
		respond.Fail(w, http.StatusServiceUnavailable, "hotupdate_disabled", "未配置热更新托管目录(CNV_HOTUPDATE_DIR)")
		return
	}
	var req struct {
		URL string `json:"download_url"`
	}
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !isSafeHTTPURL(req.URL) {
		respond.Fail(w, http.StatusBadRequest, "bad_url", "下载 URL 必须为 http(s)://")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		respond.Fail(w, http.StatusBadRequest, "bad_url", "URL 无法解析")
		return
	}
	resp, err := hotHTTPClient.Do(hreq)
	if err != nil {
		respond.Fail(w, http.StatusBadGateway, "fetch_failed", "下载热更新包失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respond.Fail(w, http.StatusBadGateway, "fetch_failed",
			fmt.Sprintf("下载源返回 HTTP %d", resp.StatusCode))
		return
	}
	h.finalizeHotBundle(w, r, kind, resp.Body)
}

// hotUpload 上传模式:从 multipart 表单的 bundle 字段读取 zip,交给 finalize 校验落盘。
// 该路由已在 main 里从全局 BodyLimit 豁免,这里自行设上限(防撑爆)。
func (h *Handler) hotUpload(w http.ResponseWriter, r *http.Request, kind string) {
	if h.HotUpdate.Dir == "" {
		respond.Fail(w, http.StatusServiceUnavailable, "hotupdate_disabled", "未配置热更新托管目录(CNV_HOTUPDATE_DIR)")
		return
	}
	max := h.hotMaxBytes()
	// +1MiB 余量留给 multipart 边界/头部;真正的内容上限在 finalize 内按 max 卡。
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		respond.Fail(w, http.StatusBadRequest, "bad_multipart", "上传解析失败(可能超过大小上限): "+err.Error())
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		respond.Fail(w, http.StatusBadRequest, "no_file", "缺少 bundle 文件字段")
		return
	}
	defer file.Close()
	h.finalizeHotBundle(w, r, kind, file)
}

func (h *Handler) hotMaxBytes() int64 {
	if h.Limits != nil {
		if n := h.Limits.HotUpdateBytes(); n > 0 {
			return n
		}
	}
	return 1024 << 20
}

// finalizeHotBundle 统一收尾:流式落盘(同时算 sha256/size、卡大小上限)→ 校验 zip
// 格式 → 原子改名为 <kind>-v<next>.zip → 入库 → 审计 → 返回服务端计算的元数据。
func (h *Handler) finalizeHotBundle(w http.ResponseWriter, r *http.Request, kind string, src io.Reader) {
	dir := h.HotUpdate.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	// 临时文件落在目标目录,保证后续 rename 同盘原子;前缀点号避免被静态目录列出。
	tmp, err := os.CreateTemp(dir, ".hotup-*.part")
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	tmpPath := tmp.Name()
	keepTmp := false
	defer func() {
		tmp.Close()
		if !keepTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	max := h.hotMaxBytes()
	hasher := sha256.New()
	// LimitReader 读到 max+1 字节以便侦测"恰好超限"。
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(src, max+1))
	if err != nil {
		respond.Fail(w, http.StatusBadRequest, "read_failed", "读取热更新包失败: "+err.Error())
		return
	}
	if n > max {
		respond.Fail(w, http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("热更新包超过大小上限(%d 字节)", max))
		return
	}
	if n == 0 {
		respond.Fail(w, http.StatusBadRequest, "empty", "热更新包为空")
		return
	}
	if err := tmp.Close(); err != nil { // 关闭后才能被 zip.OpenReader 打开
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	// 校验"格式正确":必须是可解析、且至少含一个条目的 zip(客户端会解压到 filesDir)。
	if err := validateZipFile(tmpPath); err != nil {
		respond.Fail(w, http.StatusBadRequest, "bad_format", "热更新包不是有效的 zip: "+err.Error())
		return
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	cur, _ := h.St.HotBundleGet(r.Context(), kind)
	next := cur.Version + 1
	filename := kind + "-v" + strconv.Itoa(next) + ".zip"
	finalPath := filepath.Join(dir, filename)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	keepTmp = true // 已改名,tmpPath 不复存在,无需再删

	downloadURL := filename
	if h.HotUpdate.URLBase != nil {
		downloadURL = strings.TrimRight(h.HotUpdate.URLBase(r), "/") + "/" + filename
	}

	now := time.Now().UnixMilli()
	if err := h.St.HotBundleSet(r.Context(), store.HotBundle{
		Kind: kind, Version: next, SHA256: sum,
		DownloadURL: downloadURL, Size: n, PublishedAt: &now,
	}); err != nil {
		_ = os.Remove(finalPath)
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	action := "hotupdate.scn.publish"
	if kind == "js" {
		action = "hotupdate.js.publish"
	}
	h.audit(r, action, "v"+strconv.Itoa(next), map[string]any{"sha256": sum[:12], "size": n})

	respond.OKRaw(w, map[string]any{
		"success":      true,
		"version":      next,
		"sha256":       sum,
		"size":         n,
		"download_url": downloadURL,
		"published_at": now,
	})
}

// validateZipFile 校验文件是合法 zip 且至少含一个条目。
func validateZipFile(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) == 0 {
		return fmt.Errorf("空压缩包(无任何条目)")
	}
	return nil
}

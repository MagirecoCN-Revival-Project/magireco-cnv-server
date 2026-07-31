package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"magirecocn-revival/api-server/internal/api/respond"
)

// pipelineCfg は GitHub Release → S3 → CDN 自動同期パイプラインの設定。
// 機密値（S3 秘密キー・CDN トークン等）は env/CI Secret から取得する。
// 設定表には非機密参照のみ保存し、入力値を直接返しても安全。
// §5 遵守：s3_secret_env / offline_s3_secret_env / cdn_auth_env は
// 「env 変数名（文字列）」であって秘密値そのものではない。
type pipelineCfg struct {
	// ── GitHub データソース ────────────────────────────────────────
	GitHubOwner      string `json:"github_owner"`
	GitHubRepo       string `json:"github_repo"`
	GitHubTagPattern string `json:"github_tag_pattern"` // "v*" など；空 = 全 tag
	AutoSync         bool   `json:"auto_sync"`
	PollIntervalSec  int    `json:"poll_interval_sec"` // 最小 30
	GitHubTokenEnv   string `json:"github_token_env"`  // env 変数名、例 "GH_TOKEN"

	// ── S3 アップロード（リソース/アセット）─────────────────────
	S3Enabled   bool   `json:"s3_enabled"`
	S3Endpoint  string `json:"s3_endpoint"`   // 空 = AWS デフォルト
	S3Bucket    string `json:"s3_bucket"`
	S3Region    string `json:"s3_region"`
	S3KeyID     string `json:"s3_key_id"`     // アクセスキー ID（公開情報）
	S3SecretEnv string `json:"s3_secret_env"` // env 変数名（秘密キーそのものではない）

	// ── CDN キャッシュパージ ──────────────────────────────────────
	CDNEnabled  bool   `json:"cdn_enabled"`
	CDNProvider string `json:"cdn_provider"`  // "cloudflare" | "custom"
	CDNZone     string `json:"cdn_zone"`      // Cloudflare zone ID
	CDNPurgeURL string `json:"cdn_purge_url"` // カスタムエンドポイント
	CDNAuthEnv  string `json:"cdn_auth_env"`  // env 変数名

	// ── 離線包 S3 アップロード（リソースとは別バケット）───────
	OfflineUploadEnabled bool   `json:"offline_upload_enabled"`
	OfflineS3Endpoint    string `json:"offline_s3_endpoint"`
	OfflineS3Bucket      string `json:"offline_s3_bucket"`
	OfflineS3Region      string `json:"offline_s3_region"`
	OfflineS3KeyID       string `json:"offline_s3_key_id"`
	OfflineS3SecretEnv   string `json:"offline_s3_secret_env"` // env 変数名

	// ── ランタイム状態（同期 goroutine が書き込む）──────────────
	InProgress     bool   `json:"in_progress"`
	LastSyncAt     int64  `json:"last_sync_at"`
	LastSyncResult string `json:"last_sync_result"` // "" | "ok" | "fail"
}

func defaultPipeline() pipelineCfg {
	return pipelineCfg{
		GitHubTagPattern: "v*",
		PollIntervalSec:  300,
		CDNProvider:      "cloudflare",
	}
}

// pipelineSafeView は設定の公開安全なマップを返す。
// 実際の秘密値は含まず、env 変数名のみ。releases は呼び出し元が付加。
func pipelineSafeView(c pipelineCfg) map[string]any {
	return map[string]any{
		"github_owner":           c.GitHubOwner,
		"github_repo":            c.GitHubRepo,
		"github_tag_pattern":     c.GitHubTagPattern,
		"auto_sync":              c.AutoSync,
		"poll_interval_sec":      c.PollIntervalSec,
		"github_token_env":       c.GitHubTokenEnv,
		"s3_enabled":             c.S3Enabled,
		"s3_endpoint":            c.S3Endpoint,
		"s3_bucket":              c.S3Bucket,
		"s3_region":              c.S3Region,
		"s3_key_id":              c.S3KeyID,
		"s3_secret_env":          c.S3SecretEnv,
		"cdn_enabled":            c.CDNEnabled,
		"cdn_provider":           c.CDNProvider,
		"cdn_zone":               c.CDNZone,
		"cdn_purge_url":          c.CDNPurgeURL,
		"cdn_auth_env":           c.CDNAuthEnv,
		"offline_upload_enabled": c.OfflineUploadEnabled,
		"offline_s3_endpoint":    c.OfflineS3Endpoint,
		"offline_s3_bucket":      c.OfflineS3Bucket,
		"offline_s3_region":      c.OfflineS3Region,
		"offline_s3_key_id":      c.OfflineS3KeyID,
		"offline_s3_secret_env":  c.OfflineS3SecretEnv,
		"in_progress":            c.InProgress,
		"last_sync_at":           c.LastSyncAt,
		"last_sync_result":       c.LastSyncResult,
	}
}

// ── ハンドラー ─────────────────────────────────────────────────────

func (h *Handler) pipelineGet(w http.ResponseWriter, r *http.Request) {
	c := defaultPipeline()
	_, _ = h.St.ConfigGet(r.Context(), "pipeline", &c)
	v := pipelineSafeView(c)
	v["releases"] = []any{}
	respond.OKRaw(w, map[string]any{"success": true, "pipeline": v})
}

func (h *Handler) pipelinePut(w http.ResponseWriter, r *http.Request) {
	cur := defaultPipeline()
	_, _ = h.St.ConfigGet(r.Context(), "pipeline", &cur)

	var patch map[string]json.RawMessage
	if !respond.ReadJSONAllowUnknown(w, r, &patch) {
		return
	}
	// ランタイム状態フィールドはクライアントが書き換えできない
	for _, k := range []string{"in_progress", "last_sync_at", "last_sync_result"} {
		delete(patch, k)
	}
	merged, _ := json.Marshal(cur)
	var mergedMap map[string]json.RawMessage
	_ = json.Unmarshal(merged, &mergedMap)
	for k, v := range patch {
		mergedMap[k] = v
	}
	mergedJSON, _ := json.Marshal(mergedMap)
	var next pipelineCfg
	if err := json.Unmarshal(mergedJSON, &next); err != nil {
		respond.Fail(w, http.StatusBadRequest, "bad_payload", "")
		return
	}
	// ランタイム状態は現在値を継承
	next.InProgress = cur.InProgress
	next.LastSyncAt = cur.LastSyncAt
	next.LastSyncResult = cur.LastSyncResult

	if next.PollIntervalSec < 30 {
		next.PollIntervalSec = 30
	}
	if next.CDNProvider != "cloudflare" && next.CDNProvider != "custom" && next.CDNProvider != "" {
		respond.Fail(w, http.StatusBadRequest, "bad_cdn_provider", "cdn_provider は cloudflare または custom")
		return
	}
	for _, u := range []string{next.S3Endpoint, next.CDNPurgeURL, next.OfflineS3Endpoint} {
		if u != "" && !isSafeHTTPURL(u) {
			respond.Fail(w, http.StatusBadRequest, "bad_url", "URL は http(s):// で始まる必要があります")
			return
		}
	}
	if err := h.St.ConfigSet(r.Context(), "pipeline", next); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "pipeline.config.update", "", map[string]any{
		"github_repo": next.GitHubOwner + "/" + next.GitHubRepo,
		"auto_sync":   next.AutoSync,
		"s3_enabled":  next.S3Enabled,
		"cdn_enabled": next.CDNEnabled,
	})
	respond.OK(w, nil)
}

// pipelineSync は管道を即時トリガーする。
// 既に実行中なら 423 Locked。設定済みなら goroutine を起動して即座に返す。
func (h *Handler) pipelineSync(w http.ResponseWriter, r *http.Request) {
	cfg := defaultPipeline()
	_, _ = h.St.ConfigGet(r.Context(), "pipeline", &cfg)
	if cfg.InProgress {
		respond.Fail(w, http.StatusLocked, "pipeline_busy", "同期が実行中です")
		return
	}
	if cfg.GitHubOwner == "" || cfg.GitHubRepo == "" {
		respond.Fail(w, http.StatusBadRequest, "pipeline_not_configured", "GitHub リポジトリが設定されていません")
		return
	}

	cfg.InProgress = true
	if err := h.St.ConfigSet(r.Context(), "pipeline", cfg); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	triggeredAt := time.Now().UnixMilli()
	h.audit(r, "pipeline.manual_sync", "", nil)

	go func() {
		ctx := context.Background()
		log := slog.Default()
		result := "ok"
		if err := runPipelineSync(ctx, cfg, log); err != nil {
			result = "fail"
			log.Error("管道同期失敗", "err", err)
		}
		final := defaultPipeline()
		_, _ = h.St.ConfigGet(ctx, "pipeline", &final)
		final.InProgress = false
		final.LastSyncAt = time.Now().UnixMilli()
		final.LastSyncResult = result
		_ = h.St.ConfigSet(ctx, "pipeline", final)
	}()

	respond.OKRaw(w, map[string]any{"success": true, "triggered_at": triggeredAt})
}

// ── 実行ロジック ──────────────────────────────────────────────────

// runPipelineSync は GitHub Release からアセットをダウンロードし S3 へアップロード、
// 続いて CDN キャッシュをパージする。goroutine 内で呼ぶ（長時間実行の可能性あり）。
func runPipelineSync(ctx context.Context, cfg pipelineCfg, log *slog.Logger) error {
	ghToken := ""
	if cfg.GitHubTokenEnv != "" {
		ghToken = os.Getenv(cfg.GitHubTokenEnv)
	}

	rel, err := ghLatestRelease(ctx, cfg.GitHubOwner, cfg.GitHubRepo, cfg.GitHubTagPattern, ghToken)
	if err != nil {
		return fmt.Errorf("GitHub API: %w", err)
	}
	if rel == nil {
		log.Info("パイプライン：マッチする release なし、スキップ")
		return nil
	}
	log.Info("パイプライン：release 検出", "tag", rel.TagName, "assets", len(rel.Assets))

	if cfg.S3Enabled && cfg.S3Bucket != "" && cfg.S3SecretEnv != "" {
		s3Secret := os.Getenv(cfg.S3SecretEnv)
		if s3Secret == "" {
			return fmt.Errorf("S3 シークレット環境変数 %q が未設定", cfg.S3SecretEnv)
		}
		for _, asset := range rel.Assets {
			log.Info("パイプライン：S3 アップロード中", "name", asset.Name)
			if err := syncAssetToS3(ctx, asset, cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region, cfg.S3KeyID, s3Secret, ghToken); err != nil {
				return fmt.Errorf("S3 アップロード %q: %w", asset.Name, err)
			}
		}
		log.Info("パイプライン：S3 アップロード完了", "count", len(rel.Assets))
	}

	if cfg.CDNEnabled && cfg.CDNAuthEnv != "" {
		cdnToken := os.Getenv(cfg.CDNAuthEnv)
		if cdnToken == "" {
			return fmt.Errorf("CDN 認証環境変数 %q が未設定", cfg.CDNAuthEnv)
		}
		if err := purgeCDN(ctx, cfg, cdnToken); err != nil {
			return fmt.Errorf("CDN パージ: %w", err)
		}
		log.Info("パイプライン：CDN キャッシュパージ完了")
	}
	return nil
}

// uploadOfflineToS3 は離線包ファイルを設定済み S3 バケットへアップロードし、
// 公開 URL を返す。未設定またはバケット名が空の場合は ("", nil) を返す。
func (h *Handler) uploadOfflineToS3(ctx context.Context, filePath, filename string) (string, error) {
	cfg := defaultPipeline()
	_, _ = h.St.ConfigGet(ctx, "pipeline", &cfg)
	if !cfg.OfflineUploadEnabled || cfg.OfflineS3Bucket == "" || cfg.OfflineS3SecretEnv == "" {
		return "", nil
	}
	s3Secret := os.Getenv(cfg.OfflineS3SecretEnv)
	if s3Secret == "" {
		return "", fmt.Errorf("offline S3 シークレット環境変数 %q が未設定", cfg.OfflineS3SecretEnv)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	s3Key := "offline/" + filename
	if err := s3PutObject(ctx, cfg.OfflineS3Endpoint, cfg.OfflineS3Region, cfg.OfflineS3Bucket,
		s3Key, "application/zip", f, fi.Size(), cfg.OfflineS3KeyID, s3Secret); err != nil {
		return "", err
	}
	ep := strings.TrimRight(cfg.OfflineS3Endpoint, "/")
	if ep == "" {
		ep = "https://s3." + cfg.OfflineS3Region + ".amazonaws.com"
	}
	return ep + "/" + cfg.OfflineS3Bucket + "/" + s3Key, nil
}

// ── GitHub API ────────────────────────────────────────────────────

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// ghLatestRelease は /releases/latest を叩き、tagPattern にマッチする場合に返す。
func ghLatestRelease(ctx context.Context, owner, repo, tagPattern, token string) (*ghRelease, error) {
	u := "https://api.github.com/repos/" + owner + "/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if !tagMatches(rel.TagName, tagPattern) {
		return nil, nil
	}
	return &rel, nil
}

// tagMatches は単純なサフィックスワイルドカード ("v*" → "v" 始まり) をサポート。
func tagMatches(tag, pattern string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(tag, pattern[:len(pattern)-1])
	}
	return tag == pattern
}

// syncAssetToS3 は GitHub アセットをダウンロードして S3 へストリームアップロードする。
// ダウンロードは直接 S3 へパイプするのでメモリへの全量バッファリングを行わない。
func syncAssetToS3(ctx context.Context, asset ghAsset, endpoint, bucket, region, keyID, secret, ghToken string) error {
	dlReq, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return err
	}
	if ghToken != "" {
		dlReq.Header.Set("Authorization", "Bearer "+ghToken)
	}
	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		return err
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != 200 {
		return fmt.Errorf("GitHub ダウンロード HTTP %d", dlResp.StatusCode)
	}
	size := dlResp.ContentLength // -1 if unknown
	return s3PutObject(ctx, endpoint, region, bucket, asset.Name, "application/octet-stream",
		dlResp.Body, size, keyID, secret)
}

// ── CDN パージ ───────────────────────────────────────────────────

func purgeCDN(ctx context.Context, cfg pipelineCfg, token string) error {
	switch cfg.CDNProvider {
	case "cloudflare":
		if cfg.CDNZone == "" {
			return fmt.Errorf("Cloudflare zone ID が未設定")
		}
		return cfPurgeAll(ctx, cfg.CDNZone, token)
	case "custom":
		if cfg.CDNPurgeURL == "" {
			return fmt.Errorf("custom cdn_purge_url が未設定")
		}
		return customPurge(ctx, cfg.CDNPurgeURL, token)
	default:
		return fmt.Errorf("未知の cdn_provider: %q", cfg.CDNProvider)
	}
}

func cfPurgeAll(ctx context.Context, zone, token string) error {
	body, _ := json.Marshal(map[string]bool{"purge_everything": true})
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.cloudflare.com/client/v4/zones/"+zone+"/purge_cache",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("Cloudflare purge HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func customPurge(ctx context.Context, purgeURL, token string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", purgeURL, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("カスタム CDN パージ HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ── AWS S3 PutObject (SigV4 UNSIGNED-PAYLOAD) ────────────────────
//
// §5 遵守：secret は呼び出し元が env から取得したもの。ここでは使い捨て。

// s3PutObject は AWS Signature Version 4 + UNSIGNED-PAYLOAD を用いて
// body を path-style エンドポイントへ PUT する。
// endpoint が空の場合は "https://s3.{region}.amazonaws.com" を使用。
// HTTPS 通信前提で UNSIGNED-PAYLOAD を使い、ストリームアップロードに対応。
func s3PutObject(ctx context.Context, endpoint, region, bucket, key, contentType string,
	body io.Reader, contentLen int64, keyID, secret string) error {

	ep := strings.TrimRight(endpoint, "/")
	if ep == "" {
		ep = "https://s3." + region + ".amazonaws.com"
	}
	// path-style: /bucket/key
	canonPath := "/" + s3URIEncode(bucket) + "/" + s3URIEncodeKey(key)
	rawURL := ep + canonPath

	now := time.Now().UTC()
	ds := now.Format("20060102")        // yyyymmdd
	dt := now.Format("20060102T150405Z") // yyyymmddThhmmssZ

	req, err := http.NewRequestWithContext(ctx, "PUT", rawURL, body)
	if err != nil {
		return err
	}
	if contentLen >= 0 {
		req.ContentLength = contentLen
	}

	host := req.URL.Host
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		ct = "application/octet-stream"
	}

	req.Header.Set("Content-Type", ct)
	req.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")
	req.Header.Set("x-amz-date", dt)

	// カノニカルヘッダー（アルファベット順、小文字キー、値トリム済み）
	canonHdrs := "content-type:" + ct + "\n" +
		"host:" + host + "\n" +
		"x-amz-content-sha256:UNSIGNED-PAYLOAD\n" +
		"x-amz-date:" + dt + "\n"
	signedHdrs := "content-type;host;x-amz-content-sha256;x-amz-date"

	// カノニカルリクエスト（クエリ文字列は空）
	canonReq := "PUT\n" + canonPath + "\n\n" + canonHdrs + "\n" + signedHdrs + "\nUNSIGNED-PAYLOAD"

	scope := ds + "/" + region + "/s3/aws4_request"
	strToSign := "AWS4-HMAC-SHA256\n" + dt + "\n" + scope + "\n" + s3HashHex([]byte(canonReq))

	sigKey := s3DeriveSignKey(secret, ds, region, "s3")
	sig := hex.EncodeToString(s3HMAC(sigKey, strToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		keyID, scope, signedHdrs, sig))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("S3 PUT HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// s3DeriveSignKey は AWS SigV4 署名キーを導出する。
func s3DeriveSignKey(secret, date, region, service string) []byte {
	k := s3HMAC([]byte("AWS4"+secret), date)
	k = s3HMAC(k, region)
	k = s3HMAC(k, service)
	return s3HMAC(k, "aws4_request")
}

func s3HMAC(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func s3HashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// s3URIEncode は S3 パスの 1 セグメントを RFC 3986 非予約文字以外をパーセントエンコードする。
func s3URIEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// s3URIEncodeKey は S3 オブジェクトキー全体をエンコードする（スラッシュは保持）。
func s3URIEncodeKey(key string) string {
	key = strings.TrimLeft(key, "/")
	parts := strings.Split(key, "/")
	enc := make([]string, len(parts))
	for i, p := range parts {
		enc[i] = s3URIEncode(p)
	}
	return strings.Join(enc, "/")
}

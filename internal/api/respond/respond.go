// Package respond 统一 JSON 响应。
// 所有 API 响应都含顶层 `success`,这是《服务端编写指南》§2.2 约定。
package respond

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// JSON 写入 status + body。
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		_, _ = w.Write([]byte("{}"))
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("respond: encode", "err", err)
	}
}

// OK 200 + `{success:true, ...extra}`。extra 可为 map 或带 success 字段的结构体。
func OK(w http.ResponseWriter, extra map[string]any) {
	body := map[string]any{"success": true}
	for k, v := range extra {
		body[k] = v
	}
	JSON(w, http.StatusOK, body)
}

// OKRaw 200 + 任意 struct(调用方自带 Success 字段)。
func OKRaw(w http.ResponseWriter, body any) {
	JSON(w, http.StatusOK, body)
}

// Fail 200/4xx/5xx + `{success:false, error, message?}`。
func Fail(w http.ResponseWriter, status int, errorCode, msg string) {
	body := map[string]any{"success": false, "error": errorCode}
	if msg != "" {
		body["message"] = msg
	}
	JSON(w, status, body)
}

// FailExtra 同 Fail 但允许附加字段(如 ban 详情)。
func FailExtra(w http.ResponseWriter, status int, errorCode, msg string, extra map[string]any) {
	body := map[string]any{"success": false, "error": errorCode}
	if msg != "" {
		body["message"] = msg
	}
	for k, v := range extra {
		body[k] = v
	}
	JSON(w, status, body)
}

// ReadJSON 读取 body 到 dst,失败返回 400。
func ReadJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
		return false
	}
	return true
}

// ReadJSONAllowUnknown 同上但允许未知字段(用于宽松解码,如客户端可能带额外遥测字段)。
func ReadJSONAllowUnknown(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
		return false
	}
	return true
}

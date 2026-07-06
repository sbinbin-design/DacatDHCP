package server

// P0安全整改: 统一 HTTP 安全中间件、CSRF 令牌、安全响应头、严格 JSON 解码
// 仅处理管理页面安全,不影响 DHCP 业务逻辑
// 所有安全校验失败返回 403/413/415 + 稳定错误码,前端按 code 翻译

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// P0安全: 新增错误码常量(与 server.go 中的 errCode* 同体系)
const (
	errCodeCSRFTokenInvalid   = "csrf_token_invalid"     // 写接口缺少或令牌不匹配
	errCodeHostRejected       = "host_rejected"          // Host 头与监听地址不一致
	errCodeOriginRejected     = "origin_rejected"        // Origin/Referer 来源非法
	errCodeUnsupportedMedia   = "unsupported_media_type" // 写接口 Content-Type 非 application/json
	errCodePayloadTooLarge    = "payload_too_large"      // 请求体超过 64KB
	errCodeInvalidLanguage    = "invalid_language"       // 语言新增: 不支持的语言代码
	errCodeLanguageSaveFailed = "language_save_failed"   // 语言新增: 语言保存到配置文件失败
)

// P0安全: 请求体最大字节数
const maxRequestBodyBytes = 64 * 1024

// P0安全: 首页 CSRF 令牌 meta 占位符,handleIndex 替换为真实令牌
const csrfMetaPlaceholder = `<meta name="dacat-csrf-token" content="">`

// 语言新增: 首页语言 meta 占位符,handleIndex 替换为当前语言代码
// 前端 loadSavedLang 优先读取此值,localStorage 仅作为旧版本兼容回退
const languageMetaPlaceholder = `<meta name="dacat-language" content="">`

// errPayloadTooLarge P0安全: 请求体超限哨兵错误,供 handler 识别并返回 413
var errPayloadTooLarge = errors.New("payload too large")

// generateCSRFToken 使用 crypto/rand 生成 32 字节随机令牌,返回 hex 编码字符串
// 令牌仅在程序启动时生成一次,保存于 AppServer 内存,不写入配置、日志或接口
func generateCSRFToken() (string, error) {
	b := make([]byte, 32) // 32 字节 = 256 位,满足"不少于 32 字节"要求
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// writeSecurityHeaders P0安全: 设置统一安全响应头(所有响应)
// 禁止添加 Access-Control-Allow-Origin
func writeSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

// isWriteMethod P0安全: 判断是否为写方法(需要 CSRF 令牌和 Content-Type 校验)
func isWriteMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut ||
		method == http.MethodPatch || method == http.MethodDelete
}

// isJSONContentType P0安全收口: 使用 mime.ParseMediaType 严格校验 Content-Type
// 媒体类型必须为 application/json; charset 可省略,存在时只能为 utf-8
// 解析失败、其他媒体类型、其他字符集或多余参数统一返回 false(后端返回 415)
func isJSONContentType(ct string) bool {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	if mediaType != "application/json" {
		return false
	}
	// charset 可省略,存在时只能为 utf-8(值不区分大小写)
	if charset, ok := params["charset"]; ok {
		if strings.ToLower(strings.TrimSpace(charset)) != "utf-8" {
			return false
		}
	}
	// 拒绝除 charset 外的其他参数(如 boundary 等无关参数)
	for k := range params {
		if k != "charset" {
			return false
		}
	}
	return true
}

// securityMiddleware P0安全: 统一 HTTP 安全中间件
// 1. 设置安全响应头(所有响应)
// 2. 校验 Host 与实际监听地址完全一致,拒绝 DNS Rebinding
// 3. Origin 存在时必须与 http://127.0.0.1:port 完全一致;为 null 或外部来源拒绝
// 4. Referer 存在时同样校验;缺少 Origin/Referer 不单独拒绝(兼容缺少 Origin/Referer 的本地请求场景)
// 5. 写方法校验 CSRF 令牌和 Content-Type,限制请求体大小为 64KB
func (a *AppServer) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. 设置安全响应头(所有响应,包括错误响应)
		writeSecurityHeaders(w)

		// 2. 校验 Host 头,防止 DNS Rebinding 和异常 Host
		expected := a.expectedHostValue()
		if r.Host != expected {
			writeError(w, http.StatusForbidden, errCodeHostRejected, "host rejected")
			return
		}

		// 3. Origin 存在时必须与 http://127.0.0.1:port 完全一致
		expectedOrigin := "http://" + expected
		if origin := r.Header.Get("Origin"); origin != "" {
			if origin != expectedOrigin {
				writeError(w, http.StatusForbidden, errCodeOriginRejected, "origin rejected")
				return
			}
		}

		// 4. Referer 存在时校验来源(允许等于 origin 或以 origin/ 开头)
		if ref := r.Header.Get("Referer"); ref != "" {
			if ref != expectedOrigin && !strings.HasPrefix(ref, expectedOrigin+"/") {
				writeError(w, http.StatusForbidden, errCodeOriginRejected, "referer rejected")
				return
			}
		}

		// 5. 写方法校验 CSRF 令牌、Content-Type,限制请求体大小
		if isWriteMethod(r.Method) {
			// CSRF 令牌校验: 缺少或不匹配返回 403
			token := r.Header.Get("X-Dacat-CSRF-Token")
			if token == "" || token != a.csrfToken {
				writeError(w, http.StatusForbidden, errCodeCSRFTokenInvalid, "csrf token invalid")
				return
			}
			// Content-Type 校验: 仅接受 application/json(可选 charset=utf-8),其他返回 415
			if !isJSONContentType(r.Header.Get("Content-Type")) {
				writeError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedMedia, "unsupported media type")
				return
			}
			// P0安全收口: Content-Length 预检查,超过 64KB 直接返回 413,避免读取超大正文
			if r.ContentLength > maxRequestBodyBytes {
				writeError(w, http.StatusRequestEntityTooLarge, errCodePayloadTooLarge, "payload too large")
				return
			}
			// 请求体大小限制: handler 读取正文时超过 64KB 触发 413
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}

		next.ServeHTTP(w, r)
	})
}

// decodeJSONBody P0安全: 严格解码 JSON 请求体
// 拒绝未知字段(DisallowUnknownFields)、多个 JSON 对象和尾随垃圾内容
// 检测到请求体超限时返回 errPayloadTooLarge,供 handler 返回 413
func decodeJSONBody(r *http.Request, v interface{}) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errPayloadTooLarge
		}
		return err
	}
	// 拒绝多个 JSON 对象和尾随垃圾内容: 尝试解码第二个值
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("unexpected trailing JSON content")
	} else if err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errPayloadTooLarge
		}
		return fmt.Errorf("invalid trailing content")
	}
	return nil
}

// requireEmptyBody P0安全收口: /api/stop 和 /api/logs/clear 明确要求空正文或空 JSON 对象 {}
// 禁止携带超大或无关正文,保持前端请求兼容(前端 post 传 null 时正文为空)
// 检测到请求体超限时返回 errPayloadTooLarge,供 handler 返回 413
func requireEmptyBody(r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errPayloadTooLarge
		}
		return err
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	return fmt.Errorf("unexpected request body")
}

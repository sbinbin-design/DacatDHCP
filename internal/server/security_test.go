package server

// P0安全: 管理页面安全中间件测试套件
// 覆盖: 合法本地请求通过、错误 Host 拒绝、外部 Origin/Referer 拒绝、
// 写请求缺少/伪造令牌拒绝、错误 Content-Type 返回 415、超大请求返回 413、
// 合法令牌写操作成功、安全响应头完整、静态资源和 GET 接口功能正常
// 所有测试通过 a.securityMiddleware(a.buildMux()) 走真实路由+中间件

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// testSecurityWebFS 返回包含 CSRF 占位符的测试用 webFS
// 避免导入 web 包,使用 fstest.MapFS 提供最小静态文件集
// 语言新增: 同步包含语言 meta 占位符,与 index.html 实际结构一致
func testSecurityWebFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head>
<meta name="dacat-csrf-token" content="">
<meta name="dacat-language" content="">
<title>DacatDHCP</title>
</head><body><button id="btn-start">start</button></body></html>`),
		},
		"style.css": &fstest.MapFile{Data: []byte("body{margin:0}")},
		"app.js":    &fstest.MapFile{Data: []byte("var app=1;")},
		"i18n.js":   &fstest.MapFile{Data: []byte("var i18n=1;")},
		"theme.js":  &fstest.MapFile{Data: []byte("var theme=1;")},
		"dhcp.ico":  &fstest.MapFile{Data: []byte("fake-icon")},
	}
}

// newSecurityTestApp 创建带测试 webFS 的 AppServer,供安全中间件测试使用
func newSecurityTestApp(t *testing.T) *AppServer {
	t.Helper()
	dataDir := t.TempDir()
	app, err := NewAppServer(dataDir, testSecurityWebFS())
	if err != nil {
		t.Fatalf("NewAppServer 失败: %v", err)
	}
	t.Cleanup(func() {
		app.Close()
	})
	return app
}

// expectedTestHost 返回测试环境下的预期 Host(listener 为 nil 时回退到默认端口)
func expectedTestHost() string {
	return "127.0.0.1:8765"
}

// expectedTestOrigin 返回测试环境下的预期 Origin
func expectedTestOrigin() string {
	return "http://127.0.0.1:8765"
}

// newLocalRequest 创建带正确 Host 的请求(模拟合法本地请求)
func newLocalRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = expectedTestHost()
	return req
}

// newLocalWriteRequest 创建带 CSRF 令牌和 JSON Content-Type 的写请求
func newLocalWriteRequest(app *AppServer, method, target, body string) *http.Request {
	req := newLocalRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---- 安全响应头测试 ----

// TestSecurity_HeadersComplete 所有响应包含完整安全头
func TestSecurity_HeadersComplete(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	h := w.Header()
	checks := map[string]string{
		"Content-Security-Policy":      "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
		"X-Frame-Options":              "DENY",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
	}
	for header, expected := range checks {
		if got := h.Get(header); got != expected {
			t.Errorf("安全头 %s 期望 %q, 实际 %q", header, expected, got)
		}
	}
	// 禁止添加 Access-Control-Allow-Origin
	if got := h.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("禁止添加 Access-Control-Allow-Origin, 实际 %q", got)
	}
}

// ---- Host 校验测试 ----

// TestSecurity_LegitimateLocalRequestPasses 合法本地请求(正确 Host)通过
func TestSecurity_LegitimateLocalRequestPasses(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("合法本地请求应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// TestSecurity_WrongHostRejected 错误 Host 被拒绝(403 host_rejected)
func TestSecurity_WrongHostRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "evil.example.com:8765"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("错误 Host 应返回 403, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeHostRejected) {
		t.Errorf("错误 Host 应返回 code=%s, body=%s", errCodeHostRejected, w.Body.String())
	}
}

// TestSecurity_LocalhostHostRejected localhost Host 被拒绝(仅允许 127.0.0.1)
func TestSecurity_LocalhostHostRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "localhost:8765"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("localhost Host 应返回 403(仅允许 127.0.0.1), 实际 %d", w.Code)
	}
}

// ---- Origin/Referer 校验测试 ----

// TestSecurity_ExternalOriginRejected 外部 Origin 被拒绝
func TestSecurity_ExternalOriginRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("外部 Origin 应返回 403, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeOriginRejected) {
		t.Errorf("外部 Origin 应返回 code=%s, body=%s", errCodeOriginRejected, w.Body.String())
	}
}

// TestSecurity_NullOriginRejected Origin: null 被拒绝
func TestSecurity_NullOriginRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Origin", "null")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Origin: null 应返回 403, 实际 %d", w.Code)
	}
}

// TestSecurity_ExternalRefererRejected 外部 Referer 被拒绝
func TestSecurity_ExternalRefererRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Referer", "http://evil.example.com/page")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("外部 Referer 应返回 403, 实际 %d", w.Code)
	}
}

// TestSecurity_LegitimateOriginPasses 合法 Origin(与监听地址一致)通过
func TestSecurity_LegitimateOriginPasses(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Origin", expectedTestOrigin())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("合法 Origin 应返回 200, 实际 %d", w.Code)
	}
}

// TestSecurity_MissingOriginNotRejected 缺少 Origin 不单独拒绝(兼容缺少 Origin/Referer 的本地请求场景)
func TestSecurity_MissingOriginNotRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/status", nil)
	// 不设置 Origin 和 Referer
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("缺少 Origin/Referer 的 GET 请求应通过(兼容缺少 Origin/Referer 的本地请求场景), 实际 %d", w.Code)
	}
}

// ---- CSRF 令牌校验测试 ----

// TestSecurity_WriteMissingTokenRejected 写请求缺少 CSRF 令牌被拒绝
func TestSecurity_WriteMissingTokenRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("Content-Type", "application/json")
	// 不设置 X-Dacat-CSRF-Token
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("缺少 CSRF 令牌应返回 403, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeCSRFTokenInvalid) {
		t.Errorf("缺少 CSRF 令牌应返回 code=%s, body=%s", errCodeCSRFTokenInvalid, w.Body.String())
	}
}

// TestSecurity_WriteForgedTokenRejected 写请求伪造 CSRF 令牌被拒绝
func TestSecurity_WriteForgedTokenRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dacat-CSRF-Token", "forged-token-12345")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("伪造 CSRF 令牌应返回 403, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeCSRFTokenInvalid) {
		t.Errorf("伪造 CSRF 令牌应返回 code=%s, body=%s", errCodeCSRFTokenInvalid, w.Body.String())
	}
}

// TestSecurity_WriteValidTokenStopsCheck 写请求合法令牌通过 CSRF 校验
// handleStop 因服务未运行返回 400,但证明已通过 CSRF 和 Content-Type 校验
func TestSecurity_WriteValidTokenStopsCheck(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPost, "/api/stop", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// 服务未运行返回 400(service_not_running),证明已通过 CSRF 校验
	if w.Code == http.StatusForbidden {
		t.Errorf("合法令牌不应返回 403, body=%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), errCodeCSRFTokenInvalid) {
		t.Errorf("合法令牌不应返回 csrf_token_invalid, body=%s", w.Body.String())
	}
}

// ---- Content-Type 校验测试 ----

// TestSecurity_WriteWrongContentTypeRejected 写请求错误 Content-Type 返回 415
func TestSecurity_WriteWrongContentTypeRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("错误 Content-Type 应返回 415, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeUnsupportedMedia) {
		t.Errorf("错误 Content-Type 应返回 code=%s, body=%s", errCodeUnsupportedMedia, w.Body.String())
	}
}

// TestSecurity_WriteJSONCharsetAccepted application/json; charset=utf-8 被接受
func TestSecurity_WriteJSONCharsetAccepted(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// 应通过 Content-Type 校验(返回 400 service_not_running 而非 415)
	if w.Code == http.StatusUnsupportedMediaType {
		t.Errorf("application/json; charset=utf-8 应被接受, 不应返回 415")
	}
}

// ---- 请求体大小限制测试 ----

// TestSecurity_OversizedBodyRejected 请求体超过 64KB 返回 413
func TestSecurity_OversizedBodyRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	// 构造超过 64KB 的 JSON 请求体
	bigData := strings.Repeat("a", 70*1024)
	body := `{"data":"` + bigData + `"}`
	req := newLocalWriteRequest(app, http.MethodPut, "/api/config", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超大请求体应返回 413, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), errCodePayloadTooLarge) {
		t.Errorf("超大请求体应返回 code=%s, body=%s", errCodePayloadTooLarge, w.Body.String())
	}
}

// ---- 合法令牌写操作成功测试 ----

// TestSecurity_LegitimateTokenClearLogsSucceeds 合法令牌清空日志成功
func TestSecurity_LegitimateTokenClearLogsSucceeds(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPost, "/api/logs/clear", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("合法令牌清空日志应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// TestSecurity_LegitimateTokenPutConfigSucceeds 合法令牌保存配置成功
func TestSecurity_LegitimateTokenPutConfigSucceeds(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	body := `{"adapter_name":"","pool_start":"","pool_end":"","lease_minutes":60,"web_port":8765,"gateway":"","dns_servers":[]}`
	req := newLocalWriteRequest(app, http.MethodPut, "/api/config", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("合法令牌保存配置应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// ---- 严格 JSON 解码测试 ----

// TestSecurity_JSONUnknownFieldRejected JSON 含未知字段被拒绝
func TestSecurity_JSONUnknownFieldRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	body := `{"adapter_name":"","unknown_field":"x"}`
	req := newLocalWriteRequest(app, http.MethodPut, "/api/config", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("含未知字段的 JSON 应返回 400, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// TestSecurity_JSONTrailingGarbageRejected JSON 尾随垃圾被拒绝
func TestSecurity_JSONTrailingGarbageRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	body := `{"adapter_name":""}{}`
	req := newLocalWriteRequest(app, http.MethodPut, "/api/config", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("尾随垃圾的 JSON 应返回 400, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// ---- 静态资源和 GET 接口测试 ----

// TestSecurity_IndexPageInjectsCSRFToken 首页注入 CSRF 令牌并设置 Cache-Control: no-store
func TestSecurity_IndexPageInjectsCSRFToken(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("首页应返回 200, 实际 %d", w.Code)
	}
	body := w.Body.String()
	// 占位符应被替换为真实令牌
	if strings.Contains(body, csrfMetaPlaceholder) {
		t.Errorf("首页应注入真实 CSRF 令牌, 仍包含占位符")
	}
	if !strings.Contains(body, app.csrfToken) {
		t.Errorf("首页应包含 CSRF 令牌 %s, body=%s", app.csrfToken, body)
	}
	// 首页应设置 Cache-Control: no-store
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("首页应设置 Cache-Control: no-store, 实际 %q", cc)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("首页 Content-Type 应为 text/html, 实际 %q", ct)
	}
}

// TestSecurity_StaticFilesServed 静态文件正常服务且带安全头
func TestSecurity_StaticFilesServed(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	files := []struct {
		path        string
		contentType string
	}{
		{"/style.css", "text/css"},
		{"/app.js", "application/javascript"},
		{"/i18n.js", "application/javascript"},
		{"/theme.js", "application/javascript"},
		{"/favicon.ico", "image/x-icon"},
	}
	for _, f := range files {
		req := newLocalRequest(http.MethodGet, f.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("静态文件 %s 应返回 200, 实际 %d", f.path, w.Code)
			continue
		}
		ct := w.Header().Get("Content-Type")
		if !strings.Contains(ct, f.contentType) {
			t.Errorf("静态文件 %s Content-Type 应含 %s, 实际 %s", f.path, f.contentType, ct)
		}
		// 静态文件也应带安全头
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("静态文件 %s 应带 X-Frame-Options: DENY", f.path)
		}
	}
}

// TestSecurity_AllGetInterfacesWork 所有 GET 接口功能正常
func TestSecurity_AllGetInterfacesWork(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	getApis := []string{
		"/api/adapters",
		"/api/config",
		"/api/status",
		"/api/leases",
		"/api/logs",
		"/api/version",
		"/api/language", // 语言新增: GET 接口需通过安全中间件
	}
	for _, api := range getApis {
		req := newLocalRequest(http.MethodGet, api, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		// /api/adapters 在测试环境可能因网卡枚举失败返回 500,其他应返回 200
		// 主要验证不被安全中间件拦截(不返回 403)
		if w.Code == http.StatusForbidden {
			t.Errorf("GET %s 不应被安全中间件拒绝(403), body=%s", api, w.Body.String())
		}
	}
}

// TestSecurity_PoolRecommendGetWorks GET /api/pool-recommend 不被中间件拦截
func TestSecurity_PoolRecommendGetWorks(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/pool-recommend", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	// 缺少 adapter_name 返回 400,但不应被中间件拦截为 403
	if w.Code == http.StatusForbidden {
		t.Errorf("GET /api/pool-recommend 不应被安全中间件拒绝(403), body=%s", w.Body.String())
	}
}

// TestSecurity_StopAndLogsClearRequireToken /api/stop 和 /api/logs/clear 必须通过令牌和 Content-Type 校验
func TestSecurity_StopAndLogsClearRequireToken(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	// /api/stop 无令牌应被拒绝
	req1 := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusForbidden {
		t.Errorf("/api/stop 无令牌应返回 403, 实际 %d", w1.Code)
	}

	// /api/logs/clear 无令牌应被拒绝
	req2 := newLocalRequest(http.MethodPost, "/api/logs/clear", nil)
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("/api/logs/clear 无令牌应返回 403, 实际 %d", w2.Code)
	}

	// /api/stop 无 Content-Type(但有令牌)应返回 415
	req3 := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req3.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	if w3.Code != http.StatusUnsupportedMediaType {
		t.Errorf("/api/stop 无 Content-Type 应返回 415, 实际 %d", w3.Code)
	}
}

// ---- P0安全收口: 严格 Content-Type 校验测试 (mime.ParseMediaType) ----

// TestSecurity_ContentTypeWrongCharsetRejected application/json; charset=iso-8859-1 返回 415
func TestSecurity_ContentTypeWrongCharsetRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "application/json; charset=iso-8859-1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("charset=iso-8859-1 应返回 415, 实际 %d", w.Code)
	}
}

// TestSecurity_ContentTypeExtraParamRejected application/json; boundary=x 返回 415
func TestSecurity_ContentTypeExtraParamRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "application/json; boundary=something")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("多余参数 boundary 应返回 415, 实际 %d", w.Code)
	}
}

// TestSecurity_ContentTypeMalformedRejected 格式错误的 Content-Type 返回 415
func TestSecurity_ContentTypeMalformedRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "application/json; charset")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("格式错误的 Content-Type 应返回 415, 实际 %d", w.Code)
	}
}

// TestSecurity_ContentTypeTextJSONRejected text/json 返回 415(仅接受 application/json)
func TestSecurity_ContentTypeTextJSONRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPost, "/api/stop", nil)
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "text/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/json 应返回 415(仅接受 application/json), 实际 %d", w.Code)
	}
}

// ---- P0安全收口: Content-Length 预检查测试 ----

// TestSecurity_ContentLengthPreCheckRejected Content-Length 超过 64KB 直接返回 413
func TestSecurity_ContentLengthPreCheckRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	// 构造超过 64KB 的请求,设置明确的 Content-Length
	bigData := strings.Repeat("a", 70*1024)
	req := newLocalRequest(http.MethodPut, "/api/config", strings.NewReader(bigData))
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(bigData))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("Content-Length 超限应返回 413, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), errCodePayloadTooLarge) {
		t.Errorf("Content-Length 超限应返回 code=%s, body=%s", errCodePayloadTooLarge, w.Body.String())
	}
}

// ---- P0安全收口: /api/stop 和 /api/logs/clear 空正文校验测试 ----

// TestSecurity_StopWithNonEmptyBodyRejected /api/stop 携带非空正文返回 400
func TestSecurity_StopWithNonEmptyBodyRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPost, "/api/stop", `{"unexpected":"data"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("/api/stop 携带非空正文应返回 400, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// TestSecurity_StopWithEmptyJSONObjectAccepted /api/stop 携带空 JSON 对象 {} 通过正文校验
func TestSecurity_StopWithEmptyJSONObjectAccepted(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPost, "/api/stop", "{}")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// {} 通过正文校验,服务未运行返回 400 service_not_running
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), errCodeInvalidRequest) {
		t.Errorf("/api/stop 携带 {} 不应返回 invalid_request, body=%s", w.Body.String())
	}
}

// TestSecurity_LogsClearWithNonEmptyBodyRejected /api/logs/clear 携带非空正文返回 400
func TestSecurity_LogsClearWithNonEmptyBodyRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPost, "/api/logs/clear", `{"x":1}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("/api/logs/clear 携带非空正文应返回 400, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// TestSecurity_LogsClearWithEmptyJSONObjectSucceeds /api/logs/clear 携带 {} 清空成功
func TestSecurity_LogsClearWithEmptyJSONObjectSucceeds(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPost, "/api/logs/clear", "{}")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("/api/logs/clear 携带 {} 应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

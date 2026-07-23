package server

import (
	"DacatDHCP/internal/dhcp"
	"DacatDHCP/internal/i18n"
	"DacatDHCP/internal/network"
	"DacatDHCP/internal/version"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config 持久化配置
type Config struct {
	AdapterName  string              `json:"adapter_name"`
	PoolStart    string              `json:"pool_start"`
	PoolEnd      string              `json:"pool_end"`
	LeaseMinutes int                 `json:"lease_minutes"`
	WebPort      int                 `json:"web_port"`
	Gateway      string              `json:"gateway"`       // V2新增: 默认网关（可选，空则不下发 Option 3）
	DNSServers   []string            `json:"dns_servers"`   // V2新增: DNS 服务器（可选，空则不下发 Option 6）
	Language     string              `json:"language"`      // 语言: zh-CN/en-US;空或无效时由 main.go 检测 Windows 界面语言,NewAppServer 沿用全局语言
	StaticLeases []StaticLeaseConfig `json:"static_leases"` // V1.0.3新增: MAC-IP 固定映射列表
}

// StaticLeaseConfig 固定 MAC-IP 映射配置项（V1.0.3新增）
// 持久化到 config.json,不引入数据库;MAC 保存时统一规范为大写冒号格式
type StaticLeaseConfig struct {
	MAC       string `json:"mac"`        // MAC 地址,大写冒号格式,如 00:11:22:33:44:55
	IP        string `json:"ip"`         // IPv4 地址
	Remark    string `json:"remark"`     // 备注（可选）
	Enabled   bool   `json:"enabled"`    // 是否启用
	CreatedAt string `json:"created_at"` // 创建时间（RFC3339）
	UpdatedAt string `json:"updated_at"` // 更新时间（RFC3339）
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		LeaseMinutes: 60,
		WebPort:      8765,
	}
}

// AppServer HTTP API 服务器，管理 DHCP 服务生命周期
type AppServer struct {
	mu             sync.RWMutex
	dhcpSrv        *dhcp.Server
	logger         *Logger
	config         Config
	configDir      string
	webFS          fs.FS
	listener       net.Listener // V1修复: 先创建 listener，确认绑定成功再开浏览器
	httpSrv        *http.Server
	closing        bool   // V2: 退出过程中拒绝新的 Web API 操作
	onStatusChange func() // V3: DHCP 状态变化时回调（用于更新托盘 Tooltip）
	// 语言新增: 语言变化时回调(用于刷新托盘 Tooltip)
	// 通过 SetOnLanguageChange 注入,handleLanguage 在锁外直接触发
	onLanguageChange func()
	isAdmin          bool   // V10新增: 进程是否以管理员权限运行(由 main 注入),前端底部栏展示真实权限状态
	csrfToken        string // P0安全: 每次启动生成的 CSRF 令牌,注入首页 meta,写接口校验 X-Dacat-CSRF-Token
	// V16新增测试钩子: 非 nil 时覆盖默认的临时文件写入逻辑(Write/Sync/Close)
	// 仅测试使用,生产代码不得设置
	testSaveConfigWriter saveConfigWriter
	// V16新增测试钩子: 非 nil 时覆盖默认的文件替换逻辑(Rename)
	// 仅测试使用,生产代码不得设置
	testSaveConfigReplacer saveConfigReplacer
	// V15新增测试钩子: 非 nil 时 handleStart/handleConfig 使用此函数查找网卡,绕过真实网卡枚举
	// 仅测试使用,生产代码不得设置
	testFindAdapterFunc func(name string) (net.IP, net.IPMask, error)
}

// V16新增: saveConfigWriter 是可注入的"写入临时文件并关闭"函数类型
// dir 为配置目录,data 为序列化后的配置数据
// 返回临时文件路径和可能的错误
// 测试可注入此函数模拟 Write/Sync/Close 失败
type saveConfigWriter func(dir string, data []byte) (tmpPath string, err error)

// V16新增: saveConfigReplacer 是可注入的"替换正式文件"函数类型
// tmpPath 为临时文件路径,configPath 为正式配置文件路径
// 测试可注入此函数模拟 Rename 失败
type saveConfigReplacer func(tmpPath, configPath string) error

// NewAppServer 创建应用服务器
func NewAppServer(dataDir string, webFS fs.FS) (*AppServer, error) {
	// 创建数据目录
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %v", err)
	}

	// 创建日志记录器
	logger, err := NewLogger(dataDir)
	if err != nil {
		return nil, err
	}

	// V1修复: 先加载 config.json（配置加载顺序：config.json → 网卡列表 → 恢复已保存网卡）
	config := DefaultConfig()
	configPath := filepath.Join(dataDir, "config.json")
	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &config)
	}
	// 语言重构: 不再用空配置覆盖 main.go 已确定的语言
	// 配置中 Language 有效时采用配置值,无效或缺失时沿用当前全局语言(由 main.go 通过配置或 Windows 检测设置)
	// 后续 saveConfig 时会写入标准值,实现旧配置迁移
	// 必须在任何 MessageBox 之前完成(由 main 在 NewAppServer 之前预先调用 i18n.SetLanguage)
	if parsed, ok := i18n.ParseLanguage(config.Language); ok {
		i18n.SetLanguage(parsed)
		config.Language = parsed
	} else {
		// 配置无效或缺失: 沿用 main.go 已设置的全局语言,确保 config.Language 写入标准值
		config.Language = i18n.GetLanguage()
	}

	// 创建 DHCP 服务器
	dhcpSrv := dhcp.NewServer()
	dhcpSrv.SetLogFunc(func(format string, args ...interface{}) {
		logger.Log(format, args...)
	})

	// P0安全: 每次启动使用 crypto/rand 生成 CSRF 令牌,不写入配置/日志/接口
	csrfToken, err := generateCSRFToken()
	if err != nil {
		return nil, fmt.Errorf("生成 CSRF 令牌失败: %v", err)
	}

	return &AppServer{
		dhcpSrv:   dhcpSrv,
		logger:    logger,
		config:    config,
		configDir: dataDir,
		webFS:     webFS,
		csrfToken: csrfToken,
	}, nil
}

// PostInit 初始化回调连接（V3: 在 main.go 创建 AppServer 后、Start 前调用）
// 必须在 AppServer 创建后调用，因为回调需要访问 AppServer 自身
func (a *AppServer) PostInit() {
	// V3: DHCP 自动停止时通知托盘更新状态
	// V8: 回调接收停止原因（当前未使用，保留供未来扩展）
	a.dhcpSrv.SetOnAutoStop(func(_ string) {
		a.notifyStatusChange()
	})
}

// buildMux 构建路由 mux(不含安全中间件)
// P0安全: 提取为独立方法,供 Start 和安全测试使用,路由定义保持不变
func (a *AppServer) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// 静态文件
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/style.css", a.handleStatic("style.css", "text/css"))
	mux.HandleFunc("/app.js", a.handleStatic("app.js", "application/javascript"))
	// 新增: 国际化资源与主题管理脚本路由,通过 embed 编译进单 EXE
	mux.HandleFunc("/i18n.js", a.handleStatic("i18n.js", "application/javascript"))
	mux.HandleFunc("/theme.js", a.handleStatic("theme.js", "application/javascript"))
	mux.HandleFunc("/ie11-check.js", a.handleStatic("ie11-check.js", "application/javascript")) // IE11 检测脚本,在 theme.js 之前加载
	mux.HandleFunc("/favicon.ico", a.handleStatic("dhcp.ico", "image/x-icon"))                  // V1修复: Web favicon

	// API 路由
	mux.HandleFunc("/api/adapters", a.handleAdapters)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/start", a.handleStart)
	mux.HandleFunc("/api/stop", a.handleStop)
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/leases", a.handleLeases)
	mux.HandleFunc("/api/logs", a.handleLogs)
	mux.HandleFunc("/api/logs/clear", a.handleLogsClear)         // V12新增: 清空内存环形缓冲区并截断日志文件
	mux.HandleFunc("/api/pool-recommend", a.handlePoolRecommend) // V1修复: 后端计算地址池推荐
	mux.HandleFunc("/api/version", a.handleVersion)              // 版本信息：读取 internal/version 唯一源
	mux.HandleFunc("/api/language", a.handleLanguage)            // 语言新增: GET 返回当前语言,PUT 切换语言
	mux.HandleFunc("/api/reservations", a.handleReservations)    // V1.0.3新增: MAC-IP 固定映射 CRUD

	return mux
}

// expectedHostValue P0安全: 返回预期的 Host 头值(与实际监听地址一致)
// 优先使用 listener 实际地址,listener 未设置时(测试)回退到配置端口
func (a *AppServer) expectedHostValue() string {
	if a.listener != nil {
		return a.listener.Addr().String()
	}
	return fmt.Sprintf("127.0.0.1:%d", a.config.WebPort)
}

// Start 启动 HTTP 服务器（V1修复: 先绑定端口，确认成功后再开始服务）
// 返回监听地址，供调用方在确认绑定成功后打开浏览器
// P0安全: 所有请求经 securityMiddleware 校验,http.Server 增加超时和 MaxHeaderBytes
func (a *AppServer) Start() error {
	handler := a.securityMiddleware(a.buildMux())

	addr := fmt.Sprintf("127.0.0.1:%d", a.config.WebPort)

	// V1修复: 先尝试绑定端口，端口占用时明确报错
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("管理端口 %s 被占用或无法绑定: %v", addr, err)
	}

	a.listener = listener
	// P0安全: 增加超时和 MaxHeaderBytes,仍只监听 127.0.0.1
	a.httpSrv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16384,
	}

	a.logger.Log("管理页面已启动: http://%s", addr)

	// 在后台 goroutine 中提供 HTTP 服务
	go func() {
		if err := a.httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			a.logger.Log("HTTP 服务器错误: %v", err)
		}
	}()

	return nil
}

// Close 关闭服务器（兼容旧调用，等价于 CloseServices + CloseLogger）
func (a *AppServer) Close() {
	a.CloseServices()
	a.CloseLogger()
}

// CloseServices V8: 停止 DHCP 和 HTTP 服务，不关闭日志
// 供退出流程在 tray.RequestExit 之前调用
func (a *AppServer) CloseServices() {
	a.mu.Lock()

	// V5: 先清除状态变化回调
	a.onStatusChange = nil
	// 语言新增: 同步清除语言变化回调,避免托盘销毁后旧回调继续触发
	a.onLanguageChange = nil

	// V1修复: 先停止 DHCP 服务（如果运行中），Stop() 内部会等待全部协程退出
	// V9修复: 程序退出路径使用 StopForShutdown，清空 status.error 不残留错误提示
	if a.dhcpSrv != nil && a.dhcpSrv.IsRunning() {
		a.mu.Unlock() // Stop() 内部也需要锁，先释放避免死锁
		a.dhcpSrv.StopForShutdown()
		a.mu.Lock()
	}

	// 关闭 HTTP 服务器
	if a.httpSrv != nil {
		a.httpSrv.Close()
	}
	a.mu.Unlock()
}

// CloseLogger V8: 刷新并关闭日志文件
// 必须在所有服务停止后、退出前调用，确保退出阶段的错误已写入日志
func (a *AppServer) CloseLogger() {
	if a.logger != nil {
		a.logger.Close()
	}
}

// IsDHCPRunning 检查 DHCP 服务是否运行中
// V2: 供托盘退出确认使用
func (a *AppServer) IsDHCPRunning() bool {
	return a.dhcpSrv != nil && a.dhcpSrv.IsRunning()
}

// SetClosing 设置退出标志，拒绝新的 Web API 操作
// V2: 退出流程第一步
func (a *AppServer) SetClosing() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closing = true
}

// SetOnStatusChange 设置 DHCP 状态变化回调
// V3: 供托盘在 DHCP 启动/停止/自动停止时实时更新 Tooltip
func (a *AppServer) SetOnStatusChange(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onStatusChange = f
}

// SetOnLanguageChange 设置语言变化回调
// 语言新增: 语言保存成功后在锁外直接触发 onLanguageChange,通知托盘刷新 Tooltip
// 必须在 Start 之前设置,关闭时由 CloseServices 清除
func (a *AppServer) SetOnLanguageChange(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onLanguageChange = f
}

// ClearOnLanguageChange 清除语言变化回调
// 语言新增: 退出时清除,避免托盘销毁后旧回调继续触发
func (a *AppServer) ClearOnLanguageChange() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onLanguageChange = nil
}

// SetIsAdmin V10新增: 注入进程管理员权限状态(由 main 在创建 AppServer 后调用)
// 前端底部栏据此展示真实权限,禁止前端伪造
func (a *AppServer) SetIsAdmin(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.isAdmin = v
}

// ClearOnStatusChange 清除状态变化回调
// V5: 退出时先清回调再停 DHCP，避免托盘销毁后旧回调继续发送状态更新
func (a *AppServer) ClearOnStatusChange() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onStatusChange = nil
}

// notifyStatusChange 通知 DHCP 状态变化
// V3: 在 DHCP 启动、停止、自动停止时调用
func (a *AppServer) notifyStatusChange() {
	a.mu.RLock()
	f := a.onStatusChange
	a.mu.RUnlock()
	if f != nil {
		f()
	}
}

// ListenAddr 返回实际监听地址
func (a *AppServer) ListenAddr() string {
	if a.listener != nil {
		return a.listener.Addr().String()
	}
	return ""
}

// Logf 写入日志（V5: 供托盘等外部模块使用）
func (a *AppServer) Logf(format string, args ...interface{}) {
	if a.logger != nil {
		a.logger.Log(format, args...)
	}
}

// handleIndex 处理首页
// P0安全: 注入 CSRF 令牌到 <meta name="dacat-csrf-token">,设置 Cache-Control: no-store
// 令牌不写入配置、日志或接口,仅通过首页 meta 暴露给同源前端
// 语言新增: 同时注入当前语言到 <meta name="dacat-language">,前端首次加载以服务端语言为准
func (a *AppServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(a.webFS, "index.html")
	if err != nil {
		http.Error(w, "页面加载失败", http.StatusInternalServerError)
		return
	}
	// P0安全: 将占位符替换为真实 CSRF 令牌(仅替换一次,避免影响其他 meta)
	html := strings.Replace(string(data), csrfMetaPlaceholder,
		fmt.Sprintf(`<meta name="dacat-csrf-token" content="%s">`, a.csrfToken), 1)
	// 语言新增: 将语言占位符替换为当前语言代码,前端 loadSavedLang 优先读取此值
	// 放在 CSRF 替换之后,不影响 CSRF meta
	html = strings.Replace(html, languageMetaPlaceholder,
		fmt.Sprintf(`<meta name="dacat-language" content="%s">`, i18n.GetLanguage()), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// P0安全: 首页禁止缓存,避免令牌被缓存复用
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(html))
}

// handleStatic 处理静态文件
func (a *AppServer) handleStatic(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(a.webFS, name)
		if err != nil {
			http.Error(w, "文件加载失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType+"; charset=utf-8")
		w.Write(data)
	}
}

// handleAdapters 处理网卡列表 API
// V13: 错误统一使用 writeError 返回稳定 code
func (a *AppServer) handleAdapters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	adapters, err := network.EnumerateAdapters()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"adapters": adapters})
}

// handleConfig 处理配置 API
// V13: 所有错误统一使用 writeError 返回稳定 code
func (a *AppServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		writeJSON(w, http.StatusOK, a.config)
		a.mu.RUnlock()

	case http.MethodPut:
		a.mu.Lock()
		defer a.mu.Unlock()

		// 运行时禁止修改配置
		if a.dhcpSrv.IsRunning() {
			writeError(w, http.StatusBadRequest, errCodeServiceRunning, "服务运行中，无法修改配置")
			return
		}

		var cfg Config
		// P0安全: 使用 decodeJSONBody 严格解码,拒绝未知字段、多个对象、尾随垃圾和超限请求体
		if err := decodeJSONBody(r, &cfg); err != nil {
			if err == errPayloadTooLarge {
				writeError(w, http.StatusRequestEntityTooLarge, errCodePayloadTooLarge, "请求体过大")
				return
			}
			writeError(w, http.StatusBadRequest, errCodeInvalidConfig, "配置格式错误")
			return
		}

		// V2新增: 保存时规范化网关和 DNS（去空格、去重、最多3个、校验 IPv4）
		// V2.1扩展: 已选择有效网卡并存在地址池时，继续校验同子网、非网络/广播、不在池内
		cfg.Gateway = strings.TrimSpace(cfg.Gateway)
		// 尝试解析网卡信息（网卡信息不足时仅校验 IPv4 格式）
		var adapterIP net.IP
		var subnetMask net.IPMask
		if cfg.AdapterName != "" {
			// V15: 测试钩子优先,允许测试绕过真实网卡枚举
			if a.testFindAdapterFunc != nil {
				adapterIP, subnetMask, _ = a.testFindAdapterFunc(cfg.AdapterName)
			} else {
				adapterIP, subnetMask, _ = findAdapterIP(cfg.AdapterName)
			}
		}
		// 解析地址池（缺失时跳过池内检查）
		var poolStart, poolEnd net.IP
		if cfg.PoolStart != "" {
			poolStart = net.ParseIP(cfg.PoolStart)
		}
		if cfg.PoolEnd != "" {
			poolEnd = net.ParseIP(cfg.PoolEnd)
		}
		// V1.0.2新增: 保存配置前校验地址池和网关必须与网卡同子网（早于 validateGateway 拦截）
		if code, err := validatePoolSubnet(adapterIP, subnetMask, cfg.Gateway, poolStart, poolEnd); err != nil {
			writeError(w, http.StatusBadRequest, code, err.Error())
			return
		}
		// 复用统一校验函数（禁止重复编写判断逻辑）
		_, err := validateGateway(adapterIP, subnetMask, cfg.Gateway, poolStart, poolEnd)
		if err != nil {
			// V13: 网关错误统一返回 invalid_gateway 或 gateway_in_pool
			code := errCodeInvalidGateway
			if strings.Contains(err.Error(), "地址池内") || strings.Contains(err.Error(), "地址池范围") {
				code = errCodeGatewayInPool
			}
			writeError(w, http.StatusBadRequest, code, err.Error())
			return
		}
		dnsIPs, err := normalizeDNSServers(cfg.DNSServers)
		if err != nil {
			// V14: DNS 数量超限使用独立错误码 dns_too_many,其他 DNS 错误用 invalid_dns
			dnsCode := errCodeInvalidDNS
			if strings.Contains(err.Error(), "最多允许 3 个") {
				dnsCode = errCodeDNSTooMany
			}
			writeError(w, http.StatusBadRequest, dnsCode, err.Error())
			return
		}
		cfg.DNSServers = ipSliceToStrings(dnsIPs)

		// 语言新增: PUT /api/config 不修改 Language 字段,Language 由 /api/language 独立管理
		// 在 a.config = cfg 之前保留当前 Language,避免被请求体覆盖为空
		cfg.Language = a.config.Language
		// V1.0.3新增: PUT /api/config 不修改 StaticLeases,由 /api/reservations 独立管理
		cfg.StaticLeases = a.config.StaticLeases

		// V14: 先保存配置到文件,成功后再更新内存配置,禁止写入失败仍更新内存或返回 ok
		// 临时设置 a.config 供 saveConfig 序列化,失败则恢复原值
		oldConfig := a.config
		a.config = cfg
		if err := a.saveConfig(); err != nil {
			// 保存失败,恢复内存配置,返回 config_save_failed
			a.config = oldConfig
			writeError(w, http.StatusInternalServerError, errCodeConfigSaveFailed, err.Error())
			return
		}
		// 保存成功,内存配置已更新(上方 a.config = cfg)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
	}
}

// handleStart 处理启动 DHCP 服务
// V13: 方法不允许统一使用 writeError
func (a *AppServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	a.mu.Lock()

	// V2: 退出过程中拒绝新操作
	if a.closing {
		a.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, errCodeServiceClosing, "服务正在关闭")
		return
	}

	if a.dhcpSrv.IsRunning() {
		a.mu.Unlock()
		writeError(w, http.StatusBadRequest, errCodeServiceRunning, "DHCP 服务已在运行")
		return
	}

	var req struct {
		AdapterName  string   `json:"adapter_name"`
		PoolStart    string   `json:"pool_start"`
		PoolEnd      string   `json:"pool_end"`
		LeaseMinutes int      `json:"lease_minutes"`
		Gateway      string   `json:"gateway"`     // V2新增: 可选网关
		DNSServers   []string `json:"dns_servers"` // V2新增: 可选 DNS 列表
	}
	// P0安全: 使用 decodeJSONBody 严格解码,拒绝未知字段、多个对象、尾随垃圾和超限请求体
	if err := decodeJSONBody(r, &req); err != nil {
		a.mu.Unlock()
		if err == errPayloadTooLarge {
			writeError(w, http.StatusRequestEntityTooLarge, errCodePayloadTooLarge, "请求体过大")
			return
		}
		writeError(w, http.StatusBadRequest, errCodeInvalidRequest, "请求格式错误")
		return
	}

	// 查找网卡的 IP 和子网掩码
	// V11新增: 使用 findAdapterIPWithCode 返回稳定错误码
	// V15: 测试钩子优先,允许测试绕过真实网卡枚举
	var adapterIP net.IP
	var subnetMask net.IPMask
	var adapterCode string
	var findErr error
	if a.testFindAdapterFunc != nil {
		adapterIP, subnetMask, findErr = a.testFindAdapterFunc(req.AdapterName)
		if findErr != nil {
			adapterCode = errCodeAdapterNotFound
		}
	} else {
		adapterIP, subnetMask, adapterCode, findErr = findAdapterIPWithCode(req.AdapterName)
	}
	if findErr != nil {
		a.mu.Unlock()
		writeError(w, http.StatusBadRequest, adapterCode, findErr.Error())
		return
	}

	poolStart := net.ParseIP(req.PoolStart)
	poolEnd := net.ParseIP(req.PoolEnd)

	// V1.0.2新增: 启动服务前校验地址池和网关必须与网卡同子网（早于 validateGateway 拦截）
	if code, err := validatePoolSubnet(adapterIP, subnetMask, req.Gateway, poolStart, poolEnd); err != nil {
		a.mu.Unlock()
		writeError(w, http.StatusBadRequest, code, err.Error())
		return
	}
	// V2新增: 启动前再次执行完整后端校验（禁止仅依赖前端或保存接口校验）
	// V2.1扩展: 网关校验包含地址池冲突检查（含边界）
	gatewayIP, err := validateGateway(adapterIP, subnetMask, req.Gateway, poolStart, poolEnd)
	if err != nil {
		a.mu.Unlock()
		// V13: 网关错误统一返回 invalid_gateway 或 gateway_in_pool,禁止空 code
		code := errCodeInvalidGateway
		if strings.Contains(err.Error(), "地址池内") || strings.Contains(err.Error(), "地址池范围") {
			code = errCodeGatewayInPool
		}
		writeError(w, http.StatusBadRequest, code, err.Error())
		return
	}
	dnsIPs, err := normalizeDNSServers(req.DNSServers)
	if err != nil {
		a.mu.Unlock()
		// V14: DNS 数量超限使用独立错误码 dns_too_many,其他 DNS 错误用 invalid_dns
		dnsCode := errCodeInvalidDNS
		if strings.Contains(err.Error(), "最多允许 3 个") {
			dnsCode = errCodeDNSTooMany
		}
		writeError(w, http.StatusBadRequest, dnsCode, err.Error())
		return
	}

	// V1.0.3新增: 启动前校验固定映射（MAC 重复/IP 重复/跨网段/与网卡网关冲突）
	// V1.0.3修复: 阻断性校验仅对 enabled=true 生效,enabled=false 不阻止启动
	if code, err := validateReservationsForStart(a.config.StaticLeases, adapterIP, subnetMask, gatewayIP); err != nil {
		a.mu.Unlock()
		writeError(w, http.StatusBadRequest, code, err.Error())
		return
	}

	// V1.0.3新增: 将启用的固定映射传递给 DHCP 服务器
	a.dhcpSrv.SetStaticLeases(buildDHCPStaticLeases(a.config.StaticLeases))

	// 启动 DHCP 服务（V1修复: 地址池校验已移入 NewLeaseStore）
	// V2新增: 传入网关/DNS（已校验），为空则不下发 Option 3/6
	if err := a.dhcpSrv.Start(req.AdapterName, adapterIP, subnetMask, poolStart, poolEnd, req.LeaseMinutes, gatewayIP, dnsIPs); err != nil {
		a.mu.Unlock()
		// V11新增: 按 dhcpSrv.Start 错误内容映射稳定错误码
		// V13: 未匹配的错误统一返回 internal_error,禁止空 code
		msg := err.Error()
		code := errCodeInternalError
		if strings.Contains(msg, "最大支持") || strings.Contains(msg, "地址池过大") {
			code = errCodePoolTooLarge
		} else if strings.Contains(msg, "包含服务端 IP") || strings.Contains(msg, "包含网络地址") || strings.Contains(msg, "包含广播地址") {
			code = errCodePoolSpecialAddr
		} else if strings.Contains(msg, "起始地址不能大于结束地址") {
			code = errCodePoolOrderInvalid
		} else if strings.Contains(msg, "绑定 UDP 67") {
			code = errCodeBindPort67
		}
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}

	// 更新并保存配置（V2新增: 保存网关和规范化后的 DNS）
	// V14: saveConfig 失败时 DHCP 已启动,必须明确返回错误码 config_save_failed
	// 禁止前端只收到普通启动失败而误判服务状态(实际服务已启动)
	a.config.AdapterName = req.AdapterName
	a.config.PoolStart = req.PoolStart
	a.config.PoolEnd = req.PoolEnd
	a.config.LeaseMinutes = req.LeaseMinutes
	a.config.Gateway = strings.TrimSpace(req.Gateway)
	a.config.DNSServers = ipSliceToStrings(dnsIPs)
	if saveErr := a.saveConfig(); saveErr != nil {
		a.mu.Unlock()
		// 服务已启动但配置保存失败,日志记录并返回明确错误码
		a.logger.Log("DHCP 已启动但保存配置失败: %v", saveErr)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":               true,
			"config_save_fail": true,
			"code":             errCodeConfigSaveFailed,
			"error":            saveErr.Error(),
		})
		a.notifyStatusChange()
		return
	}

	// V4: 释放锁后再触发状态通知和返回 HTTP 响应，避免递归加锁
	a.mu.Unlock()

	a.notifyStatusChange()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStop 处理停止 DHCP 服务
// V6: 禁止持有 a.mu 调用 dhcpSrv.Stop()（Stop 内部等待协程退出，可能触发回调死锁）
// V13: 方法不允许统一使用 writeError
// P0安全收口: 明确要求空正文或空 JSON 对象 {},禁止携带超大或无关正文
func (a *AppServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	// P0安全收口: 校验正文必须为空或 {},禁止携带无关正文
	if err := requireEmptyBody(r); err != nil {
		if err == errPayloadTooLarge {
			writeError(w, http.StatusRequestEntityTooLarge, errCodePayloadTooLarge, "请求体过大")
			return
		}
		writeError(w, http.StatusBadRequest, errCodeInvalidRequest, "请求格式错误")
		return
	}

	a.mu.Lock()
	// V2: 退出过程中拒绝新操作
	if a.closing {
		a.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, errCodeServiceClosing, "服务正在关闭")
		return
	}
	running := a.dhcpSrv.IsRunning()
	a.mu.Unlock()

	if !running {
		writeError(w, http.StatusBadRequest, errCodeServiceNotRunning, "DHCP 服务未运行")
		return
	}

	// V6: 不持有 a.mu 调用 Stop()，Stop 内部会等待协程退出
	a.dhcpSrv.Stop()

	a.notifyStatusChange()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStatus 处理服务状态 API
// V13: 方法不允许统一使用 writeError
func (a *AppServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	status := a.dhcpSrv.Status()
	writeJSON(w, http.StatusOK, status)
}

// handleLeases 处理租约列表 API
// V13: 方法不允许统一使用 writeError
func (a *AppServer) handleLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	leases := a.dhcpSrv.Leases()
	if leases == nil {
		leases = []dhcp.LeaseJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"leases": leases})
}

// handleLogs 处理日志 API
// V13: 方法不允许统一使用 writeError
func (a *AppServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	logs := a.logger.GetRecentLogs()
	if logs == nil {
		logs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"logs": logs})
}

// handleLogsClear V12新增: 清空日志接口
// 调用 Logger.Clear 在互斥锁内清空内存环形缓冲区并截断当前日志文件
// 成功后前端立即刷新日志,禁止只清空前端 DOM 被下一次轮询恢复
// P0安全收口: 明确要求空正文或空 JSON 对象 {},禁止携带超大或无关正文
func (a *AppServer) handleLogsClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	// P0安全收口: 校验正文必须为空或 {},禁止携带无关正文
	if err := requireEmptyBody(r); err != nil {
		if err == errPayloadTooLarge {
			writeError(w, http.StatusRequestEntityTooLarge, errCodePayloadTooLarge, "请求体过大")
			return
		}
		writeError(w, http.StatusBadRequest, errCodeInvalidRequest, "请求格式错误")
		return
	}

	a.mu.RLock()
	closing := a.closing
	a.mu.RUnlock()
	if closing {
		writeError(w, http.StatusServiceUnavailable, errCodeServiceClosing, "服务正在关闭")
		return
	}

	if err := a.logger.Clear(); err != nil {
		writeError(w, http.StatusInternalServerError, errCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePoolRecommend 处理地址池推荐 API
// V1修复: 直接调用 RecommendPool 生产函数
// V13: 方法不允许统一使用 writeError
func (a *AppServer) handlePoolRecommend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}

	adapterName := r.URL.Query().Get("adapter_name")
	if adapterName == "" {
		writeError(w, http.StatusBadRequest, errCodeMissingAdapter, "缺少网卡名称")
		return
	}

	// V11新增: 使用 findAdapterIPWithCode 返回稳定错误码
	// V15: 测试钩子优先,允许测试绕过真实网卡枚举
	var adapterIP net.IP
	var subnetMask net.IPMask
	var adapterCode string
	var findErr error
	if a.testFindAdapterFunc != nil {
		adapterIP, subnetMask, findErr = a.testFindAdapterFunc(adapterName)
		if findErr != nil {
			adapterCode = errCodeAdapterNotFound
		}
	} else {
		adapterIP, subnetMask, adapterCode, findErr = findAdapterIPWithCode(adapterName)
	}
	if findErr != nil {
		writeError(w, http.StatusBadRequest, adapterCode, findErr.Error())
		return
	}

	poolStart, poolEnd, err := RecommendPool(adapterIP, subnetMask)
	if err != nil {
		// V13: 推荐失败统一返回 internal_error,禁止直接 writeJSON error
		writeError(w, http.StatusBadRequest, errCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"pool_start": poolStart,
		"pool_end":   poolEnd,
	})
}

// handleVersion 处理版本信息 API
// 读取 internal/version 唯一源，前端不得再次硬编码版本号
// V13: 方法不允许统一使用 writeError
func (a *AppServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
		return
	}
	a.mu.RLock()
	admin := a.isAdmin
	a.mu.RUnlock()
	// V10新增: 返回真实管理员权限状态,前端底部栏禁止伪造
	writeJSON(w, http.StatusOK, map[string]string{
		"version":      version.Version(),
		"file_version": version.FileVersion(),
		"product_name": version.ProductName(),
		"copyright":    version.Copyright(),
		"is_admin":     boolStr(admin),
	})
}

// boolStr V10新增: 布尔值转字符串,供 version 接口返回 is_admin
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// handleLanguage 处理语言 API
// 语言新增: GET 返回当前语言;PUT 仅接受 {"language":"zh-CN"} 或 {"language":"en-US"}
// 复用现有 CSRF/Host/Origin/Content-Type/64KB 限制和严格 JSON 解码(由中间件统一处理)
// 允许在 DHCP 运行期间切换语言,保存失败必须回滚内存语言并返回 language_save_failed
func (a *AppServer) handleLanguage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 返回当前语言,前端首次加载以服务端语言为准
		a.mu.RLock()
		lang := a.config.Language
		a.mu.RUnlock()
		if lang == "" {
			lang = i18n.GetLanguage()
		}
		writeJSON(w, http.StatusOK, map[string]string{"language": lang})

	case http.MethodPut:
		a.mu.Lock()

		// 退出过程中拒绝修改语言
		if a.closing {
			a.mu.Unlock()
			writeError(w, http.StatusServiceUnavailable, errCodeServiceClosing, "服务正在关闭")
			return
		}

		var req struct {
			Language string `json:"language"`
		}
		// P0安全: 使用 decodeJSONBody 严格解码,拒绝未知字段、多个对象、尾随垃圾和超限请求体
		if err := decodeJSONBody(r, &req); err != nil {
			a.mu.Unlock()
			if err == errPayloadTooLarge {
				writeError(w, http.StatusRequestEntityTooLarge, errCodePayloadTooLarge, "请求体过大")
				return
			}
			writeError(w, http.StatusBadRequest, errCodeInvalidRequest, "请求格式错误")
			return
		}

		// 语言重构: 严格只接受 zh-CN/en-US,拒绝 zh/en/english/中文 等别名
		// 复用 i18n.ParseLanguage 严格解析,空值或无效值返回 invalid_language
		rawLang := strings.TrimSpace(req.Language)
		parsed, ok := i18n.ParseLanguage(rawLang)
		if !ok {
			a.mu.Unlock()
			writeError(w, http.StatusBadRequest, errCodeInvalidLanguage, "不支持的语言")
			return
		}

		// 内存语言先备份,保存失败时回滚
		oldLang := a.config.Language
		// 先调用 i18n.SetLanguage 更新全局状态,失败时回滚
		// 注意:i18n.SetLanguage 是全局状态,回滚时必须恢复原值
		previousGlobalLang := i18n.GetLanguage()
		if !i18n.SetLanguage(parsed) {
			// 不应发生(ParseLanguage 已校验),防御性编程
			a.mu.Unlock()
			writeError(w, http.StatusBadRequest, errCodeInvalidLanguage, "不支持的语言")
			return
		}
		a.config.Language = parsed
		if err := a.saveConfig(); err != nil {
			// 保存失败:回滚内存语言和全局 i18n 状态,返回 language_save_failed
			a.config.Language = oldLang
			i18n.SetLanguage(previousGlobalLang)
			a.logger.Log("保存语言配置失败: %v", err)
			a.mu.Unlock()
			writeError(w, http.StatusInternalServerError, errCodeLanguageSaveFailed, err.Error())
			return
		}
		// 保存成功,捕获回调后释放锁,在锁外触发回调避免递归加锁
		// 回调内可能调用 UpdateStatus 等需要锁的方法,必须在锁外执行
		callback := a.onLanguageChange
		a.mu.Unlock()
		if callback != nil {
			callback()
		}
		writeJSON(w, http.StatusOK, map[string]string{"language": parsed})

	default:
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
	}
}

// RecommendPool 根据服务端 IP 和子网掩码计算推荐地址池
// V1修复: 提取为生产函数，测试直接测试此函数
// 算法：优先从服务端IP后方选择最多100个可用地址；后方不足时只从前方选择；
// 不得生成跨越服务端IP的范围；排除服务端IP、网络地址和广播地址
func RecommendPool(serverIP net.IP, subnetMask net.IPMask) (poolStart, poolEnd string, err error) {
	ipVal := ipToUint32(serverIP.To4())
	maskVal := binary.BigEndian.Uint32(subnetMask[:4])
	networkVal := ipVal & maskVal
	broadcastVal := networkVal | ^maskVal

	// 可用地址范围：网络地址+1 到 广播地址-1
	firstUsable := networkVal + 1
	lastUsable := broadcastVal - 1

	// 优先从服务端 IP 后方选择（ipVal+1 到 min(ipVal+100, lastUsable)）
	afterStart := ipVal + 1
	afterEnd := ipVal + 100
	if afterEnd > lastUsable {
		afterEnd = lastUsable
	}
	afterCount := uint32(0)
	if afterStart <= afterEnd {
		afterCount = afterEnd - afterStart + 1
	}

	if afterCount > 0 {
		// 后方有空间，使用后方
		return uint32ToIPStr(afterStart), uint32ToIPStr(afterEnd), nil
	}

	// 后方无空间，从前方选择（max(firstUsable, ipVal-100) 到 ipVal-1）
	beforeStart := ipVal - 100
	if beforeStart < firstUsable {
		beforeStart = firstUsable
	}
	beforeEnd := ipVal - 1
	if beforeStart <= beforeEnd {
		return uint32ToIPStr(beforeStart), uint32ToIPStr(beforeEnd), nil
	}

	// 前后均无可用空间
	return "", "", fmt.Errorf("子网地址空间不足，无法推荐地址池")
}

// validateGateway 校验可选网关（V2新增，V2.1扩展：地址池冲突统一校验）
// 空字符串合法（不下发 Option 3）。
// adapterIP/subnetMask 为 nil 时仅校验 IPv4 格式（用于配置保存时网卡信息不足的场景）。
// adapterIP/subnetMask 非 nil 时继续校验同子网、非网络地址、非广播地址。
// poolStart/poolEnd 非 nil 时额外校验网关不在地址池内（含边界）。
// 允许网关等于服务端网卡 IP（此时服务端 IP 仍由地址池校验排除，不会落入池内）。
// 返回 net.IP（4 字节）和校验错误；空网关返回 (nil, nil)
func validateGateway(adapterIP net.IP, subnetMask net.IPMask, gateway string, poolStart, poolEnd net.IP) (net.IP, error) {
	gateway = strings.TrimSpace(gateway)
	if gateway == "" {
		return nil, nil // 合法：不下发 Option 3
	}
	gw := net.ParseIP(gateway)
	if gw == nil || gw.To4() == nil {
		return nil, fmt.Errorf("网关地址无效: %s", gateway)
	}
	gw4 := gw.To4()

	// 网卡信息充足时校验同子网、非网络/广播地址
	if adapterIP != nil && len(subnetMask) >= net.IPv4len {
		// 必须与网卡处于同一子网（允许等于网卡 IP）
		if !sameSubnetIPv4(adapterIP, gw4, subnetMask) {
			return nil, fmt.Errorf("网关 %s 与网卡 %s 不在同一子网", gateway, adapterIP.String())
		}
		// 不得为网络地址
		maskVal := binary.BigEndian.Uint32(subnetMask[:4])
		gwVal := binary.BigEndian.Uint32(gw4)
		networkVal := gwVal & maskVal
		if gwVal == networkVal {
			return nil, fmt.Errorf("网关 %s 不能是网络地址", gateway)
		}
		// 不得为广播地址
		broadcastVal := networkVal | ^maskVal
		if gwVal == broadcastVal {
			return nil, fmt.Errorf("网关 %s 不能是广播地址", gateway)
		}
	}

	// V2.1新增: 校验网关不在地址池内（含边界），使用 uint32 数值比较
	if poolStart != nil && poolEnd != nil {
		ps := poolStart.To4()
		pe := poolEnd.To4()
		if ps != nil && pe != nil {
			gwVal := binary.BigEndian.Uint32(gw4)
			startVal := binary.BigEndian.Uint32(ps)
			endVal := binary.BigEndian.Uint32(pe)
			if gwVal >= startVal && gwVal <= endVal {
				return nil, fmt.Errorf("默认网关位于DHCP地址池内，请调整网关或地址池范围")
			}
		}
	}
	return gw4, nil
}

// normalizeDNSServers 规范化 DNS 服务器列表（V2新增）
// 处理：去除首尾空格、丢弃空项、去重并保持用户输入顺序、最多允许 3 个、校验每项为合法 IPv4
// 入参为空或全部为空时返回 nil（不下发 Option 6）
func normalizeDNSServers(input []string) ([]net.IP, error) {
	seen := make(map[string]bool)
	var result []string
	for _, raw := range input {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if seen[s] {
			continue // 去重，保持首次出现顺序
		}
		seen[s] = true
		result = append(result, s)
	}
	if len(result) == 0 {
		return nil, nil // 合法：不下发 Option 6
	}
	if len(result) > 3 {
		// V14: 使用独立错误前缀 dns_too_many 便于调用方映射稳定错误码
		// 不再在消息中嵌入"当前N个"动态参数,前端按 code 提供完整中英文文案
		return nil, fmt.Errorf("DNS 服务器最多允许 3 个")
	}
	var ips []net.IP
	for _, s := range result {
		ip := net.ParseIP(s)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("DNS 地址无效: %s", s)
		}
		ips = append(ips, ip.To4())
	}
	return ips, nil
}

// sameSubnetIPv4 判断两个 IPv4 地址是否处于同一子网
// V2新增: server 包内网关校验使用，避免依赖 dhcp 包未导出函数
func sameSubnetIPv4(a, b net.IP, mask net.IPMask) bool {
	a4 := a.To4()
	b4 := b.To4()
	if a4 == nil || b4 == nil || len(mask) < 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if a4[i]&mask[i] != b4[i]&mask[i] {
			return false
		}
	}
	return true
}

// validatePoolSubnet 校验地址池和网关必须与所选网卡处于同一子网（V1.0.2新增）
// 使用 IP 与子网掩码按位计算网段（复用 sameSubnetIPv4），禁止用字符串前缀判断
// adapterIP/subnetMask 为 nil 时跳过子网校验（用于配置保存时网卡未选定的场景）
// poolStart/poolEnd 为 nil 时跳过对应地址的子网校验
// gateway 为空时跳过网关子网校验
// 返回稳定错误码（pool_subnet_mismatch / gateway_subnet_mismatch）和错误；合法时返回 ("", nil)
func validatePoolSubnet(adapterIP net.IP, subnetMask net.IPMask, gateway string, poolStart, poolEnd net.IP) (string, error) {
	// 网卡信息不足时无法进行子网校验
	if adapterIP == nil || len(subnetMask) < 4 {
		return "", nil
	}

	// 校验地址池起始 IP 与网卡同子网
	if poolStart != nil {
		if ps4 := poolStart.To4(); ps4 != nil && !sameSubnetIPv4(adapterIP, ps4, subnetMask) {
			return errCodePoolSubnetMismatch, fmt.Errorf("地址池必须与所选网卡处于同一网段")
		}
	}
	// 校验地址池结束 IP 与网卡同子网
	if poolEnd != nil {
		if pe4 := poolEnd.To4(); pe4 != nil && !sameSubnetIPv4(adapterIP, pe4, subnetMask) {
			return errCodePoolSubnetMismatch, fmt.Errorf("地址池必须与所选网卡处于同一网段")
		}
	}

	// 校验网关与地址池同子网（地址池已校验与网卡同子网，故等价于与网卡同子网）
	gateway = strings.TrimSpace(gateway)
	if gateway != "" {
		gw := net.ParseIP(gateway)
		if gw != nil {
			if gw4 := gw.To4(); gw4 != nil && !sameSubnetIPv4(adapterIP, gw4, subnetMask) {
				return errCodeGatewaySubnetMismatch, fmt.Errorf("网关必须与地址池处于同一网段")
			}
		}
	}

	return "", nil
}

// ipSliceToStrings 将 []net.IP 转换为规范化字符串切片（V2新增）
// 用于将校验后的 DNS 列表存入配置；空切片返回 nil
func ipSliceToStrings(ips []net.IP) []string {
	if len(ips) == 0 {
		return nil
	}
	result := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			result = append(result, ip4.String())
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// saveConfig 保存配置到文件
// V14修复: 返回 error,调用方据决定是否更新内存配置或返回错误
// V16重构: 使用 os.CreateTemp 创建唯一临时文件,依次执行 Write/Sync/Close 后再替换正式文件
// 任何步骤失败都关闭并清理临时文件,保证原 config.json 不被破坏
// 注: Windows 上 os.Rename 不保证原子性,本方法尽力保证配置完整性(安全替换),
//
//	不宣称 Windows 下绝对原子
func (a *AppServer) saveConfig() error {
	configPath := filepath.Join(a.configDir, "config.json")
	data, err := json.MarshalIndent(a.config, "", "  ")
	if err != nil {
		a.logger.Log("保存配置失败: %v", err)
		return fmt.Errorf("序列化配置失败: %v", err)
	}

	// V16: 使用可注入的写入函数,测试可模拟 Write/Sync/Close 失败
	writer := a.testSaveConfigWriter
	if writer == nil {
		writer = defaultSaveConfigWriter
	}
	tmpPath, err := writer(a.configDir, data)
	if err != nil {
		a.logger.Log("写入临时配置文件失败: %v", err)
		return fmt.Errorf("写入临时配置文件失败: %v", err)
	}

	// V16: 使用可注入的替换函数,测试可模拟 Rename 失败
	replacer := a.testSaveConfigReplacer
	if replacer == nil {
		replacer = defaultSaveConfigReplacer
	}
	if err := replacer(tmpPath, configPath); err != nil {
		// V17修复: 错误前缀由 saveConfig 统一包装,replacer 内部不再重复添加前缀
		a.logger.Log("替换配置文件失败: %v", err)
		// 替换失败时清理临时文件,保持目录整洁,原 config.json 不受影响
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换配置文件失败: %v", err)
	}
	return nil
}

// defaultSaveConfigWriter 默认的临时文件写入实现(安全替换/尽力保证完整性)
// 使用 os.CreateTemp 在配置目录创建唯一临时文件,依次执行 Write/Sync/Close
// 任何步骤失败都关闭并清理临时文件,返回错误
func defaultSaveConfigWriter(dir string, data []byte) (string, error) {
	// os.CreateTemp 在 dir 目录创建唯一临时文件,文件名前缀 config,自动生成随机后缀
	f, err := os.CreateTemp(dir, "config.json.*.tmp")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %v", err)
	}
	tmpPath := f.Name()

	// 任何失败路径都需要关闭并清理临时文件
	success := false
	defer func() {
		if !success {
			// Close 幂等,多次调用安全;Remove 忽略错误
			_ = f.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	// 步骤 1: Write
	if _, err := f.Write(data); err != nil {
		return tmpPath, fmt.Errorf("写入临时文件失败: %v", err)
	}
	// 步骤 2: Sync (尽量保证数据落盘)
	if err := f.Sync(); err != nil {
		return tmpPath, fmt.Errorf("同步临时文件失败: %v", err)
	}
	// 步骤 3: Close (关闭后再替换,避免文件句柄占用)
	if err := f.Close(); err != nil {
		return tmpPath, fmt.Errorf("关闭临时文件失败: %v", err)
	}

	success = true
	return tmpPath, nil
}

// defaultSaveConfigReplacer 默认的文件替换实现(安全替换/尽力保证完整性)
// 使用 os.Rename 替换正式配置文件
// 注: Windows 上 os.Rename 不保证原子性,本方法尽力保证配置完整性
// V17修复: 错误前缀由 saveConfig 统一包装,本函数直接返回 os.Rename 原始错误
func defaultSaveConfigReplacer(tmpPath, configPath string) error {
	return os.Rename(tmpPath, configPath)
}

// findAdapterIP 查找指定网卡的 IPv4 地址和子网掩码
// V1修复: 统一使用 network.GetAdapterIPByName（共享 OperStatus 判断逻辑）
func findAdapterIP(name string) (net.IP, net.IPMask, error) {
	return network.GetAdapterIPByName(name)
}

// V11新增: findAdapterIPWithCode 返回稳定错误码,供前端按 code 翻译
// 错误码: adapter_not_found / adapter_down / adapter_no_ipv4
func findAdapterIPWithCode(name string) (net.IP, net.IPMask, string, error) {
	ip, mask, err := network.GetAdapterIPByName(name)
	if err != nil {
		msg := err.Error()
		code := errCodeAdapterNotFound
		if strings.Contains(msg, "未连接") || strings.Contains(msg, "已禁用") {
			code = errCodeAdapterDown
		} else if strings.Contains(msg, "没有 IPv4") {
			code = errCodeAdapterNoIPv4
		}
		return nil, nil, code, err
	}
	return ip, mask, "", nil
}

// ipToUint32 将 IPv4 转换为 uint32（服务器端辅助函数）
func ipToUint32(ip net.IP) uint32 {
	if ip == nil {
		return 0
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip4)
}

// uint32ToIPStr 将 uint32 转换为 IPv4 字符串
func uint32ToIPStr(val uint32) string {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, val)
	return ip.String()
}

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// V11新增: 稳定错误码常量,前端按 code 翻译,避免依赖中文片段匹配
// 后端错误优先返回稳定 code,同时保留 msg 兼容旧前端
// V13: 补齐 invalid_gateway/invalid_dns,所有 API 错误统一非空 code
const (
	errCodeMethodNotAllowed      = "method_not_allowed"
	errCodeServiceRunning        = "service_running"
	errCodeServiceNotRunning     = "service_not_running"
	errCodeServiceClosing        = "service_closing"
	errCodeInvalidConfig         = "invalid_config"
	errCodeInvalidRequest        = "invalid_request"
	errCodeMissingAdapter        = "missing_adapter"
	errCodeAdapterNotFound       = "adapter_not_found"
	errCodeAdapterNoIPv4         = "adapter_no_ipv4"
	errCodeAdapterDown           = "adapter_down"
	errCodePoolTooLarge          = "pool_too_large"
	errCodePoolSpecialAddr       = "pool_special_addr"
	errCodePoolOrderInvalid      = "pool_order_invalid"
	errCodeGatewayInPool         = "gateway_in_pool"
	errCodeBindPort67            = "bind_port_67"
	errCodeInternalError         = "internal_error"          // V12新增: 日志清空等内部错误
	errCodeInvalidGateway        = "invalid_gateway"         // V13新增: 网关地址无效/非同子网/网络地址/广播地址
	errCodeInvalidDNS            = "invalid_dns"             // V13新增: DNS 地址无效
	errCodeDNSTooMany            = "dns_too_many"            // V14新增: DNS 数量超限(独立稳定码,前端完整文案)
	errCodeConfigSaveFailed      = "config_save_failed"      // V14新增: 配置写入文件失败
	errCodePoolSubnetMismatch    = "pool_subnet_mismatch"    // V1.0.2新增: 地址池与网卡不在同一子网
	errCodeGatewaySubnetMismatch = "gateway_subnet_mismatch" // V1.0.2新增: 网关与地址池不在同一子网
	// V1.0.3新增: 固定映射校验错误码
	errCodeResvMACInvalid        = "resv_mac_invalid"        // MAC 地址格式无效
	errCodeResvIPInvalid         = "resv_ip_invalid"         // IP 地址格式无效
	errCodeResvMACDuplicate      = "resv_mac_duplicate"      // MAC 地址重复
	errCodeResvIPDuplicate       = "resv_ip_duplicate"       // IP 地址重复
	errCodeResvSubnetMismatch    = "resv_subnet_mismatch"    // 固定 IP 与网卡不在同一网段
	errCodeResvConflictAdapter   = "resv_conflict_adapter"   // 固定 IP 等于网卡 IP
	errCodeResvConflictGateway   = "resv_conflict_gateway"   // 固定 IP 等于网关 IP
	errCodeResvConflictNetwork   = "resv_conflict_network"   // 固定 IP 等于网络地址
	errCodeResvConflictBroadcast = "resv_conflict_broadcast" // 固定 IP 等于广播地址
)

// writeError V11新增: 写入带稳定错误码的 JSON 响应
// code 供前端按错误码翻译,msg 保留中文原文向后兼容
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

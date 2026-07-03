package server

import (
	"DacatDHCP/internal/dhcp"
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
)

// Config 持久化配置
type Config struct {
	AdapterName  string   `json:"adapter_name"`
	PoolStart    string   `json:"pool_start"`
	PoolEnd      string   `json:"pool_end"`
	LeaseMinutes int      `json:"lease_minutes"`
	WebPort      int      `json:"web_port"`
	Gateway      string   `json:"gateway"`     // V2新增: 默认网关（可选，空则不下发 Option 3）
	DNSServers   []string `json:"dns_servers"` // V2新增: DNS 服务器（可选，空则不下发 Option 6）
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
}

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

	// 创建 DHCP 服务器
	dhcpSrv := dhcp.NewServer()
	dhcpSrv.SetLogFunc(func(format string, args ...interface{}) {
		logger.Log(format, args...)
	})

	return &AppServer{
		dhcpSrv:   dhcpSrv,
		logger:    logger,
		config:    config,
		configDir: dataDir,
		webFS:     webFS,
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

// Start 启动 HTTP 服务器（V1修复: 先绑定端口，确认成功后再开始服务）
// 返回监听地址，供调用方在确认绑定成功后打开浏览器
func (a *AppServer) Start() error {
	mux := http.NewServeMux()

	// 静态文件
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/style.css", a.handleStatic("style.css", "text/css"))
	mux.HandleFunc("/app.js", a.handleStatic("app.js", "application/javascript"))
	mux.HandleFunc("/favicon.ico", a.handleStatic("dhcp.ico", "image/x-icon")) // V1修复: Web favicon

	// API 路由
	mux.HandleFunc("/api/adapters", a.handleAdapters)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/start", a.handleStart)
	mux.HandleFunc("/api/stop", a.handleStop)
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/leases", a.handleLeases)
	mux.HandleFunc("/api/logs", a.handleLogs)
	mux.HandleFunc("/api/pool-recommend", a.handlePoolRecommend) // V1修复: 后端计算地址池推荐
	mux.HandleFunc("/api/version", a.handleVersion)              // 版本信息：读取 internal/version 唯一源

	addr := fmt.Sprintf("127.0.0.1:%d", a.config.WebPort)

	// V1修复: 先尝试绑定端口，端口占用时明确报错
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("管理端口 %s 被占用或无法绑定: %v", addr, err)
	}

	a.listener = listener
	a.httpSrv = &http.Server{Handler: mux}

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
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
func (a *AppServer) handleAdapters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	adapters, err := network.EnumerateAdapters()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"adapters": adapters})
}

// handleConfig 处理配置 API
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
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "服务运行中，无法修改配置"})
			return
		}

		var cfg Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "配置格式错误"})
			return
		}

		// V2新增: 保存时规范化网关和 DNS（去空格、去重、最多3个、校验 IPv4）
		// V2.1扩展: 已选择有效网卡并存在地址池时，继续校验同子网、非网络/广播、不在池内
		cfg.Gateway = strings.TrimSpace(cfg.Gateway)
		// 尝试解析网卡信息（网卡信息不足时仅校验 IPv4 格式）
		var adapterIP net.IP
		var subnetMask net.IPMask
		if cfg.AdapterName != "" {
			adapterIP, subnetMask, _ = findAdapterIP(cfg.AdapterName)
		}
		// 解析地址池（缺失时跳过池内检查）
		var poolStart, poolEnd net.IP
		if cfg.PoolStart != "" {
			poolStart = net.ParseIP(cfg.PoolStart)
		}
		if cfg.PoolEnd != "" {
			poolEnd = net.ParseIP(cfg.PoolEnd)
		}
		// 复用统一校验函数（禁止重复编写判断逻辑）
		_, err := validateGateway(adapterIP, subnetMask, cfg.Gateway, poolStart, poolEnd)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		dnsIPs, err := normalizeDNSServers(cfg.DNSServers)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cfg.DNSServers = ipSliceToStrings(dnsIPs)

		a.config = cfg
		a.saveConfig()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
	}
}

// handleStart 处理启动 DHCP 服务
func (a *AppServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	a.mu.Lock()

	// V2: 退出过程中拒绝新操作
	if a.closing {
		a.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务正在关闭"})
		return
	}

	if a.dhcpSrv.IsRunning() {
		a.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "DHCP 服务已在运行"})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}

	// 查找网卡的 IP 和子网掩码
	adapterIP, subnetMask, err := findAdapterIP(req.AdapterName)
	if err != nil {
		a.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	poolStart := net.ParseIP(req.PoolStart)
	poolEnd := net.ParseIP(req.PoolEnd)

	// V2新增: 启动前再次执行完整后端校验（禁止仅依赖前端或保存接口校验）
	// V2.1扩展: 网关校验包含地址池冲突检查（含边界）
	gatewayIP, err := validateGateway(adapterIP, subnetMask, req.Gateway, poolStart, poolEnd)
	if err != nil {
		a.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	dnsIPs, err := normalizeDNSServers(req.DNSServers)
	if err != nil {
		a.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// 启动 DHCP 服务（V1修复: 地址池校验已移入 NewLeaseStore）
	// V2新增: 传入网关/DNS（已校验），为空则不下发 Option 3/6
	if err := a.dhcpSrv.Start(req.AdapterName, adapterIP, subnetMask, poolStart, poolEnd, req.LeaseMinutes, gatewayIP, dnsIPs); err != nil {
		a.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// 更新并保存配置（V2新增: 保存网关和规范化后的 DNS）
	a.config.AdapterName = req.AdapterName
	a.config.PoolStart = req.PoolStart
	a.config.PoolEnd = req.PoolEnd
	a.config.LeaseMinutes = req.LeaseMinutes
	a.config.Gateway = strings.TrimSpace(req.Gateway)
	a.config.DNSServers = ipSliceToStrings(dnsIPs)
	a.saveConfig()

	// V4: 释放锁后再触发状态通知和返回 HTTP 响应，避免递归加锁
	a.mu.Unlock()

	a.notifyStatusChange()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStop 处理停止 DHCP 服务
// V6: 禁止持有 a.mu 调用 dhcpSrv.Stop()（Stop 内部等待协程退出，可能触发回调死锁）
func (a *AppServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	a.mu.Lock()
	// V2: 退出过程中拒绝新操作
	if a.closing {
		a.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务正在关闭"})
		return
	}
	running := a.dhcpSrv.IsRunning()
	a.mu.Unlock()

	if !running {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "DHCP 服务未运行"})
		return
	}

	// V6: 不持有 a.mu 调用 Stop()，Stop 内部会等待协程退出
	a.dhcpSrv.Stop()

	a.notifyStatusChange()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStatus 处理服务状态 API
func (a *AppServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	status := a.dhcpSrv.Status()
	writeJSON(w, http.StatusOK, status)
}

// handleLeases 处理租约列表 API
func (a *AppServer) handleLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	leases := a.dhcpSrv.Leases()
	if leases == nil {
		leases = []dhcp.LeaseJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"leases": leases})
}

// handleLogs 处理日志 API
func (a *AppServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	logs := a.logger.GetRecentLogs()
	if logs == nil {
		logs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"logs": logs})
}

// handlePoolRecommend 处理地址池推荐 API
// V1修复: 直接调用 RecommendPool 生产函数
func (a *AppServer) handlePoolRecommend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	adapterName := r.URL.Query().Get("adapter_name")
	if adapterName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少网卡名称"})
		return
	}

	adapterIP, subnetMask, err := findAdapterIP(adapterName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	poolStart, poolEnd, err := RecommendPool(adapterIP, subnetMask)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"pool_start": poolStart,
		"pool_end":   poolEnd,
	})
}

// handleVersion 处理版本信息 API
// 读取 internal/version 唯一源，前端不得再次硬编码版本号
func (a *AppServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"version":      version.Version(),
		"file_version": version.FileVersion(),
		"product_name": version.ProductName(),
		"copyright":    version.Copyright(),
	})
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
		return nil, fmt.Errorf("DNS 服务器最多允许 3 个，当前 %d 个", len(result))
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
func (a *AppServer) saveConfig() {
	configPath := filepath.Join(a.configDir, "config.json")
	data, err := json.MarshalIndent(a.config, "", "  ")
	if err != nil {
		a.logger.Log("保存配置失败: %v", err)
		return
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		a.logger.Log("写入配置文件失败: %v", err)
	}
}

// findAdapterIP 查找指定网卡的 IPv4 地址和子网掩码
// V1修复: 统一使用 network.GetAdapterIPByName（共享 OperStatus 判断逻辑）
func findAdapterIP(name string) (net.IP, net.IPMask, error) {
	return network.GetAdapterIPByName(name)
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

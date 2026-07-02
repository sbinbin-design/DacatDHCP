package server

import (
	"DacatDHCP/internal/dhcp"
	"DacatDHCP/internal/network"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Config 持久化配置
type Config struct {
	AdapterName  string `json:"adapter_name"`
	PoolStart    string `json:"pool_start"`
	PoolEnd      string `json:"pool_end"`
	LeaseMinutes int    `json:"lease_minutes"`
	WebPort      int    `json:"web_port"`
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
	mu        sync.RWMutex
	dhcpSrv   *dhcp.Server
	logger    *Logger
	config    Config
	configDir string
	webFS     fs.FS
	listener  net.Listener // V1修复: 先创建 listener，确认绑定成功再开浏览器
	httpSrv   *http.Server
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

// Close 关闭服务器（V1修复: 关闭 HTTP Server、DHCP 监听、定时任务和全部协程）
func (a *AppServer) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 停止 DHCP 服务
	if a.dhcpSrv != nil && a.dhcpSrv.IsRunning() {
		a.dhcpSrv.Stop()
	}

	// 关闭 HTTP 服务器
	if a.httpSrv != nil {
		a.httpSrv.Close()
	}

	// 关闭日志
	if a.logger != nil {
		a.logger.Close()
	}
}

// ListenAddr 返回实际监听地址
func (a *AppServer) ListenAddr() string {
	if a.listener != nil {
		return a.listener.Addr().String()
	}
	return ""
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
	defer a.mu.Unlock()

	if a.dhcpSrv.IsRunning() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "DHCP 服务已在运行"})
		return
	}

	var req struct {
		AdapterName  string `json:"adapter_name"`
		PoolStart    string `json:"pool_start"`
		PoolEnd      string `json:"pool_end"`
		LeaseMinutes int    `json:"lease_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}

	// 查找网卡的 IP 和子网掩码
	adapterIP, subnetMask, err := findAdapterIP(req.AdapterName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	poolStart := net.ParseIP(req.PoolStart)
	poolEnd := net.ParseIP(req.PoolEnd)

	// 启动 DHCP 服务（V1修复: 地址池校验已移入 NewLeaseStore）
	if err := a.dhcpSrv.Start(req.AdapterName, adapterIP, subnetMask, poolStart, poolEnd, req.LeaseMinutes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// 更新并保存配置
	a.config.AdapterName = req.AdapterName
	a.config.PoolStart = req.PoolStart
	a.config.PoolEnd = req.PoolEnd
	a.config.LeaseMinutes = req.LeaseMinutes
	a.saveConfig()

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStop 处理停止 DHCP 服务
func (a *AppServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"方法不允许"}`, http.StatusMethodNotAllowed)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.dhcpSrv.IsRunning() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "DHCP 服务未运行"})
		return
	}

	a.dhcpSrv.Stop()
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
// V1修复: 优先推荐服务端IP后方地址，空间不足时从前方可用地址选择，排除服务端IP
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

	// 使用 uint32 数值计算，正确支持非 /24 网段
	ipVal := ipToUint32(adapterIP.To4())
	maskVal := binary.BigEndian.Uint32(subnetMask[:4])
	networkVal := ipVal & maskVal
	broadcastVal := networkVal | ^maskVal

	// 可用地址范围：网络地址+1 到 广播地址-1
	firstUsable := networkVal + 1
	lastUsable := broadcastVal - 1

	// V1修复: 优先推荐服务端 IP 后方地址，空间不足时从前方补充
	// 目标: 推荐约100个地址（ipVal+10 到 ipVal+110），排除服务端 IP

	// 第一段：服务端 IP +10 到 min(IP+110, lastUsable)
	startVal := ipVal + 10
	endVal := ipVal + 110

	// 如果后方空间不足，扩展到前方
	if endVal > lastUsable {
		overflow := endVal - lastUsable
		endVal = lastUsable
		// 从前方补充溢出部分
		startVal = ipVal - overflow
		if startVal <= firstUsable {
			startVal = firstUsable
		}
	}

	// 确保起始不小于第一个可用地址
	if startVal < firstUsable {
		startVal = firstUsable
	}
	// 确保结束不大于最后一个可用地址
	if endVal > lastUsable {
		endVal = lastUsable
	}

	// 排除服务端 IP（如果刚好在推荐范围内）
	if startVal == ipVal {
		startVal = ipVal + 1
	}
	if endVal == ipVal {
		endVal = ipVal - 1
	}

	// 限制地址池最大 4096
	poolSize := endVal - startVal + 1
	if poolSize > 4096 {
		endVal = startVal + 4095
	}

	// 确保起始不大于结束
	if startVal > endVal {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "子网地址空间不足，无法推荐地址池"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"pool_start": uint32ToIPStr(startVal),
		"pool_end":   uint32ToIPStr(endVal),
	})
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

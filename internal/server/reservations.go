package server

// V1.0.3新增: MAC-IP 固定映射（reservations/static_leases）管理
// 包含: MAC 规范化、IP 校验、重复检测、API handler、启动前校验
// 持久化到 config.json 的 static_leases 字段,不引入数据库

import (
	"DacatDHCP/internal/dhcp"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// macColonRe 匹配冒号分隔的 MAC 地址: XX:XX:XX:XX:XX:XX
var macColonRe = regexp.MustCompile(`^[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}$`)

// macHyphenRe 匹配连字符分隔的 MAC 地址: XX-XX-XX-XX-XX-XX
var macHyphenRe = regexp.MustCompile(`^[0-9A-Fa-f]{2}(-[0-9A-Fa-f]{2}){5}$`)

// normalizeMAC 将 MAC 地址规范化为大写冒号格式
// 支持 00:11:22:33:44:55 和 00-11-22-33-44-55 两种输入
// 返回规范化后的 MAC（如 00:11:22:33:44:55）和是否有效
func normalizeMAC(mac string) (string, bool) {
	mac = strings.TrimSpace(mac)
	if mac == "" {
		return "", false
	}
	if !macColonRe.MatchString(mac) && !macHyphenRe.MatchString(mac) {
		return "", false
	}
	// 连字符替换为冒号,转大写
	normalized := strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
	return normalized, true
}

// isValidIPv4 检查字符串是否为合法 IPv4
func isValidIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// handleReservations 处理 MAC-IP 固定映射 API（V1.0.3新增）
// GET  /api/reservations  — 返回当前固定映射列表
// PUT  /api/reservations  — 替换整个固定映射列表（前端管理全量列表）
func (a *AppServer) handleReservations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		leases := a.config.StaticLeases
		a.mu.RUnlock()
		if leases == nil {
			leases = []StaticLeaseConfig{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"reservations": leases})

	case http.MethodPut:
		a.mu.Lock()
		defer a.mu.Unlock()

		// 运行时禁止修改固定映射（与配置一致,停止后才能修改）
		if a.dhcpSrv.IsRunning() {
			writeError(w, http.StatusBadRequest, errCodeServiceRunning, "服务运行中，无法修改配置")
			return
		}
		if a.closing {
			writeError(w, http.StatusServiceUnavailable, errCodeServiceClosing, "服务正在关闭")
			return
		}

		var req struct {
			Reservations []StaticLeaseConfig `json:"reservations"`
		}
		if err := decodeJSONBody(r, &req); err != nil {
			if err == errPayloadTooLarge {
				writeError(w, http.StatusRequestEntityTooLarge, errCodePayloadTooLarge, "请求体过大")
				return
			}
			writeError(w, http.StatusBadRequest, errCodeInvalidRequest, "请求格式错误")
			return
		}

		// 获取当前网卡信息用于子网校验（网卡可能未选择）
		var adapterIP net.IP
		var subnetMask net.IPMask
		var gatewayStr string
		if a.config.AdapterName != "" {
			if a.testFindAdapterFunc != nil {
				adapterIP, subnetMask, _ = a.testFindAdapterFunc(a.config.AdapterName)
			} else {
				adapterIP, subnetMask, _ = findAdapterIP(a.config.AdapterName)
			}
		}
		gatewayStr = strings.TrimSpace(a.config.Gateway)

		// 构建新列表并校验
		newList, code, err := processReservations(req.Reservations, a.config.StaticLeases, adapterIP, subnetMask, gatewayStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, code, err.Error())
			return
		}

		// 保存到配置
		oldLeases := a.config.StaticLeases
		a.config.StaticLeases = newList
		if err := a.saveConfig(); err != nil {
			a.config.StaticLeases = oldLeases
			writeError(w, http.StatusInternalServerError, errCodeConfigSaveFailed, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, errCodeMethodNotAllowed, "方法不允许")
	}
}

// processReservations 处理固定映射列表: 规范化 MAC、管理时间戳、校验重复和冲突
// existing 为当前已保存的列表（用于保留 created_at 时间戳）
// adapterIP/subnetMask 为 nil 时跳过子网相关校验
// gatewayStr 为空时跳过网关冲突校验
func processReservations(input []StaticLeaseConfig, existing []StaticLeaseConfig,
	adapterIP net.IP, subnetMask net.IPMask, gatewayStr string) ([]StaticLeaseConfig, string, error) {

	// 建立已存在 MAC 的索引,用于保留 created_at
	existingMap := make(map[string]StaticLeaseConfig, len(existing))
	for _, e := range existing {
		existingMap[e.MAC] = e
	}

	now := time.Now().Format(time.RFC3339)
	macSeen := make(map[string]bool)
	ipSeen := make(map[string]bool)

	var result []StaticLeaseConfig

	for _, item := range input {
		// 规范化 MAC
		normalizedMAC, ok := normalizeMAC(item.MAC)
		if !ok {
			return nil, errCodeResvMACInvalid, fmt.Errorf("MAC 地址格式无效: %s", item.MAC)
		}
		item.MAC = normalizedMAC

		// 校验 IP 格式
		ipStr := strings.TrimSpace(item.IP)
		if !isValidIPv4(ipStr) {
			return nil, errCodeResvIPInvalid, fmt.Errorf("IP 地址格式无效: %s", item.IP)
		}
		item.IP = ipStr

		// 校验 MAC 不重复
		if macSeen[normalizedMAC] {
			return nil, errCodeResvMACDuplicate, fmt.Errorf("MAC 地址重复: %s", normalizedMAC)
		}
		macSeen[normalizedMAC] = true

		// 校验 IP 不重复
		ipLower := strings.ToLower(ipStr)
		if ipSeen[ipLower] {
			return nil, errCodeResvIPDuplicate, fmt.Errorf("IP 地址重复: %s", ipStr)
		}
		ipSeen[ipLower] = true

		// V1.0.3修复: 同网段/网卡 IP/网关 IP/网络地址/广播地址等运行阻断类校验仅对 enabled=true 生效;
		// enabled=false 的映射即使跨网段也允许保存,不应影响用户编辑备注、新增其他映射或保存列表
		if !item.Enabled {
			// 跳过阻断性校验,继续处理时间戳和追加结果
		} else if adapterIP != nil && len(subnetMask) >= net.IPv4len {
			fixedIP := net.ParseIP(ipStr)
			if fixedIP != nil {
				fixedIP4 := fixedIP.To4()
				// 校验同网段
				if !sameSubnetIPv4(adapterIP, fixedIP4, subnetMask) {
					return nil, errCodeResvSubnetMismatch, fmt.Errorf("固定 IP %s 与网卡 %s 不在同一网段", ipStr, adapterIP.String())
				}
				// 不能等于网卡 IP
				if fixedIP4.Equal(adapterIP.To4()) {
					return nil, errCodeResvConflictAdapter, fmt.Errorf("固定 IP %s 不能等于网卡 IP", ipStr)
				}
				// 不能等于网关 IP
				if gatewayStr != "" {
					gw := net.ParseIP(gatewayStr)
					if gw != nil && fixedIP4.Equal(gw.To4()) {
						return nil, errCodeResvConflictGateway, fmt.Errorf("固定 IP %s 不能等于网关 IP", ipStr)
					}
				}
				// 不能是网络地址
				maskVal := binary.BigEndian.Uint32(subnetMask[:4])
				ipVal := binary.BigEndian.Uint32(fixedIP4)
				networkVal := ipVal & maskVal
				if ipVal == networkVal {
					return nil, errCodeResvConflictNetwork, fmt.Errorf("固定 IP %s 不能是网络地址", ipStr)
				}
				// 不能是广播地址
				broadcastVal := networkVal | ^maskVal
				if ipVal == broadcastVal {
					return nil, errCodeResvConflictBroadcast, fmt.Errorf("固定 IP %s 不能是广播地址", ipStr)
				}
			}
		}

		// 管理时间戳: 已存在的 MAC 保留 created_at,变更时更新 updated_at
		if old, ok := existingMap[normalizedMAC]; ok {
			item.CreatedAt = old.CreatedAt
			if old.IP != item.IP || old.Remark != item.Remark || old.Enabled != item.Enabled {
				item.UpdatedAt = now
			} else {
				item.UpdatedAt = old.UpdatedAt
			}
		} else {
			item.CreatedAt = now
			item.UpdatedAt = now
		}

		result = append(result, item)
	}

	return result, "", nil
}

// validateReservationsForStart 启动服务前校验固定映射列表（V1.0.3新增）
// 校验: MAC 重复、IP 重复、跨网段、与网卡/网关冲突、与地址池配置冲突
// adapterIP/subnetMask/gatewayIP 为实际运行时值（已校验）
// V1.0.3修复: 同网段/网卡 IP/网关 IP/网络地址/广播地址等阻断性校验仅对 enabled=true 生效;
//
//	enabled=false 的映射不因跨网段或冲突阻止服务启动;
//	MAC/IP 格式与重复校验仍对所有条目生效,避免配置脏数据
func validateReservationsForStart(leases []StaticLeaseConfig, adapterIP net.IP, subnetMask net.IPMask,
	gatewayIP net.IP) (string, error) {

	if len(leases) == 0 {
		return "", nil
	}

	macSeen := make(map[string]bool)
	ipSeen := make(map[string]bool)

	var maskVal uint32
	var networkVal, broadcastVal uint32
	hasSubnetInfo := adapterIP != nil && len(subnetMask) >= net.IPv4len
	if hasSubnetInfo {
		maskVal = binary.BigEndian.Uint32(subnetMask[:4])
		ipVal := binary.BigEndian.Uint32(adapterIP.To4())
		networkVal = ipVal & maskVal
		broadcastVal = networkVal | ^maskVal
	}

	adapterIP4 := adapterIP.To4()
	gwIP4 := net.IP(nil)
	if gatewayIP != nil {
		gwIP4 = gatewayIP.To4()
	}

	for _, sl := range leases {
		// 校验 MAC 格式（已保存的应为规范格式,防御性检查）
		normalizedMAC, ok := normalizeMAC(sl.MAC)
		if !ok {
			return errCodeResvMACInvalid, fmt.Errorf("MAC 地址格式无效: %s", sl.MAC)
		}
		// 校验 MAC 不重复
		if macSeen[normalizedMAC] {
			return errCodeResvMACDuplicate, fmt.Errorf("MAC 地址重复: %s", normalizedMAC)
		}
		macSeen[normalizedMAC] = true

		// 校验 IP 格式
		if !isValidIPv4(sl.IP) {
			return errCodeResvIPInvalid, fmt.Errorf("IP 地址格式无效: %s", sl.IP)
		}
		fixedIP := net.ParseIP(sl.IP)
		fixedIP4 := fixedIP.To4()

		// 校验 IP 不重复
		ipLower := strings.ToLower(sl.IP)
		if ipSeen[ipLower] {
			return errCodeResvIPDuplicate, fmt.Errorf("IP 地址重复: %s", sl.IP)
		}
		ipSeen[ipLower] = true

		// V1.0.3修复: 阻断性校验（同网段/网卡/网关/网络/广播）仅对 enabled=true 生效
		// enabled=false 的映射不参与运行,不因跨网段或冲突阻止服务启动
		if !sl.Enabled {
			continue
		}
		if !hasSubnetInfo {
			continue
		}

		// 校验同网段
		if !sameSubnetIPv4(adapterIP, fixedIP4, subnetMask) {
			return errCodeResvSubnetMismatch, fmt.Errorf("固定 IP %s 与网卡 %s 不在同一网段", sl.IP, adapterIP.String())
		}
		// 不能等于网卡 IP
		if fixedIP4.Equal(adapterIP4) {
			return errCodeResvConflictAdapter, fmt.Errorf("固定 IP %s 不能等于网卡 IP", sl.IP)
		}
		// 不能等于网关 IP
		if gwIP4 != nil && fixedIP4.Equal(gwIP4) {
			return errCodeResvConflictGateway, fmt.Errorf("固定 IP %s 不能等于网关 IP", sl.IP)
		}
		// 不能是网络地址
		ipVal := binary.BigEndian.Uint32(fixedIP4)
		if ipVal == networkVal {
			return errCodeResvConflictNetwork, fmt.Errorf("固定 IP %s 不能是网络地址", sl.IP)
		}
		// 不能是广播地址
		if ipVal == broadcastVal {
			return errCodeResvConflictBroadcast, fmt.Errorf("固定 IP %s 不能是广播地址", sl.IP)
		}
	}

	return "", nil
}

// buildDHCPStaticLeases 将配置中的启用固定映射转换为 DHCP 服务器使用的格式
// 仅返回 enabled=true 的映射;MAC/IP 无效的条目被跳过
func buildDHCPStaticLeases(leases []StaticLeaseConfig) []dhcp.StaticLease {
	var result []dhcp.StaticLease
	for _, sl := range leases {
		if !sl.Enabled {
			continue
		}
		hw, err := net.ParseMAC(sl.MAC)
		if err != nil {
			continue
		}
		ip := net.ParseIP(sl.IP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		result = append(result, dhcp.StaticLease{
			MAC: hw,
			IP:  ip.To4(),
		})
	}
	return result
}

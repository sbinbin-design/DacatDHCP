package network

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"syscall"
	"unsafe"
)

// AdapterInfo 表示一个网卡的信息
type AdapterInfo struct {
	Name    string `json:"name"`    // 友好名称，如 "Ethernet"
	IP      string `json:"ip"`      // IPv4 地址
	Mask    string `json:"mask"`    // 子网掩码
	MAC     string `json:"mac"`     // MAC 地址
	Status  string `json:"status"`  // "up" 或 "down"
	Type    string `json:"type"`    // "physical" 或 "virtual"
	Index   int    `json:"index"`   // 接口索引
	IsUp    bool   `json:"isUp"`    // 是否已连接
	HasIPv4 bool   `json:"hasIPv4"` // 是否有 IPv4 地址
}

// virtualKeywords 用于识别虚拟网卡的关键词
var virtualKeywords = []string{
	"virtual", "hyper-v", "vmware", "vEthernet",
	"virtualbox", "vbox", "loopback", "vpn",
	"tunnel", "6to4", "isatap", "teredo",
	"docker", "wsl", "hyper-v", "vmnet",
	"hamachi", "wireguard", "tailscale",
	"bluetooth", "pan", "npcap", "winpkfilter",
}

// EnumerateAdapters 枚举本机网卡信息
func EnumerateAdapters() ([]AdapterInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var adapters []AdapterInfo
	for _, iface := range ifaces {
		// 跳过回环接口
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		info := AdapterInfo{
			Name:  iface.Name,
			MAC:   iface.HardwareAddr.String(),
			Index: iface.Index,
		}

		// 格式化 MAC 地址
		if info.MAC == "" {
			// 某些接口没有 MAC 地址
			info.MAC = "N/A"
		}

		// 检测物理/虚拟类型
		info.Type = detectAdapterType(iface.Name)

		// 获取 IP 地址
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue // 跳过 IPv6
			}
			info.IP = ip4.String()
			mask := ipNet.Mask
			if len(mask) == 4 {
				info.Mask = net.IP(mask).String()
			}
			info.HasIPv4 = true
			break // 只取第一个 IPv4 地址
		}

		// V1修复: 统一使用 IsIfUp 判断网卡状态
		info.IsUp = IsIfUp(iface.Index, iface.Flags)

		// 判断连接状态
		if info.IsUp && info.HasIPv4 {
			info.Status = "up"
		} else {
			info.Status = "down"
		}

		adapters = append(adapters, info)
	}

	// 排序：已连接物理网卡优先
	sortAdapters(adapters)

	return adapters, nil
}

// GetIfOperStatus 使用 Windows IP Helper API 获取网卡 OperStatus
// V1修复: 导出为公共方法，网卡列表/启动校验/运行监控共用同一方法
// 兼容 Windows 7 的 IP Helper API (iphlpapi.dll GetIfEntry)
func GetIfOperStatus(ifIndex int) (uint32, bool) {
	iphlpapi := syscall.NewLazyDLL("iphlpapi.dll")
	getIfEntry := iphlpapi.NewProc("GetIfEntry")

	// MIB_IFROW 结构体布局（Windows 7 兼容）：
	//   wszName[256]          offset 0,    size 512 (256 * sizeof(WCHAR))
	//   dwIndex               offset 512,  size 4
	//   dwType                offset 516,  size 4
	//   dwMtu                 offset 520,  size 4
	//   dwSpeed               offset 524,  size 4
	//   dwPhysAddrLen         offset 528,  size 4
	//   bPhysAddr[8]          offset 532,  size 8
	//   dwAdminStatus         offset 540,  size 4
	//   dwOperStatus          offset 544,  size 4
	//   ... 后续字段
	// 总大小约 864 字节
	const rowSize = 864
	const offsetIndex = 512
	const offsetOperStatus = 544

	buf := make([]byte, rowSize)
	// 设置 dwIndex（输入参数，标识要查询的网卡）
	binary.LittleEndian.PutUint32(buf[offsetIndex:], uint32(ifIndex))

	ret, _, _ := getIfEntry.Call(uintptr(unsafe.Pointer(&buf[0])))
	if ret != 0 {
		return 0, false // 调用失败，返回 false 表示降级
	}

	// 读取 dwOperStatus
	operStatus := binary.LittleEndian.Uint32(buf[offsetOperStatus:])
	return operStatus, true
}

// IsIfUp 判断网卡是否已连接
// V1修复: 导出为公共方法，优先使用 Windows IP Helper API 获取 OperStatus，失败时降级使用 net.FlagUp
func IsIfUp(ifIndex int, flags net.Flags) bool {
	// 优先使用 Windows IP Helper API
	if operStatus, ok := GetIfOperStatus(ifIndex); ok {
		// IF_OPER_STATUS_CONNECTED = 4, IF_OPER_STATUS_OPERATIONAL = 5
		return operStatus >= 4
	}
	// 降级：使用 net.FlagUp（无法读取 OperStatus 时不导致崩溃）
	return flags&net.FlagUp != 0
}

// IsAdapterUp 按网卡名称判断是否已连接
// V1修复: 统一网卡状态判断入口，启动校验和运行监控共用
func IsAdapterUp(name string) bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Name == name {
			return IsIfUp(iface.Index, iface.Flags)
		}
	}
	return false // 网卡不存在
}

// GetAdapterIPByName 按网卡名称获取 IPv4 地址和子网掩码
// V1修复: 统一网卡 IP 查询入口，启动校验和运行监控共用
func GetAdapterIPByName(name string) (net.IP, net.IPMask, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for _, iface := range ifaces {
		if iface.Name != name {
			continue
		}
		// V1修复: 统一使用 IsIfUp 判断网卡状态
		if !IsIfUp(iface.Index, iface.Flags) {
			return nil, nil, fmt.Errorf("网卡 %s 未连接或已禁用", name)
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, nil, err
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			return ip4, ipNet.Mask, nil
		}
		return nil, nil, fmt.Errorf("网卡 %s 没有 IPv4 地址", name)
	}
	return nil, nil, fmt.Errorf("未找到网卡 %s", name)
}

// detectAdapterType 根据网卡名称检测是物理还是虚拟网卡
func detectAdapterType(name string) string {
	lower := strings.ToLower(name)
	for _, kw := range virtualKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return "virtual"
		}
	}
	return "physical"
}

// sortAdapters 排序适配器：已连接物理 > 未连接物理 > 已连接虚拟 > 未连接虚拟
func sortAdapters(adapters []AdapterInfo) {
	// 简单冒泡排序，网卡数量不多
	n := len(adapters)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if adapterPriority(adapters[j]) < adapterPriority(adapters[j+1]) {
				adapters[j], adapters[j+1] = adapters[j+1], adapters[j]
			}
		}
	}
}

// adapterPriority 返回适配器排序优先级
func adapterPriority(a AdapterInfo) int {
	score := 0
	if a.Status == "up" {
		score += 2
	}
	if a.Type == "physical" {
		score += 1
	}
	return score
}

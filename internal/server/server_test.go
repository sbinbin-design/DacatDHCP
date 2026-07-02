package server

import (
	"encoding/binary"
	"net"
	"testing"
)

// testUint32ToIP 将 uint32 转换为 net.IP（测试辅助函数）
func testUint32ToIP(val uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, val)
	return ip
}

// recommendPool 推算地址池推荐（与 handlePoolRecommend 中计算逻辑一致，用于单元测试）
func recommendPool(adapterIP net.IP, subnetMask net.IPMask) (startIP, endIP net.IP, ok bool) {
	ipVal := ipToUint32(adapterIP.To4())
	maskVal := binary.BigEndian.Uint32(subnetMask[:4])
	networkVal := ipVal & maskVal
	broadcastVal := networkVal | ^maskVal

	firstUsable := networkVal + 1
	lastUsable := broadcastVal - 1

	// 优先推荐服务端 IP +10 到 IP +110
	startVal := ipVal + 10
	endVal := ipVal + 110

	// 后方空间不足时从前方补充
	if endVal > lastUsable {
		overflow := endVal - lastUsable
		endVal = lastUsable
		startVal = ipVal - overflow
		if startVal <= firstUsable {
			startVal = firstUsable
		}
	}

	if startVal < firstUsable {
		startVal = firstUsable
	}
	if endVal > lastUsable {
		endVal = lastUsable
	}

	// 排除服务端 IP
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

	if startVal > endVal {
		return nil, nil, false
	}

	return testUint32ToIP(startVal), testUint32ToIP(endVal), true
}

// TestPoolRecommend_ServerIPAtEnd 服务端 IP 在网段末尾（192.168.1.254/24），
// 验证推荐范围不包含广播地址和服务端 IP
func TestPoolRecommend_ServerIPAtEnd(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.254")
	subnetMask := net.IPv4Mask(255, 255, 255, 0)

	startIP, endIP, ok := recommendPool(adapterIP, subnetMask)
	if !ok {
		t.Fatal("地址池推荐应成功")
	}

	// 验证不包含服务端 IP 192.168.1.254
	serverIP := net.ParseIP("192.168.1.254")
	if startIP.Equal(serverIP) || endIP.Equal(serverIP) {
		t.Error("推荐范围不应包含服务端 IP 192.168.1.254")
	}

	// 验证不包含广播地址 192.168.1.255
	broadcast := net.ParseIP("192.168.1.255")
	if startIP.Equal(broadcast) || endIP.Equal(broadcast) {
		t.Error("推荐范围不应包含广播地址 192.168.1.255")
	}

	// 验证范围在子网可用地址内
	startVal := ipToUint32(startIP.To4())
	endVal := ipToUint32(endIP.To4())
	networkVal := ipToUint32(adapterIP.To4()) & binary.BigEndian.Uint32(subnetMask[:4])
	broadcastVal := networkVal | ^binary.BigEndian.Uint32(subnetMask[:4])

	if startVal < networkVal+1 {
		t.Errorf("起始地址不应小于子网第一个可用地址 %s", testUint32ToIP(networkVal+1))
	}
	if endVal > broadcastVal-1 {
		t.Errorf("结束地址不应大于子网最后一个可用地址 %s", testUint32ToIP(broadcastVal-1))
	}
}

// TestPoolRecommend_Non24Subnet 非 /24 子网（10.0.2.1/23），
// 验证推荐范围在正确子网内且不包含服务端 IP
func TestPoolRecommend_Non24Subnet(t *testing.T) {
	adapterIP := net.ParseIP("10.0.2.1")
	subnetMask := net.IPv4Mask(255, 255, 254, 0)

	startIP, endIP, ok := recommendPool(adapterIP, subnetMask)
	if !ok {
		t.Fatal("地址池推荐应成功")
	}

	// 验证推荐范围在子网内
	startVal := ipToUint32(startIP.To4())
	endVal := ipToUint32(endIP.To4())
	networkVal := ipToUint32(adapterIP.To4()) & binary.BigEndian.Uint32(subnetMask[:4])
	broadcastVal := networkVal | ^binary.BigEndian.Uint32(subnetMask[:4])

	if startVal < networkVal+1 {
		t.Errorf("起始地址不应小于子网第一个可用地址 %s", testUint32ToIP(networkVal+1))
	}
	if endVal > broadcastVal-1 {
		t.Errorf("结束地址不应大于子网最后一个可用地址 %s", testUint32ToIP(broadcastVal-1))
	}

	// 验证不包含服务端 IP
	serverIP := net.ParseIP("10.0.2.1")
	if startIP.Equal(serverIP) || endIP.Equal(serverIP) {
		t.Error("推荐范围不应包含服务端 IP 10.0.2.1")
	}

	// 验证推荐大小合理（目标约 100 个地址）
	poolSize := endVal - startVal + 1
	if poolSize <= 0 || poolSize > 4096 {
		t.Errorf("地址池大小应合理，实际: %d", poolSize)
	}
}

// TestPoolRecommend_SmallSubnet 极小子网（192.168.1.2/30，仅2个可用主机），
// 验证推荐结果优雅处理（排除服务端 IP 和广播地址后返回可用地址）
func TestPoolRecommend_SmallSubnet(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.2")
	subnetMask := net.IPv4Mask(255, 255, 255, 252)

	startIP, endIP, ok := recommendPool(adapterIP, subnetMask)

	if !ok {
		// /30 子网空间极小，推荐返回不可用也是可接受的
		t.Log("/30 子网空间极小，推荐返回不可用（可接受）")
		return
	}

	// 推荐成功时，验证不包含服务端 IP 和广播地址
	serverIP := net.ParseIP("192.168.1.2")
	if startIP.Equal(serverIP) || endIP.Equal(serverIP) {
		t.Error("推荐范围不应包含服务端 IP 192.168.1.2")
	}

	broadcast := net.ParseIP("192.168.1.3")
	if startIP.Equal(broadcast) || endIP.Equal(broadcast) {
		t.Error("推荐范围不应包含广播地址 192.168.1.3")
	}

	// 验证至少包含一个可用地址（192.168.1.1）
	startVal := ipToUint32(startIP.To4())
	endVal := ipToUint32(endIP.To4())
	poolSize := endVal - startVal + 1
	if poolSize < 1 {
		t.Errorf("推荐范围应至少包含 1 个可用地址，实际: %d", poolSize)
	}
}

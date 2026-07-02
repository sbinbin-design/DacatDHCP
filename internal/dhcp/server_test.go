package dhcp

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
)

// buildTestDHCPPacket 构建测试用 DHCP 原始报文（用于 handlePacket 等需要 []byte 输入的测试）
func buildTestDHCPPacket(t *testing.T, flags uint16, ciaddr, giaddr net.IP, msgType byte) []byte {
	t.Helper()

	buf := make([]byte, 576)
	for i := range buf {
		buf[i] = 0
	}

	// 固定头部
	buf[0] = OpRequest
	buf[1] = HwTypeEthernet
	buf[2] = 6 // MAC 地址长度
	buf[3] = 0 // Hops
	binary.BigEndian.PutUint32(buf[4:8], 0x12345678) // XID
	binary.BigEndian.PutUint16(buf[10:12], flags)

	// CIAddr（bytes 12-16）
	if ciaddr != nil && len(ciaddr.To4()) == 4 {
		copy(buf[12:16], ciaddr.To4())
	}

	// GIAddr（bytes 24-28）
	if giaddr != nil && len(giaddr.To4()) == 4 {
		copy(buf[24:28], giaddr.To4())
	}

	// CHAddr (MAC: 00:11:22:33:44:55)
	mac := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	copy(buf[28:28+len(mac)], mac)

	// Magic Cookie
	binary.BigEndian.PutUint32(buf[236:240], MagicCookie)

	// Options: DHCP Message Type
	offset := 240
	buf[offset] = OptMsgType
	offset++
	buf[offset] = 1 // length
	offset++
	buf[offset] = msgType
	offset++

	// End
	buf[offset] = OptEnd

	return buf[:offset+1]
}

// TestResponseTarget_UnicastForRenewal CIAddr 非零且无广播标志 → 单播到 CIAddr:68
func TestResponseTarget_UnicastForRenewal(t *testing.T) {
	s := &Server{}
	ciaddr := net.ParseIP("192.168.1.100").To4()

	pkt := &Packet{
		CIAddr: ciaddr,
		Flags:  0x0000, // 无广播标志
	}

	target := s.responseTarget(pkt)

	if !target.IP.Equal(ciaddr) {
		t.Errorf("期望单播到 %s，实际: %s", ciaddr, target.IP)
	}
	if target.Port != 68 {
		t.Errorf("期望端口 68，实际: %d", target.Port)
	}
}

// TestResponseTarget_BroadcastForFirstAllocation CIAddr 为零且广播标志 → 广播到 255.255.255.255:68
func TestResponseTarget_BroadcastForFirstAllocation(t *testing.T) {
	s := &Server{}

	pkt := &Packet{
		CIAddr: net.IPv4(0, 0, 0, 0),
		Flags:  0x8000, // 广播标志
	}

	target := s.responseTarget(pkt)

	expectedBroadcast := net.IPv4(255, 255, 255, 255)
	if !target.IP.Equal(expectedBroadcast) {
		t.Errorf("期望广播到 %s，实际: %s", expectedBroadcast, target.IP)
	}
	if target.Port != 68 {
		t.Errorf("期望端口 68，实际: %d", target.Port)
	}
}

// TestResponseTarget_BroadcastWhenFlagSet CIAddr 非零但广播标志设置 → 广播优先（广播标志优先于 CIAddr）
func TestResponseTarget_BroadcastWhenFlagSet(t *testing.T) {
	s := &Server{}
	ciaddr := net.ParseIP("192.168.1.100").To4()

	pkt := &Packet{
		CIAddr: ciaddr,
		Flags:  0x8000, // 广播标志
	}

	target := s.responseTarget(pkt)

	expectedBroadcast := net.IPv4(255, 255, 255, 255)
	if !target.IP.Equal(expectedBroadcast) {
		t.Errorf("广播标志优先，期望广播到 %s，实际: %s", expectedBroadcast, target.IP)
	}
	if target.Port != 68 {
		t.Errorf("期望端口 68，实际: %d", target.Port)
	}
}

// TestGIAddrNonZero_Ignored GIAddr 非零时 handlePacket 应忽略报文（V1 不支持 DHCP Relay）
func TestGIAddrNonZero_Ignored(t *testing.T) {
	var logs []string
	s := &Server{
		logFunc: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}

	// 构造 GIAddr 非零的 DHCP Discover 报文
	giaddr := net.ParseIP("10.0.0.1")
	data := buildTestDHCPPacket(t, 0x0000, nil, giaddr, MsgTypeDiscover)

	// handlePacket 应因 giaddr 非零而提前返回，不会因 leases 为 nil 而 panic
	s.handlePacket(data)

	// 验证日志中包含 Relay 忽略信息
	found := false
	for _, log := range logs {
		if strings.Contains(log, "Relay") {
			found = true
			break
		}
	}
	if !found {
		t.Error("giaddr 非零时应记录 DHCP Relay 忽略日志")
	}
}

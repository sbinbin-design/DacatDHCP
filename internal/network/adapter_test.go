package network

import (
	"net"
	"testing"
)

// TestIsIfUp_FallbackToNetFlagUp OperStatus API 失败时降级使用 net.FlagUp
func TestIsIfUp_FallbackToNetFlagUp(t *testing.T) {
	// 使用一个不存在的 ifIndex，IP Helper API 应该失败
	// 降级到 net.FlagUp 判断
	fakeIndex := 99999
	flags := net.FlagUp | net.FlagBroadcast
	result := IsIfUp(fakeIndex, flags)
	if !result {
		t.Error("OperStatus 失败时应降级使用 net.FlagUp，FlagUp 已设置应返回 true")
	}

	// FlagUp 未设置
	flagsDown := net.FlagBroadcast
	result = IsIfUp(fakeIndex, flagsDown)
	if result {
		t.Error("OperStatus 失败且 FlagUp 未设置时应返回 false")
	}
}

// TestGetIfOperStatus_InvalidIndex 无效接口索引应返回 false
func TestGetIfOperStatus_InvalidIndex(t *testing.T) {
	_, ok := GetIfOperStatus(99999)
	if ok {
		t.Error("无效接口索引应返回 false")
	}
}

// TestIsAdapterUp_NonExistent 不存在的网卡应返回 false
func TestIsAdapterUp_NonExistent(t *testing.T) {
	result := IsAdapterUp("NonExistentAdapter12345")
	if result {
		t.Error("不存在的网卡应返回 false")
	}
}

// TestGetAdapterIPByName_NonExistent 不存在的网卡应返回错误
func TestGetAdapterIPByName_NonExistent(t *testing.T) {
	_, _, err := GetAdapterIPByName("NonExistentAdapter12345")
	if err == nil {
		t.Error("不存在的网卡应返回错误")
	}
}

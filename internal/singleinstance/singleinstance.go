// Package singleinstance 提供 Windows 命名单实例检测
// 使用 Global\ 前缀的命名互斥体兼容管理员进程场景
// V3: 删除 WM_COPYDATA IPC 逻辑，只保留互斥体检测
// V4: Acquire 返回 error 明确区分"已有实例"与"CreateMutex 失败"
package singleinstance

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	// mutexName 命名互斥体名称（Global\ 前缀确保跨会话可见，管理员进程可检测）
	mutexName = "Global\\DacatDHCP.SingleInstance" // V5: 固定名称，删除版本号
)

// kernel32 API
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
)

// Instance 单实例锁
type Instance struct {
	mutex syscall.Handle
}

// Acquire 尝试获取单实例锁
// 返回值：
//   - (instance, true, nil): 这是第一个实例，成功获取锁
//   - (nil, false, nil): 已有实例在运行（调用方应打开已有控制台后退出）
//   - (nil, false, error): 互斥体创建失败（调用方应写日志并显示 MessageBox，不得按已有实例静默退出）
//
// V4: 明确区分"已有实例"与"CreateMutex 失败"
func Acquire() (*Instance, bool, error) {
	mutexNamePtr, _ := syscall.UTF16PtrFromString(mutexName)
	mutex, _, lastErr := procCreateMutexW.Call(
		0, // lpMutexAttributes = nil
		0, // bInitialOwner = false
		uintptr(unsafe.Pointer(mutexNamePtr)),
	)

	if mutex == 0 {
		// V4: CreateMutex 调用本身失败，不是已有实例
		return nil, false, fmt.Errorf("创建单实例互斥体失败: %v", lastErr)
	}

	// 检查互斥体是否已存在
	if lastErr != nil && lastErr == syscall.Errno(183) { // ERROR_ALREADY_EXISTS
		syscall.CloseHandle(syscall.Handle(mutex))
		return nil, false, nil // 已有实例，不是错误
	}

	return &Instance{mutex: syscall.Handle(mutex)}, true, nil
}

// Release 释放单实例锁
func (i *Instance) Release() {
	if i.mutex != 0 {
		syscall.CloseHandle(i.mutex)
		i.mutex = 0
	}
}

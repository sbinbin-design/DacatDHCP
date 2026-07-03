// Package systray 提供 Windows 系统托盘图标功能
// 使用纯 Win32 API (syscall) 实现，不引入大型桌面框架，发布后保持单 EXE
//
// V7: 托盘最终修复
//   - GetCurrentThreadId 从 kernel32.dll 加载，Find 确认存在
//   - RequestExit 只发送消息，不得从业务 goroutine 修改 iconData/iconAdded/销毁窗口
//   - FinalizeAfterRun 兜底清理（Run 返回后由同 OS 线程执行）
//   - RegisterClassExW: atom==0 时仅 ERROR_CLASS_ALREADY_EXISTS 允许
//   - CreateWindowExW: 仅以 hwnd==0 判断失败
//   - WM_ENDSESSION 提取为 HandleEndSession 可测试生产函数
//   - Win32API 接口增加 GetCurrentThreadId
package systray

import (
	"DacatDHCP/internal/version"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// ============================================================
// Win32 常量
// ============================================================

const (
	WM_TRAYICON          = 0x8001
	WM_APP_UPDATE_STATUS = 0x8002
	WM_APP_EXIT          = 0x8003

	NIM_ADD        = 0x00000000
	NIM_MODIFY     = 0x00000001
	NIM_DELETE     = 0x00000002
	NIM_SETVERSION = 0x00000004

	NIF_MESSAGE  = 0x00000001
	NIF_ICON     = 0x00000002
	NIF_TIP      = 0x00000004
	NIF_SHOWTIP  = 0x00000080
	notifyUFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP | NIF_SHOWTIP

	NOTIFYICON_VERSION_4 = 0x00000004

	MSGFLT_ALLOW = 1

	WM_LBUTTONDBLCLK   = 0x0203
	WM_LBUTTONUP       = 0x0202
	WM_RBUTTONUP       = 0x0205
	WM_CONTEXTMENU     = 0x007B
	WM_COMMAND         = 0x0111
	WM_QUERYENDSESSION = 0x0011
	WM_ENDSESSION      = 0x0016
	WM_NULL            = 0x0000
	WM_QUIT            = 0x0012

	MF_STRING    = 0x00000000
	MF_SEPARATOR = 0x00000800
	MF_GRAYED    = 0x00000001

	TPM_RIGHTBUTTON = 0x0002
	TPM_BOTTOMALIGN = 0x0008

	CS_VREDRAW = 0x0001
	CS_HREDRAW = 0x0002

	trayIconUID = 1

	ERROR_CLASS_ALREADY_EXISTS = 1410
)

// trayTitle 返回托盘标题（产品名 + 版本号，版本读取 internal/version 唯一源）
func trayTitle() string {
	return version.ProductName() + " V" + version.Version()
}

// ============================================================
// Win32 结构体
// ============================================================

type POINT struct {
	X int32
	Y int32
}

type MSG struct {
	HWnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type NOTIFYICONDATAW struct {
	CbSize           uint32
	HWnd             syscall.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            syscall.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     syscall.Handle
}

// ============================================================
// Win32 API 函数指针
// V7: GetCurrentThreadId 和 GetModuleHandleW 均从 kernel32.dll 加载
// ============================================================

var (
	user32                          = syscall.NewLazyDLL("user32.dll")
	procRegisterClassExW            = user32.NewProc("RegisterClassExW")
	procCreateWindowExW             = user32.NewProc("CreateWindowExW")
	procDefWindowProcW              = user32.NewProc("DefWindowProcW")
	procGetMessageW                 = user32.NewProc("GetMessageW")
	procTranslateMessage            = user32.NewProc("TranslateMessage")
	procDispatchMessageW            = user32.NewProc("DispatchMessageW")
	procPostQuitMessage             = user32.NewProc("PostQuitMessage")
	procDestroyWindow               = user32.NewProc("DestroyWindow")
	procCreatePopupMenu             = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                 = user32.NewProc("AppendMenuW")
	procDestroyMenu                 = user32.NewProc("DestroyMenu")
	procTrackPopupMenu              = user32.NewProc("TrackPopupMenu")
	procSetForegroundWindow         = user32.NewProc("SetForegroundWindow")
	procGetCursorPos                = user32.NewProc("GetCursorPos")
	procRegisterWindowMessageW      = user32.NewProc("RegisterWindowMessageW")
	procDestroyIcon                 = user32.NewProc("DestroyIcon")
	procPostMessageW                = user32.NewProc("PostMessageW")
	procPostThreadMessageW          = user32.NewProc("PostThreadMessageW")
	procChangeWindowMessageFilterEx = user32.NewProc("ChangeWindowMessageFilterEx")

	shell32              = syscall.NewLazyDLL("shell32.dll")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procExtractIconExW   = shell32.NewProc("ExtractIconExW")

	// V7: GetCurrentThreadId 和 GetModuleHandleW 从 kernel32.dll 加载
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
)

// ============================================================
// 菜单项 ID
// ============================================================

const (
	menuStatus      = 1001
	menuOpenConsole = 1002
	menuExit        = 1003
)

// ============================================================
// parseTrayCallback - VERSION_4 消息解析纯函数
// ============================================================

func parseTrayCallback(lParam uintptr) (event uint32, iconID uint32) {
	event = uint32(uintptr(lParam) & 0xFFFF)
	iconID = uint32((uintptr(lParam) >> 16) & 0xFFFF)
	return
}

// ============================================================
// HandleEndSession - V7: 可测试的 WM_ENDSESSION 处理函数
// wParam!=0 触发 OnForceExit；wParam==0 会话取消
// 返回值：0（MSDN 要求 WM_ENDSESSION 返回 0）
// ============================================================

func HandleEndSession(callbacks Callbacks, wParam uintptr) uintptr {
	if wParam != 0 && callbacks != nil {
		go callbacks.OnForceExit()
	}
	return 0
}

// ============================================================
// Win32API 可替换接口
// ============================================================

type Win32API struct {
	PostMessage         func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool
	PostThreadMessage   func(threadID uint32, msg uint32, wParam, lParam uintptr) bool
	NotifyIcon          func(dwMessage uint32, data *NOTIFYICONDATAW) bool
	DestroyWindow       func(hwnd syscall.Handle) bool
	DestroyIcon         func(hIcon syscall.Handle) bool
	PostQuitMessage     func(exitCode int)
	GetCurrentThreadId  func() uint32 // V7: 从 kernel32 加载
	GetModuleHandle     func() uintptr
	RegisterClassEx     func(wc *WNDCLASSEXW) (atom uintptr, lastErr error)
	CreateWindowEx      func(className *uint16, hInstance uintptr) (hwnd uintptr, lastErr error)
	ChangeWindowMessage func(hwnd syscall.Handle, msg uint32) bool
	ExtractIconEx       func(exePath *uint16) (hIconSmall uintptr, ok bool)
}

func realWin32API() *Win32API {
	return &Win32API{
		PostMessage: func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
			ret, _, _ := procPostMessageW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
			return ret != 0
		},
		PostThreadMessage: func(threadID uint32, msg uint32, wParam, lParam uintptr) bool {
			ret, _, _ := procPostThreadMessageW.Call(uintptr(threadID), uintptr(msg), wParam, lParam)
			return ret != 0
		},
		NotifyIcon: func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
			ret, _, _ := procShellNotifyIconW.Call(uintptr(dwMessage), uintptr(unsafe.Pointer(data)))
			return ret != 0
		},
		DestroyWindow: func(hwnd syscall.Handle) bool {
			ret, _, _ := procDestroyWindow.Call(uintptr(hwnd))
			return ret != 0
		},
		DestroyIcon: func(hIcon syscall.Handle) bool {
			ret, _, _ := procDestroyIcon.Call(uintptr(hIcon))
			return ret != 0
		},
		PostQuitMessage: func(exitCode int) {
			procPostQuitMessage.Call(uintptr(exitCode))
		},
		// V7: GetCurrentThreadId 从 kernel32 加载
		GetCurrentThreadId: func() uint32 {
			tid, _, _ := procGetCurrentThreadId.Call()
			return uint32(tid)
		},
		GetModuleHandle: func() uintptr {
			h, _, _ := procGetModuleHandleW.Call(0)
			return h
		},
		RegisterClassEx: func(wc *WNDCLASSEXW) (uintptr, error) {
			atom, _, lastErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wc)))
			return atom, lastErr
		},
		CreateWindowEx: func(className *uint16, hInstance uintptr) (uintptr, error) {
			hwnd, _, lastErr := procCreateWindowExW.Call(
				0,
				uintptr(unsafe.Pointer(className)),
				0, 0,
				0, 0, 0, 0,
				0, 0, hInstance, 0,
			)
			return hwnd, lastErr
		},
		ChangeWindowMessage: func(hwnd syscall.Handle, msg uint32) bool {
			ret, _, _ := procChangeWindowMessageFilterEx.Call(
				uintptr(hwnd), uintptr(msg), uintptr(MSGFLT_ALLOW), 0,
			)
			return ret != 0
		},
		ExtractIconEx: func(exePath *uint16) (uintptr, bool) {
			var hIconSmall, hIconLarge uintptr
			ret, _, _ := procExtractIconExW.Call(
				uintptr(unsafe.Pointer(exePath)),
				0,
				uintptr(unsafe.Pointer(&hIconLarge)),
				uintptr(unsafe.Pointer(&hIconSmall)),
				1,
			)
			if hIconLarge != 0 {
				procDestroyIcon.Call(hIconLarge)
			}
			return hIconSmall, ret != 0 && hIconSmall != 0
		},
	}
}

// ============================================================
// 回调接口
// ============================================================

type Callbacks interface {
	OnOpenConsole()
	OnExit()
	OnForceExit()
	GetStatusText() string
}

// ============================================================
// Tray 实例
// ============================================================

type Tray struct {
	hwnd       atomic.Uintptr
	hIcon      syscall.Handle
	iconData   NOTIFYICONDATAW // 仅消息线程访问
	callbacks  Callbacks
	wndClass   string
	taskbarMsg uint32
	closing    atomic.Bool
	iconAdded  bool // 仅消息线程访问
	logFunc    func(string, ...interface{})
	win32      *Win32API
	threadID   uint32 // V7: 消息线程 ID
}

var (
	trayInstance *Tray
	trayMu       sync.Mutex
)

// NewTray 创建隐藏窗口并设置消息过滤
// V7: 使用 win32 接口加载 kernel32 函数；Find 确认 proc 存在
func NewTray(callbacks Callbacks, logFunc func(string, ...interface{})) (*Tray, error) {
	win32 := realWin32API()

	// V7: Find 确认 GetCurrentThreadId 存在
	if err := procGetCurrentThreadId.Find(); err != nil {
		return nil, fmt.Errorf("kernel32.dll: GetCurrentThreadId 不可用: %w", err)
	}
	if err := procGetModuleHandleW.Find(); err != nil {
		return nil, fmt.Errorf("kernel32.dll: GetModuleHandleW 不可用: %w", err)
	}

	threadID := win32.GetCurrentThreadId()

	// 加载图标
	hIcon, err := loadAppIconWithAPI(win32)
	if err != nil {
		return nil, fmt.Errorf("加载图标失败: %w", err)
	}

	t := &Tray{
		hIcon:     hIcon,
		callbacks: callbacks,
		wndClass:  "DacatDHCP_Tray_Window",
		logFunc:   logFunc,
		win32:     win32,
		threadID:  threadID,
	}

	// 注册 TaskbarCreated 消息
	taskbarStr, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	ret, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(taskbarStr)))
	t.taskbarMsg = uint32(ret)

	hInstance := win32.GetModuleHandle()

	// 注册窗口类
	wndClassNamePtr, _ := syscall.UTF16PtrFromString(t.wndClass)
	wc := WNDCLASSEXW{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEXW{})),
		Style:         CS_VREDRAW | CS_HREDRAW,
		LpfnWndProc:   syscall.NewCallback(trayWndProc),
		HInstance:     syscall.Handle(hInstance),
		LpszClassName: wndClassNamePtr,
	}
	// V8: 先判断 atom 是否为 0；atom 非0表示成功
	atom, regErr := win32.RegisterClassEx(&wc)
	if atom == 0 {
		// atom 为 0 时，仅 ERROR_CLASS_ALREADY_EXISTS 允许继续
		if !errors.Is(regErr, syscall.Errno(ERROR_CLASS_ALREADY_EXISTS)) {
			// V8: nil 或 Errno(0) 时生成明确错误
			if regErr == nil || regErr == syscall.Errno(0) {
				regErr = errors.New("RegisterClassExW 返回 0 且未提供错误码")
			}
			return nil, fmt.Errorf("注册托盘窗口类失败: %w", regErr)
		}
	}

	// 创建不显示的普通顶层窗口
	hwnd, createErr := win32.CreateWindowEx(wndClassNamePtr, hInstance)
	// V8: 仅以 hwnd==0 判断失败；为 0 且 lastErr 为空时生成明确错误
	if hwnd == 0 {
		if createErr == nil || createErr == syscall.Errno(0) {
			createErr = errors.New("CreateWindowExW 返回 0 且未提供错误码")
		}
		return nil, fmt.Errorf("创建托盘窗口失败: %w", createErr)
	}
	t.hwnd.Store(hwnd)

	// V7: ChangeWindowMessageFilterEx
	hwndHandle := syscall.Handle(hwnd)
	if !win32.ChangeWindowMessage(hwndHandle, WM_TRAYICON) && logFunc != nil {
		logFunc("ChangeWindowMessageFilterEx(msg=WM_TRAYICON) 失败（不影响核心功能）")
	}
	if t.taskbarMsg != 0 {
		if !win32.ChangeWindowMessage(hwndHandle, t.taskbarMsg) && logFunc != nil {
			logFunc("ChangeWindowMessageFilterEx(msg=TaskbarCreated) 失败（不影响核心功能）")
		}
	}

	trayMu.Lock()
	trayInstance = t
	trayMu.Unlock()

	return t, nil
}

// loadAppIconWithAPI 使用 Win32API 接口加载图标
func loadAppIconWithAPI(win32 *Win32API) (syscall.Handle, error) {
	exePath, err := os.Executable()
	if err != nil {
		return 0, err
	}
	exePathPtr, _ := syscall.UTF16PtrFromString(exePath)
	hIconSmall, ok := win32.ExtractIconEx(exePathPtr)
	if !ok {
		return 0, errors.New("ExtractIconExW 未找到图标资源")
	}
	return syscall.Handle(hIconSmall), nil
}

// AddIcon 添加托盘图标并设置 NOTIFYICON_VERSION_4
func (t *Tray) AddIcon() error {
	hwnd := syscall.Handle(t.hwnd.Load())
	t.iconData = NOTIFYICONDATAW{
		CbSize:           uint32(unsafe.Sizeof(NOTIFYICONDATAW{})),
		HWnd:             hwnd,
		UID:              trayIconUID,
		UFlags:           notifyUFlags,
		UCallbackMessage: WM_TRAYICON,
		HIcon:            t.hIcon,
	}
	t.setTooltipText(trayTitle())

	if !t.win32.NotifyIcon(NIM_ADD, &t.iconData) {
		return errors.New("Shell_NotifyIconW(NIM_ADD) 失败")
	}

	t.iconData.UVersion = NOTIFYICON_VERSION_4
	if !t.win32.NotifyIcon(NIM_SETVERSION, &t.iconData) {
		t.win32.NotifyIcon(NIM_DELETE, &t.iconData)
		return errors.New("Shell_NotifyIconW(NIM_SETVERSION) 失败")
	}

	t.iconAdded = true
	t.updateTrayTooltip()
	return nil
}

// Run 运行 Windows 消息循环（阻塞，直到 PostQuitMessage）
func (t *Tray) Run() {
	var msg MSG
	for {
		ret, _, _ := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)
		if ret == 0 || ret == uintptr(0xFFFFFFFF) {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// FinalizeAfterRun V8: Run 返回后由同一 OS 线程执行兜底清理
// 即使 WM_APP_EXIT 正常执行了清理，此方法也必须幂等
// NIM_DELETE、DestroyWindow、DestroyIcon 失败时记录日志，不阻止后续清理
func (t *Tray) FinalizeAfterRun() {
	// 删除残余图标
	if t.iconAdded {
		t.iconData.UFlags = 0
		if !t.win32.NotifyIcon(NIM_DELETE, &t.iconData) && t.logFunc != nil {
			t.logFunc("FinalizeAfterRun: NIM_DELETE 失败")
		}
		t.iconAdded = false
	}

	// 销毁窗口
	hwnd := t.hwnd.Load()
	if hwnd != 0 {
		if !t.win32.DestroyWindow(syscall.Handle(hwnd)) && t.logFunc != nil {
			t.logFunc("FinalizeAfterRun: DestroyWindow 失败")
		}
		t.hwnd.Store(0)
	}

	// 释放图标
	if t.hIcon != 0 {
		if !t.win32.DestroyIcon(t.hIcon) && t.logFunc != nil {
			t.logFunc("FinalizeAfterRun: DestroyIcon 失败")
		}
		t.hIcon = 0
	}

	// 清空全局引用
	trayMu.Lock()
	trayInstance = nil
	trayMu.Unlock()
}

// UpdateStatus 更新托盘状态
// V7: closing=true 或 hwnd=0 时忽略；PostMessage 失败记日志
func (t *Tray) UpdateStatus() {
	if t.closing.Load() {
		return
	}
	hwnd := syscall.Handle(t.hwnd.Load())
	if hwnd == 0 {
		return
	}
	if !t.win32.PostMessage(hwnd, WM_APP_UPDATE_STATUS, 0, 0) {
		if t.logFunc != nil {
			t.logFunc("PostMessage(WM_APP_UPDATE_STATUS) 失败")
		}
	}
}

// RequestExit 请求托盘退出
// V8: 只发送退出消息，不得从业务 goroutine 修改 iconData/iconAdded/销毁窗口
// CAS 保证只执行一次；PostMessage 成功返回 nil；
// PostMessage 失败后尝试 PostThreadMessageW，成功时记警告并返回 nil；
// 只有两者均失败时才返回 error
func (t *Tray) RequestExit() error {
	if !t.closing.CompareAndSwap(false, true) {
		return nil
	}

	hwnd := syscall.Handle(t.hwnd.Load())
	if hwnd == 0 {
		return nil
	}

	// V8: 只发送消息，不修改 iconData/iconAdded，不销毁窗口
	if t.win32.PostMessage(hwnd, WM_APP_EXIT, 0, 0) {
		return nil
	}

	// PostMessage 失败，兜底 PostThreadMessageW
	if t.logFunc != nil {
		t.logFunc("PostMessage(WM_APP_EXIT) 失败，尝试 PostThreadMessageW 兜底")
	}
	if t.threadID != 0 && t.win32.PostThreadMessage(t.threadID, WM_QUIT, 0, 0) {
		// V8: PostThreadMessage 成功，记警告并返回 nil（不作为最终错误）
		if t.logFunc != nil {
			t.logFunc("已使用线程消息兜底退出（PostMessage 失败，PostThreadMessage 成功）")
		}
		return nil
	}

	// V8: 两个方式都失败，返回 error
	return fmt.Errorf("PostMessage 和 PostThreadMessage 均失败，托盘可能残留")
}

// Destroy 直接销毁窗口和图标资源（仅用于启动失败时，消息循环尚未启动）
func (t *Tray) Destroy() {
	hwnd := t.hwnd.Load()
	if hwnd == 0 {
		return
	}
	if t.iconAdded {
		t.iconData.UFlags = 0
		t.win32.NotifyIcon(NIM_DELETE, &t.iconData)
		t.iconAdded = false
	}
	trayMu.Lock()
	trayInstance = nil
	trayMu.Unlock()
	if t.hIcon != 0 {
		t.win32.DestroyIcon(t.hIcon)
		t.hIcon = 0
	}
	t.win32.DestroyWindow(syscall.Handle(hwnd))
	t.hwnd.Store(0)
}

// ============================================================
// 内部方法（仅在消息线程中调用）
// ============================================================

func (t *Tray) setTooltipText(text string) {
	for i := range t.iconData.SzTip {
		t.iconData.SzTip[i] = 0
	}
	copy(t.iconData.SzTip[:], syscall.StringToUTF16(text))
}

func (t *Tray) removeTrayIcon() {
	if !t.iconAdded {
		return
	}
	t.iconData.UFlags = 0
	if !t.win32.NotifyIcon(NIM_DELETE, &t.iconData) && t.logFunc != nil {
		t.logFunc("NIM_DELETE 失败")
	}
	t.iconAdded = false
}

func (t *Tray) updateTrayTooltip() {
	if !t.iconAdded {
		return
	}

	statusText := "DHCP已停止"
	if t.closing.Load() {
		statusText = "正在退出"
	} else if t.callbacks != nil {
		cbText := t.callbacks.GetStatusText()
		if cbText == "DHCP: 运行中" {
			statusText = "DHCP运行中"
		}
	}
	// 版本号读取 internal/version 唯一源，禁止硬编码
	tooltip := trayTitle() + " - " + statusText

	t.setTooltipText(tooltip)
	t.iconData.UFlags = notifyUFlags
	t.iconData.HIcon = t.hIcon
	if !t.win32.NotifyIcon(NIM_MODIFY, &t.iconData) && t.logFunc != nil {
		t.logFunc("NIM_MODIFY 失败")
	}
}

func (t *Tray) showContextMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	statusText := "DHCP: 已停止"
	if t.callbacks != nil {
		statusText = t.callbacks.GetStatusText()
	}
	statusTextPtr, _ := syscall.UTF16PtrFromString(statusText)
	procAppendMenuW.Call(menu, MF_STRING|MF_GRAYED, menuStatus, uintptr(unsafe.Pointer(statusTextPtr)))
	procAppendMenuW.Call(menu, MF_SEPARATOR, 0, 0)
	openTextPtr, _ := syscall.UTF16PtrFromString("打开控制台")
	procAppendMenuW.Call(menu, MF_STRING, menuOpenConsole, uintptr(unsafe.Pointer(openTextPtr)))
	procAppendMenuW.Call(menu, MF_SEPARATOR, 0, 0)
	exitTextPtr, _ := syscall.UTF16PtrFromString("退出DacatDHCP")
	procAppendMenuW.Call(menu, MF_STRING, menuExit, uintptr(unsafe.Pointer(exitTextPtr)))

	hwnd := syscall.Handle(t.hwnd.Load())
	procSetForegroundWindow.Call(uintptr(hwnd))
	var pt POINT
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procTrackPopupMenu.Call(menu, TPM_RIGHTBUTTON|TPM_BOTTOMALIGN, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
	procPostMessageW.Call(uintptr(hwnd), WM_NULL, 0, 0)
}

func (t *Tray) addTrayIconAfterTaskbarRestart() {
	if t.closing.Load() {
		return
	}
	t.iconAdded = false

	hwnd := syscall.Handle(t.hwnd.Load())
	t.iconData.HWnd = hwnd
	t.iconData.UFlags = notifyUFlags
	t.iconData.HIcon = t.hIcon
	t.setTooltipText(trayTitle())

	if !t.win32.NotifyIcon(NIM_ADD, &t.iconData) {
		if t.logFunc != nil {
			t.logFunc("TaskbarCreated: NIM_ADD 失败")
		}
		return
	}

	t.iconData.UVersion = NOTIFYICON_VERSION_4
	if !t.win32.NotifyIcon(NIM_SETVERSION, &t.iconData) {
		t.win32.NotifyIcon(NIM_DELETE, &t.iconData)
		if t.logFunc != nil {
			t.logFunc("TaskbarCreated: NIM_SETVERSION 失败，已回滚删除图标")
		}
		return
	}

	t.iconAdded = true
	t.updateTrayTooltip()
}

func loadAppIcon() (syscall.Handle, error) {
	return loadAppIconWithAPI(realWin32API())
}

// ============================================================
// 窗口过程
// ============================================================

func trayWndProc(hwnd syscall.Handle, msg uint32, wParam uintptr, lParam uintptr) uintptr {
	trayMu.Lock()
	t := trayInstance
	trayMu.Unlock()

	if t == nil {
		ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
		return ret
	}

	switch msg {
	case WM_TRAYICON:
		event, iconID := parseTrayCallback(lParam)
		if iconID != trayIconUID {
			return 0
		}
		switch event {
		case WM_LBUTTONDBLCLK:
			if t.callbacks != nil {
				t.callbacks.OnOpenConsole()
			}
		case WM_CONTEXTMENU:
			t.showContextMenu()
		case WM_RBUTTONUP:
			t.showContextMenu()
		}

	case WM_APP_UPDATE_STATUS:
		t.updateTrayTooltip()

	case WM_APP_EXIT:
		t.closing.Store(true)
		t.removeTrayIcon()
		if t.hIcon != 0 {
			t.win32.DestroyIcon(t.hIcon)
			t.hIcon = 0
		}
		hwndVal := t.hwnd.Load()
		if hwndVal != 0 {
			t.win32.DestroyWindow(syscall.Handle(hwndVal))
			t.hwnd.Store(0)
		}
		trayMu.Lock()
		trayInstance = nil
		trayMu.Unlock()
		t.win32.PostQuitMessage(0)

	case WM_COMMAND:
		switch uint32(wParam) {
		case menuOpenConsole:
			if t.callbacks != nil {
				t.callbacks.OnOpenConsole()
			}
		case menuExit:
			if t.callbacks != nil {
				t.callbacks.OnExit()
			}
		}

	case WM_QUERYENDSESSION:
		return 1

	case WM_ENDSESSION:
		// V7: 使用可测试的 HandleEndSession 生产函数
		return HandleEndSession(t.callbacks, wParam)

	default:
		if t.taskbarMsg != 0 && msg == t.taskbarMsg {
			if !t.closing.Load() {
				t.addTrayIconAfterTaskbarRestart()
			}
			return 0
		}
	}

	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

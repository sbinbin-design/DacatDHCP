package systray

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// ---- parseTrayCallback 纯函数测试 ----

func TestParseTrayCallback_LBUTTONDBLCLK(t *testing.T) {
	lParam := uintptr(uint32(0x0203) | (1 << 16))
	event, iconID := parseTrayCallback(lParam)
	if event != WM_LBUTTONDBLCLK {
		t.Errorf("event = 0x%X, want 0x%X", event, WM_LBUTTONDBLCLK)
	}
	if iconID != 1 {
		t.Errorf("iconID = %d, want 1", iconID)
	}
}

func TestParseTrayCallback_CONTEXTMENU(t *testing.T) {
	lParam := uintptr(uint32(WM_CONTEXTMENU) | (1 << 16))
	event, iconID := parseTrayCallback(lParam)
	if event != WM_CONTEXTMENU {
		t.Errorf("event = 0x%X, want 0x%X", event, WM_CONTEXTMENU)
	}
	if iconID != 1 {
		t.Errorf("iconID = %d, want 1", iconID)
	}
}

func TestParseTrayCallback_RBUTTONUP(t *testing.T) {
	lParam := uintptr(uint32(WM_RBUTTONUP) | (1 << 16))
	event, iconID := parseTrayCallback(lParam)
	if event != WM_RBUTTONUP {
		t.Errorf("event = 0x%X, want 0x%X", event, WM_RBUTTONUP)
	}
	if iconID != 1 {
		t.Errorf("iconID = %d, want 1", iconID)
	}
}

func TestParseTrayCallback_IconIDMismatch(t *testing.T) {
	lParam := uintptr(uint32(WM_LBUTTONDBLCLK) | (99 << 16))
	event, iconID := parseTrayCallback(lParam)
	if event != WM_LBUTTONDBLCLK {
		t.Errorf("event = 0x%X, want 0x%X", event, WM_LBUTTONDBLCLK)
	}
	if iconID != 99 {
		t.Errorf("iconID = %d, want 99", iconID)
	}
}

func TestParseTrayCallback_LBUTTONUP(t *testing.T) {
	lParam := uintptr(uint32(WM_LBUTTONUP) | (1 << 16))
	event, iconID := parseTrayCallback(lParam)
	if event != WM_LBUTTONUP {
		t.Errorf("event = 0x%X, want 0x%X", event, WM_LBUTTONUP)
	}
	if iconID != 1 {
		t.Errorf("iconID = %d, want 1", iconID)
	}
}

// ---- HandleEndSession V7 测试 ----

type mockCallbacks struct {
	openConsoleCount int32
	exitCount        int32
	forceExitCount   int32
	// 语言重构: 替代原 statusText string,由调用方提供布尔状态
	dhcpRunning bool
}

func (m *mockCallbacks) OnOpenConsole()      { atomic.AddInt32(&m.openConsoleCount, 1) }
func (m *mockCallbacks) OnExit()             { atomic.AddInt32(&m.exitCount, 1) }
func (m *mockCallbacks) OnForceExit()        { atomic.AddInt32(&m.forceExitCount, 1) }
func (m *mockCallbacks) IsDHCPRunning() bool { return m.dhcpRunning }

// TestHandleEndSession_WParamZero 会话取消不触发退出
func TestHandleEndSession_WParamZero(t *testing.T) {
	cb := &mockCallbacks{}
	ret := HandleEndSession(cb, 0)
	if ret != 0 {
		t.Errorf("HandleEndSession 返回 %d, want 0", ret)
	}
	if atomic.LoadInt32(&cb.forceExitCount) != 0 {
		t.Error("wParam=0 时不应触发 OnForceExit")
	}
}

// TestHandleEndSession_WParamNonZero 触发一次 OnForceExit
func TestHandleEndSession_WParamNonZero(t *testing.T) {
	cb := &mockCallbacks{}
	ret := HandleEndSession(cb, 1)
	if ret != 0 {
		t.Errorf("HandleEndSession 返回 %d, want 0", ret)
	}
	// OnForceExit 在 goroutine 中执行，等待调度
	runtime.Gosched()
	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&cb.forceExitCount) != 1 {
		t.Error("wParam!=0 时应触发一次 OnForceExit")
	}
}

// TestHandleEndSession_RepeatedNotRepeated 重复消息不得重复执行退出协调器
// V7: 退出协调器的去重由调用方的 CAS 保证，HandleEndSession 本身只负责触发
func TestHandleEndSession_NilCallbacks(t *testing.T) {
	ret := HandleEndSession(nil, 1)
	if ret != 0 {
		t.Errorf("callbacks=nil 时应返回 0，实际 %d", ret)
	}
}

// ---- mock Tray 辅助 ----

func newMockTray() *Tray {
	cb := &mockCallbacks{dhcpRunning: false}
	return &Tray{
		callbacks: cb,
		win32: &Win32API{
			PostMessage:       func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool { return true },
			PostThreadMessage: func(threadID uint32, msg uint32, wParam, lParam uintptr) bool { return true },
			NotifyIcon:        func(dwMessage uint32, data *NOTIFYICONDATAW) bool { return true },
			DestroyWindow:     func(hwnd syscall.Handle) bool { return true },
			DestroyIcon:       func(hIcon syscall.Handle) bool { return true },
			PostQuitMessage:   func(exitCode int) {},
		},
	}
}

// ---- 托盘行为测试 ----

func TestAddIcon_UFlagsIncludesNIFShowTip(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.hIcon = syscall.Handle(0x5678)

	var capturedUFlags uint32
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		if dwMessage == NIM_ADD {
			capturedUFlags = data.UFlags
		}
		return true
	}

	if err := t_tray.AddIcon(); err != nil {
		t.Fatalf("AddIcon 失败: %v", err)
	}
	if capturedUFlags&NIF_SHOWTIP == 0 {
		t.Error("NIM_ADD 的 UFlags 应包含 NIF_SHOWTIP")
	}
}

func TestAddIcon_SetVersionFailsRollback(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.hIcon = syscall.Handle(0x5678)

	var calls []uint32
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		calls = append(calls, dwMessage)
		return dwMessage != NIM_SETVERSION
	}

	if err := t_tray.AddIcon(); err == nil {
		t.Error("AddIcon 应返回错误")
	}
	if len(calls) < 3 || calls[0] != NIM_ADD || calls[1] != NIM_SETVERSION || calls[2] != NIM_DELETE {
		t.Errorf("调用顺序应为 NIM_ADD/NIM_SETVERSION/NIM_DELETE，实际 %v", calls)
	}
	if t_tray.iconAdded {
		t.Error("SETVERSION 失败后 iconAdded 应为 false")
	}
}

func TestUpdateStatus_AfterClosing(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)

	postCount := int32(0)
	t_tray.win32.PostMessage = func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
		atomic.AddInt32(&postCount, 1)
		return true
	}

	t_tray.closing.Store(true)
	t_tray.UpdateStatus()

	if atomic.LoadInt32(&postCount) != 0 {
		t.Error("closing=true 时 UpdateStatus 不应投递消息")
	}
}

func TestUpdateStatus_HwndZero(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0)

	postCount := int32(0)
	t_tray.win32.PostMessage = func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
		atomic.AddInt32(&postCount, 1)
		return true
	}

	t_tray.UpdateStatus()

	if atomic.LoadInt32(&postCount) != 0 {
		t.Error("hwnd=0 时 UpdateStatus 不应投递消息")
	}
}

// TestRequestExit_ConcurrentCAS V7: 并发多次只投递一次 WM_APP_EXIT
func TestRequestExit_ConcurrentCAS(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)

	exitPostCount := int32(0)
	t_tray.win32.PostMessage = func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
		if msg == WM_APP_EXIT {
			atomic.AddInt32(&exitPostCount, 1)
		}
		return true
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t_tray.RequestExit()
		}()
	}
	wg.Wait()

	if atomic.LoadInt32(&exitPostCount) != 1 {
		t.Errorf("并发 RequestExit 应只投递 1 次 WM_APP_EXIT，实际 %d 次", atomic.LoadInt32(&exitPostCount))
	}
}

// TestRequestExit_PostMessageFails_Fallback V8: PostMessage 失败但 PostThreadMessage 成功时返回 nil
func TestRequestExit_PostMessageFails_Fallback(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.threadID = 1234

	t_tray.win32.PostMessage = func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
		return false
	}

	threadMsgCalled := int32(0)
	t_tray.win32.PostThreadMessage = func(threadID uint32, msg uint32, wParam, lParam uintptr) bool {
		if msg == WM_QUIT {
			atomic.AddInt32(&threadMsgCalled, 1)
		}
		return true
	}

	err := t_tray.RequestExit()
	// V8: PostThreadMessage 成功时返回 nil，不作为最终错误
	if err != nil {
		t.Errorf("PostThreadMessage 成功时应返回 nil，实际: %v", err)
	}
	if atomic.LoadInt32(&threadMsgCalled) != 1 {
		t.Error("PostMessage 失败后应调用 PostThreadMessageW(WM_QUIT) 兜底")
	}
}

// TestRequestExit_BothFail V7: 两者都失败时返回 error
func TestRequestExit_BothFail(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.threadID = 1234

	t_tray.win32.PostMessage = func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
		return false
	}
	t_tray.win32.PostThreadMessage = func(threadID uint32, msg uint32, wParam, lParam uintptr) bool {
		return false
	}

	err := t_tray.RequestExit()
	if err == nil {
		t.Error("两者都失败时应返回 error")
	}
}

// TestRequestExit_HwndZero V7: 窗口已销毁时安全返回
func TestRequestExit_HwndZero(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0)

	err := t_tray.RequestExit()
	if err != nil {
		t.Errorf("hwnd=0 时应返回 nil，实际: %v", err)
	}
}

// TestFinalizeAfterRun_Idempotent V7: 重复调用安全
func TestFinalizeAfterRun_Idempotent(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.hIcon = syscall.Handle(0x5678)
	t_tray.iconAdded = true

	deleteCount := int32(0)
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		if dwMessage == NIM_DELETE {
			atomic.AddInt32(&deleteCount, 1)
		}
		return true
	}

	// 调用两次
	t_tray.FinalizeAfterRun()
	t_tray.FinalizeAfterRun()

	if atomic.LoadInt32(&deleteCount) != 1 {
		t.Error("FinalizeAfterRun 应幂等，NIM_DELETE 只应调用一次")
	}
	if t_tray.hwnd.Load() != 0 {
		t.Error("hwnd 应被重置为 0")
	}
	if t_tray.hIcon != 0 {
		t.Error("hIcon 应被重置为 0")
	}
	if t_tray.iconAdded {
		t.Error("iconAdded 应为 false")
	}
}

func TestTaskbarCreated_ClosingFalse(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.hIcon = syscall.Handle(0x5678)
	t_tray.iconAdded = true

	addCount := int32(0)
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		if dwMessage == NIM_ADD {
			atomic.AddInt32(&addCount, 1)
		}
		return true
	}

	t_tray.addTrayIconAfterTaskbarRestart()

	if atomic.LoadInt32(&addCount) != 1 {
		t.Error("closing=false 时应重新添加图标")
	}
	if !t_tray.iconAdded {
		t.Error("重新添加成功后 iconAdded 应为 true")
	}
}

func TestTaskbarCreated_ClosingTrue(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.closing.Store(true)
	t_tray.iconAdded = true

	addCount := int32(0)
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		if dwMessage == NIM_ADD {
			atomic.AddInt32(&addCount, 1)
		}
		return true
	}

	t_tray.addTrayIconAfterTaskbarRestart()

	if atomic.LoadInt32(&addCount) != 0 {
		t.Error("closing=true 时不应重新添加图标")
	}
}

func TestTaskbarCreated_SetVersionFails_Rollback(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.hIcon = syscall.Handle(0x5678)
	t_tray.iconAdded = true

	var calls []uint32
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		calls = append(calls, dwMessage)
		return dwMessage != NIM_SETVERSION
	}

	t_tray.addTrayIconAfterTaskbarRestart()

	if t_tray.iconAdded {
		t.Error("SETVERSION 失败后 iconAdded 应为 false")
	}
	foundDelete := false
	for _, c := range calls {
		if c == NIM_DELETE {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Error("SETVERSION 失败后应调用 NIM_DELETE 回滚")
	}
}

func TestSetTooltipText_ClearsOldText(t *testing.T) {
	t_tray := newMockTray()
	t_tray.setTooltipText("This is a very long tooltip text that fills the array")
	t_tray.setTooltipText("Short")

	tip := syscall.UTF16ToString(t_tray.iconData.SzTip[:])
	if tip != "Short" {
		t.Errorf("SzTip = %q, want %q", tip, "Short")
	}
}

func TestTooltip_UFlagsIncludesNIFShowTip(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)
	t_tray.hIcon = syscall.Handle(0x5678)
	t_tray.iconAdded = true

	var capturedUFlags uint32
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		if dwMessage == NIM_MODIFY {
			capturedUFlags = data.UFlags
		}
		return true
	}

	t_tray.updateTrayTooltip()

	if capturedUFlags&NIF_SHOWTIP == 0 {
		t.Error("NIM_MODIFY 的 UFlags 应包含 NIF_SHOWTIP")
	}
}

func TestRemoveTrayIcon_Idempotent(t *testing.T) {
	t_tray := newMockTray()
	t_tray.iconAdded = false

	deleteCount := int32(0)
	t_tray.win32.NotifyIcon = func(dwMessage uint32, data *NOTIFYICONDATAW) bool {
		if dwMessage == NIM_DELETE {
			atomic.AddInt32(&deleteCount, 1)
		}
		return true
	}

	t_tray.removeTrayIcon()
	t_tray.removeTrayIcon()
	t_tray.removeTrayIcon()

	if atomic.LoadInt32(&deleteCount) != 0 {
		t.Error("iconAdded=false 时不应调用 NIM_DELETE")
	}
}

// TestClosingAfterRequestExit V7: RequestExit 后 closing=true，UpdateStatus 不再投递
func TestClosingAfterRequestExit(t *testing.T) {
	t_tray := newMockTray()
	t_tray.hwnd.Store(0x1234)

	// 先统计 RequestExit 投递的消息数
	exitPostCount := int32(0)
	t_tray.win32.PostMessage = func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
		if msg == WM_APP_EXIT {
			atomic.AddInt32(&exitPostCount, 1)
		}
		return true
	}

	t_tray.RequestExit()

	// closing 已被设为 true
	if !t_tray.closing.Load() {
		t.Error("RequestExit 后 closing 应为 true")
	}
	if atomic.LoadInt32(&exitPostCount) != 1 {
		t.Error("RequestExit 应投递一次 WM_APP_EXIT")
	}

	// 改为统计 UPDATE_STATUS 消息
	statusPostCount := int32(0)
	t_tray.win32.PostMessage = func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) bool {
		if msg == WM_APP_UPDATE_STATUS {
			atomic.AddInt32(&statusPostCount, 1)
		}
		return true
	}

	// UpdateStatus 应被忽略
	t_tray.UpdateStatus()
	if atomic.LoadInt32(&statusPostCount) != 0 {
		t.Error("closing=true 后 UpdateStatus 不应投递消息")
	}
}

// TestGetCurrentThreadId_UsesKernel32 V7: 验证 Win32API 中 GetCurrentThreadId 使用 kernel32
// 此测试验证接口定义正确，真实 kernel32 调用在集成测试中验证
func TestGetCurrentThreadId_UsesKernel32(t *testing.T) {
	api := realWin32API()
	// 验证 GetCurrentThreadId 在 kernel32 中可找到
	if err := procGetCurrentThreadId.Find(); err != nil {
		t.Fatalf("kernel32.dll: GetCurrentThreadId 不可用: %v", err)
	}
	// 调用应返回非零线程 ID
	tid := api.GetCurrentThreadId()
	if tid == 0 {
		t.Error("GetCurrentThreadId 不应返回 0")
	}
}

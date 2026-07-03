package main

import (
	"DacatDHCP/internal/server"
	"DacatDHCP/internal/singleinstance"
	"DacatDHCP/internal/systray"
	"DacatDHCP/web"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	app       *server.AppServer
	inst      *singleinstance.Instance
	tray      *systray.Tray
	isClosing int32 // atomic flag，防止退出重入

	// V6: sync.Once 退出协调器，托盘退出、关机、注销复用同一个
	exitOnce sync.Once
)

// ============================================================
// Win32 API
// ============================================================

var (
	user32Dll       = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32Dll.NewProc("MessageBoxW")

	shell32Dll        = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = shell32Dll.NewProc("ShellExecuteW")
)

const (
	MB_OK          = 0x00000000
	MB_ICONERROR   = 0x00000010
	MB_YESNO       = 0x00000004
	MB_ICONWARNING = 0x00000030
	MB_DEFBUTTON2  = 0x00000100
	IDYES          = 6
)

func isAdmin() bool {
	var token syscall.Token
	currentProcess, procErr := syscall.GetCurrentProcess()
	if procErr != nil {
		return false
	}
	err := syscall.OpenProcessToken(currentProcess, syscall.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()
	var elevation uint32
	var returnedLen uint32
	err = syscall.GetTokenInformation(token, 20, (*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &returnedLen)
	if err != nil {
		return false
	}
	return elevation != 0
}

func runAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var showCmd int32 = 1
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	ret, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, uintptr(showCmd))
	if ret <= 32 {
		return syscall.Errno(uintptr(ret))
	}
	return nil
}

func getDataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "data"
	}
	return filepath.Join(filepath.Dir(exe), "data")
}

func openBrowser(url string) {
	urlPtr, _ := syscall.UTF16PtrFromString(url)
	openPtr, _ := syscall.UTF16PtrFromString("open")
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(openPtr)), uintptr(unsafe.Pointer(urlPtr)), 0, 0, 1)
}

func showErrorBox(title, msg string) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	msgPtr, _ := syscall.UTF16PtrFromString(msg)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), MB_OK|MB_ICONERROR)
}

func showConfirmBox(title, msg string) bool {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	msgPtr, _ := syscall.UTF16PtrFromString(msg)
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), MB_YESNO|MB_ICONWARNING|MB_DEFBUTTON2)
	return ret == IDYES
}

func readWebPortFromConfig(dataDir string) int {
	configPath := filepath.Join(dataDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 8765
	}
	var cfg struct {
		WebPort int `json:"web_port"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.WebPort <= 0 {
		return 8765
	}
	return cfg.WebPort
}

func consoleURL() string {
	if app != nil {
		addr := app.ListenAddr()
		if addr != "" {
			return "http://" + addr
		}
	}
	return "http://127.0.0.1:8765"
}

// ============================================================
// trayCallbacks
// ============================================================

type trayCallbacks struct{}

func (t *trayCallbacks) OnOpenConsole() {
	openBrowser(consoleURL())
}

func (t *trayCallbacks) GetStatusText() string {
	if app != nil && app.IsDHCPRunning() {
		return "DHCP: 运行中"
	}
	return "DHCP: 已停止"
}

// OnExit 用户请求退出（含确认）
func (t *trayCallbacks) OnExit() {
	if !atomic.CompareAndSwapInt32(&isClosing, 0, 1) {
		return
	}
	if app != nil && app.IsDHCPRunning() {
		if !showConfirmBox("DacatDHCP", "DHCP 服务正在运行中，退出将停止 DHCP 服务。\n\n确定要退出吗？") {
			atomic.StoreInt32(&isClosing, 0)
			return
		}
	}
	doCleanup()
}

// OnForceExit 强制退出（关机/注销，不确认）
// V6: 由 WM_ENDSESSION 通过 goroutine 触发，不阻塞系统消息线程
func (t *trayCallbacks) OnForceExit() {
	if !atomic.CompareAndSwapInt32(&isClosing, 0, 1) {
		return
	}
	doCleanup()
}

// ============================================================
// 统一退出协调器（V6: sync.Once 保证只执行一次）
// ============================================================

// doCleanup 执行完整退出清理
// V8: 退出顺序——设置closing→禁用回调→CloseServices→RequestExit
//
//	→tray.Run返回→FinalizeAfterRun→释放单实例锁→CloseLogger
//
// 保证退出阶段的错误在 CloseLogger 前写入日志
func doCleanup() {
	exitOnce.Do(func() {
		if app != nil {
			// V8: 先清除状态变化回调
			app.ClearOnStatusChange()
			// 禁止新的 Web API 操作
			app.SetClosing()
			// V8: 停止 DHCP→关闭 HTTP（不关闭日志）
			app.CloseServices()
		}

		// V8: 请求托盘退出（投递 WM_APP_EXIT 或 PostThreadMessageW 兜底）
		if tray != nil {
			if err := tray.RequestExit(); err != nil {
				// V8: 两种退出消息均失败，在日志关闭前记录错误
				if app != nil {
					app.Logf("RequestExit 失败: %v", err)
				}
				// V8: 进程级兜底——DHCP 和 HTTP 已安全关闭后强制退出
				showErrorBox("DacatDHCP", "托盘消息循环无法退出，程序将强制结束。")
				os.Exit(1)
			}
		}

		// V8: 单实例锁在 tray.Run 返回后释放（由 main 函数执行）
		// 日志在 CloseLogger 中关闭（由 main 函数在释放锁后执行）
	})
}

// ============================================================
// main
// ============================================================

func main() {
	// 1. 检查管理员权限
	if !isAdmin() {
		if err := runAsAdmin(); err != nil {
			showErrorBox("DacatDHCP 启动失败",
				"需要管理员权限运行 DHCP 服务。\n\nUAC 提权失败，请右键点击程序选择\"以管理员身份运行\"。")
			os.Exit(1)
		}
		os.Exit(0)
	}

	dataDir := getDataDir()

	// 2. 单实例检测
	var isFirst bool
	var acquireErr error
	inst, isFirst, acquireErr = singleinstance.Acquire()
	if !isFirst {
		if acquireErr != nil {
			showErrorBox("DacatDHCP 启动失败", "单实例检测失败: "+acquireErr.Error())
			os.Exit(1)
		}
		webPort := readWebPortFromConfig(dataDir)
		openBrowser("http://127.0.0.1:" + itoa(webPort))
		os.Exit(0)
	}

	// 3. 创建应用服务器
	var err error
	app, err = server.NewAppServer(dataDir, web.Assets)
	if err != nil {
		showErrorBox("DacatDHCP 启动失败", "初始化失败: "+err.Error())
		inst.Release()
		os.Exit(1)
	}
	app.PostInit()
	app.SetIsAdmin(isAdmin()) // V10新增: 注入真实管理员权限状态,前端底部栏禁止伪造

	// 4. 启动 HTTP 服务器
	if err := app.Start(); err != nil {
		showErrorBox("DacatDHCP 启动失败", "管理服务启动失败: "+err.Error())
		app.Close()
		inst.Release()
		os.Exit(1)
	}

	// 5. LockOSThread（V8: 只允许一次 Lock，对应的 Unlock 在 FinalizeAfterRun 之后）
	runtime.LockOSThread()

	// 6. 创建隐藏窗口 + 消息过滤
	tray, err = systray.NewTray(&trayCallbacks{}, app.Logf)
	if err != nil {
		app.Logf("系统托盘创建失败: %v", err)
		showErrorBox("DacatDHCP 启动失败", "系统托盘创建失败: "+err.Error())
		app.Close()
		inst.Release()
		os.Exit(1)
	}

	// 7. 添加托盘图标（失败时不得打开浏览器或留下后台进程）
	if err := tray.AddIcon(); err != nil {
		app.Logf("托盘图标添加失败: %v", err)
		showErrorBox("DacatDHCP 启动失败", "托盘图标添加失败: "+err.Error())
		app.Close()
		tray.Destroy()
		inst.Release()
		os.Exit(1)
	}

	// 8. 设置 DHCP 状态变化回调
	app.SetOnStatusChange(func() {
		if tray != nil {
			tray.UpdateStatus()
		}
	})

	// 9. 打开浏览器
	openBrowser(consoleURL())

	// 10. 进入消息循环（阻塞，直到 WM_APP_EXIT 触发 PostQuitMessage）
	tray.Run()

	// V8: FinalizeAfterRun 兜底清理（幂等，即使 WM_APP_EXIT 已清理也不影响）
	// 错误在 CloseLogger 前写入日志
	tray.FinalizeAfterRun()

	// V8: 同一 OS 线程解锁（NewTray、消息循环、FinalizeAfterRun 均在同一线程）
	runtime.UnlockOSThread()

	// V8: tray.Run 返回后，托盘窗口已销毁，此时释放单实例锁
	// 保证旧进程托盘尚未销毁时，新进程不能取得锁
	if inst != nil {
		inst.Release()
	}

	// V8: 最后关闭日志（确保退出阶段的错误已写入）
	if app != nil {
		app.CloseLogger()
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

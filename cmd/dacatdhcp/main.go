package main

import (
	"DacatDHCP/internal/i18n"
	"DacatDHCP/internal/server"
	"DacatDHCP/internal/singleinstance"
	"DacatDHCP/internal/systray"
	"DacatDHCP/internal/version"
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

	// 语言新增: kernel32 加载 GetUserDefaultUILanguage,用于首次运行检测 Windows 界面语言
	kernel32Dll                  = syscall.NewLazyDLL("kernel32.dll")
	procGetUserDefaultUILanguage = kernel32Dll.NewProc("GetUserDefaultUILanguage")
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

// detectWindowsUILanguage 通过 GetUserDefaultUILanguage 检测 Windows 界面语言
// 语言新增: 首次运行无有效配置时调用,中文系统返回 zh-CN,英文系统返回 en-US
// 无法识别时回退 zh-CN;API 不可用时也回退 zh-CN
func detectWindowsUILanguage() string {
	if err := procGetUserDefaultUILanguage.Find(); err != nil {
		return i18n.LangZhCN
	}
	ret, _, _ := procGetUserDefaultUILanguage.Call()
	langID := uint32(ret)
	// LANGID 低 10 位为 Primary Language ID
	// 0x04 = 中文, 0x09 = 英文
	primaryLang := langID & 0x3FF
	switch primaryLang {
	case 0x04:
		return i18n.LangZhCN
	case 0x09:
		return i18n.LangEnUS
	default:
		return i18n.LangZhCN
	}
}

// loadStartupLanguage 在显示任何 MessageBox 之前加载已保存的语言配置
// 收口: 使用 ParseLanguage 严格解析,仅 zh-CN/en-US 通过;空值或无效值才检测 Windows 界面语言
// 英文 Windows 环境选 en-US,其余回退 zh-CN
// 加载完成后通过 i18n.SetLanguage 设置全局语言,供后续 MessageBox 使用
func loadStartupLanguage(dataDir string) {
	configPath := filepath.Join(dataDir, "config.json")
	if data, err := os.ReadFile(configPath); err == nil {
		var cfg struct {
			Language string `json:"language"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			// 严格解析: 仅 zh-CN/en-US 通过,拒绝 zh/en/中文 等别名
			if parsed, ok := i18n.ParseLanguage(cfg.Language); ok {
				i18n.SetLanguage(parsed)
				return
			}
		}
	}
	// 配置缺失或无效,检测 Windows 界面语言
	i18n.SetLanguage(detectWindowsUILanguage())
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

// IsDHCPRunning 语言重构: 替代原 GetStatusText,由托盘自行生成本地化文本
// 禁止通过比较中文字符串判断状态
func (t *trayCallbacks) IsDHCPRunning() bool {
	return app != nil && app.IsDHCPRunning()
}

// OnExit 用户请求退出（含确认）
// 语言重构: 退出确认文案接入 i18n,标题使用 version.ProductName(),正文使用 msgbox.exit_confirm
func (t *trayCallbacks) OnExit() {
	if !atomic.CompareAndSwapInt32(&isClosing, 0, 1) {
		return
	}
	if app != nil && app.IsDHCPRunning() {
		if !showConfirmBox(version.ProductName(), i18n.T("msgbox.exit_confirm")) {
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
// 语言重构: 强制退出 MessageBox 文案接入 i18n
func doCleanup() {
	exitOnce.Do(func() {
		if app != nil {
			// V8: 先清除状态变化回调
			app.ClearOnStatusChange()
			// 语言新增: 同步清除语言变化回调
			app.ClearOnLanguageChange()
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
				// 语言重构: 强制退出提示文案接入 i18n,标题用产品名,正文含 %s 由 FormatErrorf 传入产品名
				showErrorBox(version.ProductName(), i18n.Tf("msgbox.tray_exit_forced", version.ProductName()))
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
	// 语言新增: 1. 先获取数据目录并加载已保存语言,在任何 MessageBox 之前完成
	// 首次运行无有效配置时由 detectWindowsUILanguage 检测 Windows 界面语言
	dataDir := getDataDir()
	loadStartupLanguage(dataDir)

	// 2. 检查管理员权限
	if !isAdmin() {
		if err := runAsAdmin(); err != nil {
			// 语言重构: 管理员权限不足提示接入 i18n,标题用产品名,正文含 %s 由 FormatErrorf 传入产品名
			showErrorBox(version.ProductName(), i18n.FormatErrorf("msgbox.admin_required", err.Error(), version.ProductName()))
			os.Exit(1)
		}
		os.Exit(0)
	}

	// 3. 单实例检测
	var isFirst bool
	var acquireErr error
	inst, isFirst, acquireErr = singleinstance.Acquire()
	if !isFirst {
		if acquireErr != nil {
			// 语言重构: 单实例检测失败提示接入 i18n,标题用产品名,正文不含 %s,详细信息附在本地化主提示之后
			showErrorBox(version.ProductName(), i18n.FormatError("msgbox.single_instance_failed", acquireErr.Error()))
			os.Exit(1)
		}
		webPort := readWebPortFromConfig(dataDir)
		openBrowser("http://127.0.0.1:" + itoa(webPort))
		os.Exit(0)
	}

	// 4. 创建应用服务器
	var err error
	app, err = server.NewAppServer(dataDir, web.Assets)
	if err != nil {
		// 语言重构: 初始化失败提示接入 i18n,标题用产品名,正文含 %s 由 FormatErrorf 传入产品名
		showErrorBox(version.ProductName(), i18n.FormatErrorf("msgbox.init_failed", err.Error(), version.ProductName()))
		inst.Release()
		os.Exit(1)
	}
	app.PostInit()
	app.SetIsAdmin(isAdmin()) // V10新增: 注入真实管理员权限状态,前端底部栏禁止伪造

	// 5. 启动 HTTP 服务器
	if err := app.Start(); err != nil {
		// 语言重构: 管理服务启动失败提示接入 i18n,标题用产品名,正文含 %s 由 FormatErrorf 传入产品名
		showErrorBox(version.ProductName(), i18n.FormatErrorf("msgbox.http_start_failed", err.Error(), version.ProductName()))
		app.Close()
		inst.Release()
		os.Exit(1)
	}

	// 6. LockOSThread（V8: 只允许一次 Lock，对应的 Unlock 在 FinalizeAfterRun 之后）
	runtime.LockOSThread()

	// 7. 创建隐藏窗口 + 消息过滤
	tray, err = systray.NewTray(&trayCallbacks{}, app.Logf)
	if err != nil {
		app.Logf("系统托盘创建失败: %v", err)
		// 语言重构: 托盘创建失败提示接入 i18n,标题用产品名,正文含 %s 由 FormatErrorf 传入产品名
		showErrorBox(version.ProductName(), i18n.FormatErrorf("msgbox.tray_create_failed", err.Error(), version.ProductName()))
		app.Close()
		inst.Release()
		os.Exit(1)
	}

	// 8. 添加托盘图标（失败时不得打开浏览器或留下后台进程）
	if err := tray.AddIcon(); err != nil {
		app.Logf("托盘图标添加失败: %v", err)
		// 语言重构: 托盘图标添加失败提示接入 i18n,标题用产品名,正文含 %s 由 FormatErrorf 传入产品名
		showErrorBox(version.ProductName(), i18n.FormatErrorf("msgbox.tray_icon_failed", err.Error(), version.ProductName()))
		app.Close()
		tray.Destroy()
		inst.Release()
		os.Exit(1)
	}

	// 9. 设置 DHCP 状态变化回调
	app.SetOnStatusChange(func() {
		if tray != nil {
			tray.UpdateStatus()
		}
	})

	// 语言新增: 10. 设置语言变化回调,语言保存成功后通知托盘刷新 Tooltip 和右键菜单
	app.SetOnLanguageChange(func() {
		if tray != nil {
			tray.UpdateStatus()
		}
	})

	// 11. 打开浏览器
	openBrowser(consoleURL())

	// 12. 进入消息循环（阻塞，直到 WM_APP_EXIT 触发 PostQuitMessage）
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

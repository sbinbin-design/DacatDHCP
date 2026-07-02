package main

import (
	"DacatDHCP/internal/server"
	"DacatDHCP/web"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"unsafe"
)

// isAdmin 检查当前进程是否拥有管理员权限
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

	// TokenElevation = 20
	var elevation uint32
	var returnedLen uint32
	err = syscall.GetTokenInformation(token, 20, (*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &returnedLen)
	if err != nil {
		return false
	}
	return elevation != 0
}

// runAsAdmin 通过 UAC 提权重新启动程序
func runAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取程序路径失败: %v", err)
	}

	var showCmd int32 = 1 // SW_SHOWNORMAL
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)

	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellExecuteW := shell32.NewProc("ShellExecuteW")

	ret, _, _ := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0,
		0,
		uintptr(showCmd),
	)

	if ret <= 32 {
		return fmt.Errorf("UAC 提权失败 (返回值: %d)", ret)
	}
	return nil
}

// openBrowser 打开默认浏览器
func openBrowser(url string) {
	// 使用 cmd /c start 命令打开浏览器，兼容 Windows 7+
	exec.Command("cmd", "/c", "start", "", url).Start()
}

// getDataDir 获取数据目录路径（EXE 同级的 data 目录）
func getDataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "data"
	}
	return filepath.Join(filepath.Dir(exe), "data")
}

func main() {
	// 检查管理员权限
	if !isAdmin() {
		fmt.Println("需要管理员权限，正在请求 UAC 提权...")
		if err := runAsAdmin(); err != nil {
			fmt.Fprintf(os.Stderr, "UAC 提权失败: %v\n", err)
			fmt.Println("请右键点击程序选择'以管理员身份运行'。")
			fmt.Println("按回车键退出...")
			var input string
			fmt.Scanln(&input)
			os.Exit(1)
		}
		// 提权后新进程已启动，当前进程退出
		os.Exit(0)
	}

	dataDir := getDataDir()

	// 创建应用服务器
	app, err := server.NewAppServer(dataDir, web.Assets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		fmt.Println("按回车键退出...")
		var input string
		fmt.Scanln(&input)
		os.Exit(1)
	}

	// V1修复: 先启动 HTTP 服务器并确认绑定成功，再打开浏览器
	// 端口占用时明确报错并退出，不得留下空进程
	if err := app.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		fmt.Println("按回车键退出...")
		var input string
		fmt.Scanln(&input)
		os.Exit(1)
	}

	// HTTP 服务器绑定成功后再打开浏览器
	listenAddr := app.ListenAddr()
	openBrowser(fmt.Sprintf("http://%s", listenAddr))

	fmt.Printf("DacatDHCP V1 已启动 - 管理页面: http://%s\n", listenAddr)
	fmt.Println("关闭此窗口将停止 DHCP 服务")

	// 等待退出信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("正在停止服务...")
	app.Close()
	fmt.Println("已退出")
}

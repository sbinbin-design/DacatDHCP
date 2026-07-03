package server

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maxLogSize    = 2 * 1024 * 1024 // 2MB
	logRingSize   = 500             // 内存中保留的最近日志条数
	logTimeFormat = "2006-01-02 15:04:05"
)

// Logger 提供日志记录功能，同时写入文件和内存环形缓冲区
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	ring     []string
	ringPos  int
	ringLen  int
}

// NewLogger 创建日志记录器
func NewLogger(logDir string) (*Logger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %v", err)
	}

	l := &Logger{
		filePath: filepath.Join(logDir, "dhcpsrv.log"),
		ring:     make([]string, logRingSize),
	}

	// 打开日志文件（追加模式）
	f, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("打开日志文件失败: %v", err)
	}
	l.file = f

	return l, nil
}

// Log 记录一条日志
func (l *Logger) Log(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s", time.Now().Format(logTimeFormat), msg)

	l.mu.Lock()
	defer l.mu.Unlock()

	// 写入环形缓冲区
	l.ring[l.ringPos] = line
	l.ringPos = (l.ringPos + 1) % logRingSize
	if l.ringLen < logRingSize {
		l.ringLen++
	}

	// 写入文件
	if l.file != nil {
		l.file.WriteString(line + "\n")
		l.file.Sync()

		// 检查文件大小，需要轮转
		l.rotateIfNeeded()
	}
}

// GetRecentLogs 获取最近的日志条目
func (l *Logger) GetRecentLogs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	result := make([]string, 0, l.ringLen)
	// 从环形缓冲区按时间顺序读取
	start := l.ringPos - l.ringLen
	if start < 0 {
		start += logRingSize
	}
	for i := 0; i < l.ringLen; i++ {
		idx := (start + i) % logRingSize
		result = append(result, l.ring[idx])
	}
	return result
}

// Close 关闭日志文件
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// rotateIfNeeded 在日志文件达到最大大小时执行轮转
func (l *Logger) rotateIfNeeded() {
	if l.file == nil {
		return
	}

	stat, err := l.file.Stat()
	if err != nil {
		return
	}

	if stat.Size() < int64(maxLogSize) {
		return
	}

	// 关闭当前文件
	l.file.Close()

	// 将当前日志重命名为 .old（覆盖旧的 .old）
	oldPath := l.filePath + ".old"
	os.Remove(oldPath)             // 忽略错误
	os.Rename(l.filePath, oldPath) // 忽略错误

	// 创建新的日志文件
	f, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	l.file = f
}

// WriteLog 提供 io.Writer 接口用于 log.Logger
func (l *Logger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	line := string(p)

	// 写入环形缓冲区
	l.ring[l.ringPos] = line
	l.ringPos = (l.ringPos + 1) % logRingSize
	if l.ringLen < logRingSize {
		l.ringLen++
	}

	// 写入文件
	if l.file != nil {
		l.file.Write(p)
		l.file.Sync()
		l.rotateIfNeeded()
	}

	return len(p), nil
}

// 确保实现 io.Writer 接口
var _ io.Writer = (*Logger)(nil)

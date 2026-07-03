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
	// V14测试钩子: 非 nil 时 Clear 的 Truncate 路径直接返回该错误,用于模拟截断失败
	// 仅测试使用,生产代码不得设置
	testTruncateErr error
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

// Clear V12新增: 清空内存环形缓冲区并截断当前日志文件
// V14重写: 优先在现有文件句柄上执行 Truncate(0),成功后再清空内存环形缓冲区
// 截断失败时不得关闭或替换原句柄,不得清空内存日志,保证原日志写入能力不丢失
// 若确需重新打开文件(Windows O_APPEND 句柄 Truncate 失败或句柄为 nil),
// 必须先成功取得新句柄再关闭旧句柄,任何失败路径都保证原日志写入能力不丢失
func (l *Logger) Clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// V14: 句柄为 nil 时(异常状态),先取得新句柄再赋值,失败则返回错误不修改状态
	if l.file == nil {
		f, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0644)
		if err != nil {
			// 无法打开文件,保留 nil 句柄和内存日志,返回错误
			return fmt.Errorf("reopen_log_failed: %v", err)
		}
		// 新句柄取得成功后才赋值
		l.file = f
		// 文件已是空(O_TRUNC),清空内存缓冲区
		l.clearRingLocked()
		return nil
	}

	// V14测试钩子: 模拟截断完全失败(Truncate 和 reopen 均失败),验证原句柄和内存保留
	// 测试设置此字段后 Clear 直接返回错误,不尝试 Truncate 也不尝试 reopen
	if l.testTruncateErr != nil {
		return fmt.Errorf("truncate_log_failed: %v", l.testTruncateErr)
	}

	// V14: 优先在现有句柄上 Truncate(0),不关闭不替换原句柄
	truncateErr := l.file.Truncate(0)
	if truncateErr == nil {
		// Truncate 成功,Seek 到文件头重置写入位置
		if _, err := l.file.Seek(0, io.SeekStart); err != nil {
			// Seek 失败: 保留原句柄和内存日志,返回错误
			// 文件已被 Truncate 但写入位置可能不对,后续写入仍可用(O_APPEND 会修正)
			return fmt.Errorf("seek_log_failed: %v", err)
		}
		// 文件截断和 Seek 都成功,清空内存环形缓冲区
		l.clearRingLocked()
		return nil
	}

	// V14: Truncate 失败(Windows O_APPEND 句柄不支持 Truncate),走 reopen 回退路径
	// 必须先成功取得新句柄,再关闭旧句柄;新句柄未取得前不动旧句柄
	newFile, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0644)
	if err != nil {
		// reopen 失败: 保留原句柄和内存日志,返回错误,不中断后续写入
		return fmt.Errorf("truncate_log_failed: %v", truncateErr)
	}
	// 新句柄取得成功,关闭旧句柄并替换为新句柄
	l.file.Close()
	l.file = newFile
	// 文件已被 O_TRUNC 清空,清空内存环形缓冲区
	l.clearRingLocked()
	return nil
}

// clearRingLocked 清空内存环形缓冲区(调用方必须持有 mu 锁)
func (l *Logger) clearRingLocked() {
	for i := range l.ring {
		l.ring[i] = ""
	}
	l.ringPos = 0
	l.ringLen = 0
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

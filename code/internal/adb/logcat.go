package adb

// ============================================================================
// 文件名称 : logcat.go
// 功    能 : 日志抓取会话（面向 GUI 的一键抓取）。
//            一个会话对应一台设备：启动 adb logcat 子进程（隐藏窗口），
//            日志逐行写入文件，Stop 时写入结束标记并关闭文件。
// 说明    : 界面不展示实时日志内容（目标用户为非技术人员），
//            仅由上层把 LogPath 展示给用户定位文件。
// ============================================================================

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"adb-log-helper/internal/ui"
)

// LogcatSession 管理单个设备的日志抓取会话。
// 字段说明：
//   - Address: adb -s 使用的设备标识（USB 序列号或 ip:port）
//   - ADBPath: adb 可执行文件路径
//   - SaveDir: 日志保存根目录；空字符串表示默认位置（程序目录下 logs/）
//   - Cmd/LogFile/LogPath: 运行期状态，由 Start/Stop 维护
type LogcatSession struct {
	Address string
	ADBPath string
	SaveDir string

	Cmd     *exec.Cmd
	LogFile *os.File
	LogPath string

	mu      sync.Mutex
	running bool
}

// NewLogcatSession 创建一个新的日志抓取会话。
// address 既可以是 USB 序列号（如 emulator-5554），也可以是 ip:port（如 192.168.1.100:5555）。
func NewLogcatSession(adbPath string, address string) *LogcatSession {
	return &LogcatSession{
		ADBPath: adbPath,
		Address: address,
	}
}

// Start 启动日志抓取（后台运行）。
// 日志保存到 <SaveDir 或 logs>/<设备标识>/adb_log_<时间戳>.log，
// 命令为 adb -s <address> logcat -v time。
// 入参:
//   - ctx: 取消信号（取消后读取 goroutine 退出；子进程由 Stop 终止）
//
// 返回: 启动失败时的错误（文件句柄已兜底关闭，无泄漏）
func (s *LogcatSession) Start(ctx context.Context) error {
	// 日志目录名用设备标识；Windows 目录名不能含冒号，替换为下划线
	logsDirName := strings.ReplaceAll(s.Address, ":", "_")

	// 日志保存根目录：用户指定了 SaveDir 则用之，否则默认程序目录下的 logs/
	baseDir := s.SaveDir
	if baseDir == "" {
		baseDir = filepath.Join(".", "logs")
	}

	// 创建设备专属日志目录: <保存根目录>/<设备标识>/
	logsDir := filepath.Join(baseDir, logsDirName)
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("创建日志目录失败: %w", err)
	}

	// 生成带时间戳的日志文件名
	timestamp := time.Now().Format("20060102_150405")
	logFileName := fmt.Sprintf("adb_log_%s.log", timestamp)
	logFilePath := filepath.Join(logsDir, logFileName)

	// 打开日志文件
	logFile, err := os.Create(logFilePath)
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %w", err)
	}
	s.LogFile = logFile

	// 兜底：本函数后续步骤（获取管道、启动子进程）若失败，必须关闭已打开的日志文件，
	// 否则文件句柄泄漏。用 success 标志控制——仅当函数成功走完才不关闭。
	success := false
	defer func() {
		if !success && s.LogFile != nil {
			s.LogFile.Close()
			s.LogFile = nil
		}
	}()

	absLogPath, _ := filepath.Abs(logFilePath)
	s.LogPath = absLogPath

	// 写入日志文件头部
	header := fmt.Sprintf("=== ADB Logcat ===\n设备: %s\n开始: %s\n\n",
		s.Address, time.Now().Format("2006-01-02 15:04:05"))
	logFile.WriteString(header)

	// 启动 adb logcat -v time 子进程（hiddenCmd：不弹控制台窗口）
	s.Cmd = hiddenCmd(s.ADBPath, "-s", s.Address, "logcat", "-v", "time")

	// stdout → 日志文件
	stdoutPipe, err := s.Cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法获取 logcat 输出流: %w", err)
	}

	// stderr 单独处理（错误提示）
	stderrPipe, err := s.Cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("无法获取 logcat 错误流: %w", err)
	}

	if err := s.Cmd.Start(); err != nil {
		return fmt.Errorf("启动 logcat 失败: %w", err)
	}

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	ui.Success("[%s] 日志抓取已启动: %s", s.Address, s.LogPath)

	// 读取 stdout — 逐行写入日志文件；读到 EOF（子进程结束）自然退出
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		// 单行缓冲放大到 1MB：logcat 偶有超长行，默认 64KB 会截断报错
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
				line := scanner.Bytes()
				s.LogFile.Write(line)
				s.LogFile.Write([]byte("\n"))
			}
		}
	}()

	// 读取 stderr — 仅用于错误提示（GUI 下控制台输出被丢弃，仅写日志留痕）
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
				ui.Warn("[%s stderr] %s", s.Address, scanner.Text())
			}
		}
	}()

	// 标记成功：阻止上方 defer 关闭日志文件（文件交由后台 goroutine 持续写入）
	success = true
	return nil
}

// Stop 停止日志抓取并关闭文件（幂等，可重复调用）。
// 终止子进程 → 写入结束标记 → 关闭日志文件。
func (s *LogcatSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}
	s.running = false

	if s.Cmd != nil && s.Cmd.Process != nil {
		s.Cmd.Process.Kill()
		s.Cmd.Wait()
	}

	if s.LogFile != nil {
		footer := fmt.Sprintf("\n=== 结束: %s ===\n",
			time.Now().Format("2006-01-02 15:04:05"))
		s.LogFile.WriteString(footer)
		s.LogFile.Close()
	}

	ui.Success("[%s] 日志抓取已停止: %s", s.Address, s.LogPath)
}

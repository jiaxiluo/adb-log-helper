//go:build windows

package cmdshell

// ============================================================================
// 文件名称 : shellsession.go
// 功    能 : 底部面板「命令行模式」的交互式 shell 会话：长驻子进程 +
//            stdin 管道写入 + stdout 流式回调。承载裸 `adb shell`——
//            普通命令走 RunCommand 的 cmd /C 一次性执行，其 stdin 非终端，
//            adb 交互 shell 读到 EOF 立即退出（界面表现：敲了没反应），
//            必须用本会话保活进程逐行喂命令。
// 设计要点 : 1. stdin 用管道保持打开：Write 写一行，子进程读一行执行一行
//            2. stdout/stderr 逐行扫描 → 解码 → onOutput 回调（前端经
//               Wails 事件渲染）；读到 EOF 即进程退出 → onEnded 回调
//            3. Stop 幂等：杀进程 + 关 stdin；自然退出（exit 命令/设备
//               断开）与主动停止共用一次 onEnded（sync.Once 防重复）
//            4. 无超时：交互会话本身就是长驻的，生命周期由用户控制
// ============================================================================

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
)

// ShellSession 一个长驻交互式子进程会话（当前用于 adb shell）。
// 回调在独立 goroutine 触发，调用方需自行保证线程安全
type ShellSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	onOut  func(line string) // 每行输出回调（已按控制台规则解码）
	onEnd  func()            // 进程结束回调（自然退出或 Stop，保证恰好一次）
	once   sync.Once         // onEnd 只触发一次
	mu     sync.Mutex        // 保护 running 与 stdin 的并发访问
	run    bool              // 会话是否运行中
	closed bool              // stdin 是否已关闭（防重复关闭报错）
}

// NewShellSession 创建交互式会话对象（未启动）。
// 入参:
//   - path:   可执行文件路径（如 adb 绝对路径）
//   - args:   命令行参数（如 ["-s", serial, "shell"]）
//   - onOut:  每行输出回调（可为 nil，仅调试场景）
//   - onEnd:  会话结束回调（可为 nil；自然退出与主动停止都会触发）
//
// 返回: 会话对象
func NewShellSession(path string, args []string, onOut func(string), onEnd func()) *ShellSession {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return &ShellSession{
		cmd:   cmd,
		onOut: onOut,
		onEnd: onEnd,
	}
}

// Start 启动子进程并开始读取输出。
// 返回: 启动失败错误（进程与管道已兜底清理，无需再调 Stop）
func (s *ShellSession) Start() error {
	stdin, err := s.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("无法获取 shell 输入流: %w", err)
	}
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法获取 shell 输出流: %w", err)
	}
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("无法获取 shell 错误流: %w", err)
	}

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("启动 shell 失败: %w", err)
	}

	s.mu.Lock()
	s.stdin = stdin
	s.run = true
	s.mu.Unlock()

	// 读循环：stdout 逐行扫描。adb shell 的输出行到达时机不定（命令执行
	// 完才吐出），行缓冲比整块回调更贴合面板逐行渲染；读到 EOF 即进程退出。
	// 单行缓冲 1MB：与实时日志一致，防超长行截断
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			if s.onOut != nil {
				s.onOut(DecodeConsoleBytes(scanner.Bytes()))
			}
		}
	}()

	// stderr 合流：adb 的连接类报错（设备断开等）走 stderr，一并展示
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			if s.onOut != nil {
				s.onOut(DecodeConsoleBytes(scanner.Bytes()))
			}
		}
	}()

	// 退出监视：等待进程结束后触发一次 onEnd（自然退出/被杀统一走这里）
	go func() {
		_ = s.cmd.Wait()
		s.mu.Lock()
		s.run = false
		stdin := s.stdin
		s.mu.Unlock()
		if stdin != nil {
			_ = stdin.Close() // 进程已死，关管道释放描述符（重复关由 closed 标记防）
		}
		s.once.Do(func() {
			if s.onEnd != nil {
				s.onEnd()
			}
		})
	}()

	return nil
}

// Write 向会话 stdin 写入一行命令（自动补换行）。
// 入参:
//   - line: 一行命令文本（不含换行符）
//
// 返回: 会话未启动或已结束时返回错误
func (s *ShellSession) Write(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.run || s.stdin == nil {
		return fmt.Errorf("shell 会话已结束，请重新进入")
	}
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		return fmt.Errorf("写入 shell 失败（会话可能已断开）: %w", err)
	}
	return nil
}

// Running 返回会话是否运行中（进程存活且 stdin 未关闭）
func (s *ShellSession) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run
}

// Stop 主动终止会话（幂等，可重复调用）。
// 杀掉子进程并关闭 stdin；onEnd 由退出监视 goroutine 统一触发
func (s *ShellSession) Stop() {
	s.mu.Lock()
	running := s.run
	stdin := s.stdin
	closed := s.closed
	s.closed = true
	s.mu.Unlock()

	if running && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	if stdin != nil && !closed {
		_ = stdin.Close() // 先关 stdin 也能让交互进程读到 EOF 自行退出
	}
}

package adb

// ============================================================================
// 文件名称 : livelog.go
// 功    能 : 实时日志会话（参考 Android Studio Logcat 的"边产生边查看"模式）。
//            与 logcat.go 的一键抓取（落文件、无界面展示）互补：
//            本会话不写文件，把 logcat 输出逐行解析后通过回调批量推送给上层，
//            由上层（app.go）经 Wails 事件转发到前端实时渲染。
// 设计要点 : 1. 级别过滤用 logcat 原生 filter 表达式（如 *:W），在设备端就完成
//               过滤，最小化 IPC 流量；切换级别 = 重启会话（与 AS 行为一致）
//            2. 推送按"批"聚合（150ms 或 80 行触发一次），避免逐行事件把
//               WebView2 桥接打爆（高频日志下逐行事件会造成界面卡顿）
//            3. 行解析为结构体（时间/级别/Tag/PID/内容），前端直接着色过滤
//            4. 会话异常结束（设备拔出/adb 退出）通过 onEnded 回调通知前端
// ============================================================================

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"adb-log-helper/internal/ui"
)

// ---------------------------- 日志行结构与解析 ----------------------------

// LiveLogLine 是单条实时日志的结构化表示（JSON 序列化后推送给前端）。
// 字段说明：
//   - Time:    日志时间戳（logcat -v time 格式，如 "08-21 10:23:45.123"）
//   - Level:   级别单字符 V/D/I/W/E/F；无法解析的行（如 beginning 头）为 "?"
//   - Tag:     日志 Tag（如 ActivityManager）
//   - Pid:     进程号（字符串，避免前端处理数字精度）
//   - Message: 日志正文
//   - Raw:     原始整行（前端解析失败/特殊行的兜底展示）
type LiveLogLine struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Tag     string `json:"tag"`
	Pid     string `json:"pid"`
	Message string `json:"message"`
	Raw     string `json:"raw"`
}

// liveLineRe 匹配 logcat -v time 的标准行格式：
//
//	08-21 10:23:45.123 D/ActivityManager( 1234): message text
//
// 各捕获组：1=时间 2=级别 3=Tag 4=PID 5=正文
// 说明：PID 左侧可能有空格填充（老系统对齐用），用 \s* 吸收
var liveLineRe = regexp.MustCompile(
	`^(\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}) ([VDIWEF])/([^\(]*)\(\s*(\d+)\): (.*)$`)

// ParseLiveLogLine 把 logcat -v time 的一行输出解析为结构体。
// 非标准行（"--------- beginning of main" 头、空行、异常输出等）不报错，
// 整行放入 Message/Raw、Level 置 "?"，前端按"无级别行"原样展示。
// 入参:
//   - line: logcat 输出的单行文本（不含换行符）
//
// 返回: 结构化的日志行
func ParseLiveLogLine(line string) LiveLogLine {
	// 兜底初值：级别 "?" 表示"无级别行"（前端 log-q 暗灰样式），
	// 整行作为正文；匹配成功后各字段被覆盖
	parsed := LiveLogLine{Level: "?", Message: line, Raw: line}

	m := liveLineRe.FindStringSubmatch(line)
	if m == nil {
		return parsed // 非标准行：整行作为正文展示
	}
	parsed.Time = m[1]
	parsed.Level = m[2]
	// Tag 去掉首尾空格：部分设备（如机顶盒/电视系统）的 logcat 为对齐
	// 会在 Tag 与 "(" 之间填充空格（"I/adbd    (  532):"），
	// 不 trim 的话 Tag 会带尾随空格，影响显示宽度与精确匹配
	parsed.Tag = strings.TrimSpace(m[3])
	parsed.Pid = m[4]
	parsed.Message = m[5]
	return parsed
}

// logLevelPriority 给日志级别定义大小顺序（数值越大级别越高）。
// V(Verbose) 最低，F(Fatal) 最高；用于把用户选择的"最低级别"转成
// logcat 的 filter 表达式
var logLevelPriority = map[string]int{
	"V": 2, "D": 3, "I": 4, "W": 5, "E": 6, "F": 7,
}

// LogcatFilterExpr 把用户选择的最低级别转换为 logcat 原生 filter 参数。
// 例如选 "W" → "*:W"，表示所有 Tag 只输出 Warn 及以上级别的日志。
// 传入 "V"（最全量）或非法值时返回空字符串（不附加过滤，输出全部日志）。
// 入参:
//   - minLevel: 最低级别单字符（V/D/I/W/E/F）
//
// 返回: logcat filter 表达式（空 = 不过滤）
func LogcatFilterExpr(minLevel string) string {
	if _, ok := logLevelPriority[minLevel]; !ok {
		return "" // 非法值按不过滤处理（安全兜底）
	}
	if minLevel == "V" {
		return "" // Verbose 即全量，无需过滤参数
	}
	return "*:" + minLevel
}

// ---------------------------- 实时日志会话 ----------------------------

// LiveLogSession 管理单台设备的实时日志会话。
// 字段说明：
//   - Address: adb -s 使用的设备标识
//   - ADBPath: adb 可执行文件路径
//   - minLevel: 最低日志级别（构造 filter 表达式用）
//   - Pid: 目标进程号（非空时附加 --pid= 只看该进程的日志，即包名过滤的实现）
//   - Cmd: 运行中的 logcat 子进程
//
// 回调（构造时传入，均为可选；回调里不要做重活，避免拖慢读取循环）：
//   - onLines: 每聚合一批日志行回调一次（批量推送）
//   - onEnded: 会话结束（设备断开/adb 退出/Stop 调用）后回调一次
type LiveLogSession struct {
	Address  string
	ADBPath  string
	minLevel string
	Pid      string

	Cmd *exec.Cmd

	onLines func(lines []LiveLogLine)
	onEnded func(reason string)

	mu       sync.Mutex
	running  bool
	endOnce  sync.Once // 保证 onEnded 只触发一次（Stop 与自然结束竞态）
	stopFlag bool      // Stop() 主动停止标记：区分"用户停止"与"异常结束"
}

// NewLiveLogSession 创建实时日志会话。
// 入参:
//   - adbPath:  adb 可执行文件路径
//   - address:  设备标识（USB 序列号或 ip:port）
//   - minLevel: 最低日志级别（V/D/I/W/E/F，非法值按 V 全量处理）
//   - onLines:  批量行回调（可能为 nil，仅调试场景）
//   - onEnded:  会话结束回调（可能为 nil）
func NewLiveLogSession(adbPath, address, minLevel string,
	onLines func(lines []LiveLogLine), onEnded func(reason string)) *LiveLogSession {

	return &LiveLogSession{
		ADBPath:  adbPath,
		Address:  address,
		minLevel: minLevel,
		onLines:  onLines,
		onEnded:  onEnded,
	}
}

// Start 启动实时日志子进程并开始推送。
// 命令：adb -s <address> logcat -v time [--pid=<pid>] [*:<minLevel>]
// 返回: 启动失败错误（子进程已兜底清理）
func (s *LiveLogSession) Start() error {
	// 组装命令行；级别过滤交给 logcat 原生 filter（设备端过滤，流量最小）
	args := []string{"-s", s.Address, "logcat", "-v", "time"}
	if s.Pid != "" {
		// 包名过滤：logcat 不认识包名，只认 pid（Android Studio 的
		// package: 过滤同样是先解析成 pid 再过滤）
		args = append(args, "--pid="+s.Pid)
	}
	if expr := LogcatFilterExpr(s.minLevel); expr != "" {
		args = append(args, expr)
	}
	s.Cmd = hiddenCmd(s.ADBPath, args...)

	stdoutPipe, err := s.Cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法获取实时日志输出流: %w", err)
	}
	stderrPipe, err := s.Cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("无法获取实时日志错误流: %w", err)
	}

	if err := s.Cmd.Start(); err != nil {
		return fmt.Errorf("启动实时日志失败: %w", err)
	}

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	ui.Success("[%s] 实时日志已启动（最低级别 %s）", s.Address, s.minLevel)

	// 行通道：读取循环 → 聚合循环。容量 512 给读取侧留缓冲，
	// 短时日志洪峰时不丢行（聚合循环消费很快，正常不会触顶）
	lineCh := make(chan string, 512)

	// goroutine 1：逐行读取 stdout（阻塞读，进程结束时 Scan 返回 false）
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		// 单行缓冲放大到 1MB：与一键抓取一致，防超长行截断
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh) // 读到 EOF：进程结束，通知聚合循环收尾
	}()

	// goroutine 2：读取 stderr（仅留痕，实时日志的错误不影响主流程）
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			ui.Warn("[%s 实时日志 stderr] %s", s.Address, scanner.Text())
		}
	}()

	// goroutine 3：聚合推送 —— 200ms 定时器或攒够 200 行触发一次回调，
	// 把批内原始行统一解析后推送。逐行回调在高频日志下会产生事件风暴，
	// 批量化是本会话不卡界面的关键。
	// V1.7 调参：原 150ms/80 行对高频设备偏密（每秒 6~7 次 IPC + 前端布局），
	// 放宽到 200ms/200 行后事件频率降一半、单批更大更利于前端合帧渲染，
	// 而 200ms 的展示延迟对日志查看完全无感
	go func() {
		const (
			flushInterval = 200 * time.Millisecond // 聚合时间窗
			flushCount    = 200                    // 攒够行数立即触发
		)
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		var batch []LiveLogLine
		flush := func() {
			if len(batch) == 0 || s.onLines == nil {
				batch = batch[:0]
				return
			}
			// 拷贝一份交给回调（batch 缓冲继续复用，避免切片别名问题）
			out := make([]LiveLogLine, len(batch))
			copy(out, batch)
			s.onLines(out)
			batch = batch[:0]
		}

		for {
			select {
			case line, ok := <-lineCh:
				if !ok {
					flush() // 通道关闭：把最后一批推完再宣告结束
					s.finish("日志流已结束（设备断开或 adb 退出）")
					return
				}
				batch = append(batch, ParseLiveLogLine(line))
				if len(batch) >= flushCount {
					flush() // 攒满提前推，保证低延迟
				}
			case <-ticker.C:
				flush() // 定时推，保证低频日志也及时可见
			}
		}
	}()

	return nil
}

// finish 是会话结束的统一出口：标记停止 + 触发一次 onEnded。
// reason 会区分"用户主动停止"与"异常结束"，前端据此提示
func (s *LiveLogSession) finish(reason string) {
	s.mu.Lock()
	s.running = false
	userStopped := s.stopFlag
	s.mu.Unlock()

	s.endOnce.Do(func() {
		if s.onEnded != nil {
			if userStopped {
				s.onEnded("已停止")
			} else {
				s.onEnded(reason)
			}
		}
	})
}

// Stop 主动停止实时日志会话（幂等，可重复调用）。
// 杀掉子进程后读取/聚合循环自然退出，结束回调由 finish 触发
func (s *LiveLogSession) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return // 已停止（或从未启动）：幂等返回
	}
	s.stopFlag = true // 标记为用户主动停止
	s.mu.Unlock()

	if s.Cmd != nil && s.Cmd.Process != nil {
		s.Cmd.Process.Kill()
		s.Cmd.Wait()
	}
	s.finish("已停止")

	ui.Success("[%s] 实时日志已停止", s.Address)
}

// PidOf 查询指定应用包名当前运行的主进程号（实时日志"按包名过滤"的实现基础：
// logcat 只认 pid 不认包名——Android Studio 的 package: 过滤同样是先解析成
// pid 再加 --pid 参数过滤）。
// 入参:
//   - adbPath: adb 可执行文件路径
//   - serial:  设备序列号
//   - pkg:     应用包名
//
// 返回: (进程号, 错误)。进程号为空字符串 = 应用当前未运行（不是错误，
// 由上层决定如何提示）
func PidOf(adbPath, serial, pkg string) (string, error) {
	// pidof 是设备端快命令，5 秒超时兜底防设备异常挂住
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := hiddenCmdContext(ctx, adbPath, "-s", serial, "shell", "pidof", pkg)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("查询进程号失败: %w", err)
	}
	// pidof 可能返回多个 pid（多进程应用，空格分隔），取第一个；
	// 输出为空 = 应用未运行
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// Running 返回会话是否仍在运行（供上层查询状态）
func (s *LiveLogSession) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

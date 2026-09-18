//go:build windows

package cmdshell

// ============================================================================
// 文件名称 : cmdshell.go
// 功    能 : 底部面板「命令行模式」的执行内核：在本机以隐藏窗口方式执行
//            Windows cmd 命令（dir / type / echo / ping / ipconfig 等），
//            并维护会话级工作目录（cd 持久化——cmd /C 每次都是新进程，
//            不自己维护 cd 就永远停在初始目录）。
// 设计要点 : 1. cd / cd /d 命令不真正起进程，直接解析并更新内部目录
//            2. 输出编码自适应：先按 UTF-8 校验（原生 UTF-8 或已切代码页的
//               程序直通），非法则按 GBK 解码（中文 Windows 控制台默认编码）。
//               GBK 解码用 golang.org/x/text（本工程依赖树已有，非新增依赖）
//            3. 统一 10 秒超时：防 ping -t 之类的常驻命令把面板挂死
//            4. 子进程同样隐藏控制台窗口（复用 adb 包同款 CREATE_NO_WINDOW 方案）
// 安全说明 : 命令由本机使用者在界面输入、在本机执行——与用户自己开 cmd 等价，
//            不做命令白名单（白名单会砍掉 cmd 的基本可用性）。
// ============================================================================

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 命令执行超时：常驻命令（ping -t、交互式命令）到期被强制终止，
// 输出末尾会追加超时提示。10 秒对 dir/echo 类即时命令毫无影响
const commandTimeout = 10 * time.Second

// createNoWindow 是 Windows 进程创建标志 CREATE_NO_WINDOW (0x08000000)，
// 与 internal/adb/cmd_windows.go 同款：GUI 程序拉起控制台程序不弹黑窗。
// 此处独立维护一份（而非导出 adb 包的函数）：两个包职责不同，
// 且 cmdshell 不应反向依赖 adb 包
const createNoWindow = 0x08000000

// newHiddenCmd 创建隐藏窗口的 cmd.exe 命令对象（带超时上下文）
func newHiddenCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}

// ---------------------------- cd 命令解析（纯函数，有单测） ----------------------------

// ParseCd 判断一条命令是否是 cd 命令，并取出其路径参数。
// 支持形态：cd（无参=显示当前目录）/ cd <path> / cd /d <path>（跨盘切换）。
// 入参:
//   - command: 用户输入的完整命令行
//
// 返回: (路径参数, 是否 cd 命令)。路径参数去掉 cd 前缀与首尾引号/空格
func ParseCd(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	lower := strings.ToLower(trimmed)
	if lower == "cd" || lower == "cd." || lower == "cd\\" {
		return "", true // 无参 cd：由调用方返回当前目录
	}
	if strings.HasPrefix(lower, "cd /d ") {
		return stripQuotes(strings.TrimSpace(trimmed[len("cd /d"):])), true
	}
	if strings.HasPrefix(lower, "cd ") {
		return stripQuotes(strings.TrimSpace(trimmed[3:])), true
	}
	return "", false
}

// stripQuotes 去掉路径参数首尾的引号（cd "C:\Program Files" 场景）
func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// ResolveCwd 把 cd 的目标参数解析为绝对目录并校验存在。
// 支持绝对路径（含盘符）、相对路径、. 与 ..（filepath.Clean 统一处理）。
// 入参:
//   - cwd:    当前工作目录（绝对路径）
//   - target: cd 的路径参数
//
// 返回: (新的绝对目录, 错误)。目标不存在/不是目录时返回错误（cwd 不变）
func ResolveCwd(cwd, target string) (string, error) {
	target = stripQuotes(strings.TrimSpace(target))
	if target == "" || target == "." {
		return cwd, nil
	}

	var abs string
	if filepath.IsAbs(target) {
		abs = filepath.Clean(target)
	} else {
		abs = filepath.Clean(filepath.Join(cwd, target))
	}

	info, err := os.Stat(abs)
	if err != nil {
		return cwd, fmt.Errorf("系统找不到指定的路径: %s", target)
	}
	if !info.IsDir() {
		return cwd, fmt.Errorf("指定的是文件而不是目录: %s", target)
	}
	return abs, nil
}

// ---------------------------- 输出解码（纯函数，有单测） ----------------------------

// DecodeConsoleBytes 把命令输出字节解码为 UTF-8 文本。
// Windows 控制台程序输出编码不统一（GBK 常见，chcp 65001 后为 UTF-8），
// 策略：字节序列本身合法 UTF-8 则直通；否则按 GBK 解码；再失败原样返回。
// 入参:
//   - b: 命令的原始输出字节（stdout+stderr 合并）
//
// 返回: 解码后的文本
func DecodeConsoleBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b) // 本身就是 UTF-8：直通
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b) // 既非 UTF-8 又非 GBK：原样兜底（宁乱码不丢数据）
	}
	return string(decoded)
}

// ---------------------------- 命令执行主入口 ----------------------------

// RunCommand 在指定工作目录中执行一条 cmd 命令。
// 处理顺序：
//  1. cd 命令 → 解析并切换内部工作目录（不起进程）
//  2. 其余命令 → cmd.exe /C <command>（隐藏窗口，10 秒超时，stdout/stderr 合并）
//
// 命令的"业务失败"（如 dir 不存在的路径、exit code 非 0）不算 error：
// 输出文本本身就是结果，返回给前端展示即可；error 仅在无法启动进程时出现。
// 入参:
//   - cwd:     当前工作目录（绝对路径）
//   - command: 用户输入的完整命令行（空命令直接返回，不执行）
//
// 返回: (输出文本, 命令执行后的工作目录, 启动错误)
func RunCommand(cwd, command string) (string, string, error) {
	if strings.TrimSpace(command) == "" {
		return "", cwd, nil
	}

	// ---- cd 特殊处理：更新会话目录 ----
	if target, isCd := ParseCd(command); isCd {
		if target == "" {
			// 无参 cd：显示当前目录（与真实 cmd 行为一致）
			return cwd + "\r\n", cwd, nil
		}
		newCwd, err := ResolveCwd(cwd, target)
		if err != nil {
			return err.Error() + "\r\n", cwd, nil
		}
		return newCwd + "\r\n", newCwd, nil
	}

	// ---- 普通命令：cmd.exe /C 执行 ----
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := newHiddenCmd(ctx, "/C", command)
	cmd.Dir = cwd
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		return "", cwd, fmt.Errorf("命令启动失败: %w", err)
	}
	// 等待命令结束（超时由 CommandContext 自动杀进程，Wait 返回 kill 错误属预期）
	_ = cmd.Wait()

	text := DecodeConsoleBytes(out.Bytes())
	if ctx.Err() == context.DeadlineExceeded {
		// 超时被杀：输出保留已产生的部分，并明确告知原因
		text += "\r\n[命令超过 10 秒未结束，已被终止]"
	}
	return text, cwd, nil
}

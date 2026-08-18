//go:build windows

package adb

// ============================================================================
// 文件名称 : cmd_windows.go
// 功    能 : 提供统一的子进程命令构造入口 hiddenCmd。
//            本程序是 GUI 应用（无控制台），直接 exec.Command 拉起 adb.exe 等
//            控制台程序时，Windows 会为每个子进程弹出一个 cmd 黑窗，体验极差。
//            所有 adb 子进程必须经由 hiddenCmd 创建，统一隐藏控制台窗口。
// 依赖说明 : 本项目仅面向 Windows（注册表 PATH / WebView2），故直接用 windows 构建标签。
// ============================================================================

import (
	"context"
	"os/exec"
	"syscall"
)

// createNoWindow 是 Windows 进程创建标志 CREATE_NO_WINDOW (0x08000000)：
// 子进程为控制台程序时不创建新控制台窗口（也不会继承父窗口闪烁）。
const createNoWindow = 0x08000000

// hiddenCmd 创建一个不弹出控制台窗口的命令对象。
// 与 exec.Command 的唯一区别是附加了 SysProcAttr（隐藏窗口 + CREATE_NO_WINDOW），
// 其余行为（参数、管道、工作目录）完全一致。
// 入参:
//   - path: 可执行文件路径（如 adb 绝对路径）
//   - args: 命令行参数
//
// 返回: 配置好隐藏窗口属性的 *exec.Cmd
func hiddenCmd(path string, args ...string) *exec.Cmd {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}

// hiddenCmdContext 创建一个不弹出控制台窗口、且带超时取消能力的命令对象。
// 与 hiddenCmd 的区别仅在于关联了 context：
// 命令超过 ctx 的期限会被强制结束（ProcessState 记录为 killed），
// 用于 getprop 等可能因设备异常而长时间无响应的查询，避免一台设备拖垮整个列表。
// 入参:
//   - ctx:  控制命令生命周期的上下文（通常来自 context.WithTimeout）
//   - path: 可执行文件路径（如 adb 绝对路径）
//   - args: 命令行参数
//
// 返回: 配置好隐藏窗口属性的 *exec.Cmd
func hiddenCmdContext(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}

package main

// 本文件定义 Wails 前端可调用的绑定结构体 App。
// 前端通过 window.go.main.App.<方法名>(...) 调用这些导出方法。
// 每个导出方法对应一个常用 adb 操作，返回结果与错误，前端直接展示。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"adb-log-helper/internal/adb"
	"adb-log-helper/internal/cmdshell"
)

// App 是 Wails 应用的核心绑定结构体。
// 字段说明：
//   - ctx:      Wails 应用上下文，用于事件推送与文件对话框
//   - adbPath:  adb 可执行文件绝对路径（启动时检测/安装得到）
//   - session:  当前日志抓取会话（同一时间仅一个）
//   - logCancel: 日志抓取会话的取消函数
//   - liveSession: 当前实时日志会话（与 session 相互独立，互不影响）
//   - cmdCwd:   命令行模式的会话工作目录（cd 持久化用，初始 = exe 目录）
type App struct {
	ctx         context.Context
	adbPath     string
	session     *adb.LogcatSession
	logCancel   context.CancelFunc
	liveSession *adb.LiveLogSession
	cmdCwd      string
}

// NewApp 创建一个 App 实例。
func NewApp() *App {
	return &App{}
}

// startup 在 Wails 应用启动时被框架调用。
// 职责：
//  1. 保存应用上下文
//  2. 把工作目录切到 exe 所在目录，保证 logs/、adb-tools/ 等相对路径正确
//  3. 检测或安装 ADB，记录 adb 路径
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// 切换到可执行文件所在目录。
	// GUI 应用双击运行时 cwd 可能是任意目录，这里统一到 exe 目录，
	// 使日志目录、adb-tools 解压目录等相对路径保持一致。
	if exePath, err := os.Executable(); err == nil {
		_ = os.Chdir(filepath.Dir(exePath))
		// 命令行模式的初始工作目录与程序目录一致（与双击 cmd 打开的位置相同）
		if abs, err := filepath.Abs("."); err == nil {
			a.cmdCwd = abs
		}
	}

	// 启动时仅做检测，不再自动安装 ——
	// 未安装的场景由前端「ADB 环境检测」引导页接管（可视化安装流程）
	adbPath, found, _ := adb.Detect()
	if found {
		a.adbPath = adbPath
		runtime.LogInfo(ctx, fmt.Sprintf("ADB 就绪: %s", adbPath))
		return
	}
	runtime.LogInfo(ctx, "未检测到 ADB，等待前端引导页处理安装流程")
}

// RecheckAdb 重新检测本机 ADB 环境并更新内部状态。
// 供前端「ADB 环境检测」引导页使用：检测到则返回路径（非空），未检测到返回空字符串。
// 返回: ADB 可执行文件路径（空 = 未安装）
func (a *App) RecheckAdb() string {
	if adbPath, found, _ := adb.Detect(); found {
		a.adbPath = adbPath
	}
	return a.adbPath
}

// InstallAdbLocal 从本地 ZIP 安装包安装 ADB（可视化安装流程的入口之一）。
// 入参:
//   - zipPath: 安装包路径；空字符串表示自动在 exe 同目录查找支持的文件名
//
// 返回: 安装后的 ADB 路径与错误。成功后内部 adbPath 同步更新。
func (a *App) InstallAdbLocal(zipPath string) (string, error) {
	installedPath, err := adb.InstallFromZip(zipPath)
	if err != nil {
		return "", err
	}
	a.adbPath = installedPath
	return installedPath, nil
}

// InstallAdbOnline 联网从 Google 官方地址下载并安装 ADB。
// 下载进度通过 "adb-setup-progress" 事件推送给前端（percent/received/total）。
// 返回: 安装后的 ADB 路径与错误。成功后内部 adbPath 同步更新。
func (a *App) InstallAdbOnline() (string, error) {
	installedPath, err := adb.DownloadAndInstall(func(percent int, received, total int64) {
		// 把进度推送给引导页展示；total<=0 时前端按已下载字节数展示
		runtime.EventsEmit(a.ctx, "adb-setup-progress", map[string]interface{}{
			"percent":  percent,
			"received": received,
			"total":    total,
		})
	})
	if err != nil {
		return "", err
	}
	a.adbPath = installedPath
	return installedPath, nil
}

// SelectZipFile 弹出系统文件选择框（过滤 zip），用于手动指定 ADB 安装包。
// 返回: 选中的文件完整路径与错误
func (a *App) SelectZipFile() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择 ADB 安装包（zip）",
		Filters: []runtime.FileFilter{
			{DisplayName: "ADB 安装包 (*.zip)", Pattern: "*.zip"},
		},
	})
}

// shutdown 在应用关闭时被框架调用，负责清理日志抓取会话。
func (a *App) shutdown(ctx context.Context) {
	a.StopLogcat()
	a.StopLiveLog() // 实时日志会话一并清理（幂等，未启动时调用无害）
}

// ensureAdb 检查 ADB 是否已就绪，未就绪时返回友好错误。
func (a *App) ensureAdb() error {
	if a.adbPath == "" {
		return fmt.Errorf("ADB 未就绪，请将 platform-tools.zip 放到程序同目录后重启")
	}
	return nil
}

// GetAdbStatus 返回 ADB 可执行文件路径（空字符串表示未就绪）。
// 前端启动时调用，用于在界面上提示 ADB 状态。
func (a *App) GetAdbStatus() string {
	return a.adbPath
}

// GetDevices 返回当前已连接的设备列表（对应 adb devices）。
// 供设备下拉框的后台轮询使用，只取序列号与状态，开销小。
// 返回: 设备列表与错误。
func (a *App) GetDevices() ([]adb.Device, error) {
	if err := a.ensureAdb(); err != nil {
		return nil, err
	}
	return adb.Devices(a.adbPath)
}

// GetDevicesDetail 返回所有设备的完整信息（序列号/状态/连接方式/型号/安卓版本）。
// 供「查看设备列表」弹窗使用；比 GetDevices 多一轮每设备属性查询
// （并行执行、单台 4 秒超时），打开弹窗时调用一次即可。
// 返回: 设备完整信息列表与错误。
func (a *App) GetDevicesDetail() ([]adb.DeviceDetail, error) {
	if err := a.ensureAdb(); err != nil {
		return nil, err
	}
	return adb.DevicesDetail(a.adbPath)
}

// Connect 通过 TCP/IP 连接设备（对应 adb connect <ip:port>）。
// 这是所有 TCP 设备操作的前置步骤：设备必须先 connect 成功，
// 才会出现在设备下拉列表中。USB 设备无需此步骤。
// 入参:
//   - address: 设备地址，如 "192.168.1.100" 或 "192.168.1.100:5555"，
//     不带端口时后端自动补默认端口 5555
//
// 返回: adb connect 的输出（含 connected / already connected / 失败原因）与错误
func (a *App) Connect(address string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.Connect(a.adbPath, address)
}

// Disconnect 断开一台 TCP 设备（对应 adb disconnect <ip:port>）。
// 入参: 同 Connect 的 address。
// 返回: 命令输出与错误
func (a *App) Disconnect(address string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.Disconnect(a.adbPath, address)
}

// StartApp 启动设备上的指定应用（拉起到前台）。
// 后端策略：优先 am start（解析启动入口 Activity），旧系统自动回退 monkey 拉起。
// 入参:
//   - serial: 目标设备序列号
//   - pkg:    应用包名
//
// 返回: 启动命令输出与错误。
func (a *App) StartApp(serial, pkg string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.StartApp(a.adbPath, serial, pkg)
}

// ForceStop 强制停止设备上的指定应用（对应 adb shell am force-stop）。
// 立即杀掉应用进程，未保存的运行中状态丢失，磁盘数据不受影响。
// 入参:
//   - serial: 目标设备序列号
//   - pkg:    应用包名
//
// 返回: 命令输出与错误。
func (a *App) ForceStop(serial, pkg string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.ForceStop(a.adbPath, serial, pkg)
}

// ClearCache 清理指定应用的缓存（对应 adb shell pm clear）。
// 入参:
//   - serial: 目标设备序列号
//   - pkg:    应用包名
//
// 返回: 命令输出与错误。
func (a *App) ClearCache(serial, pkg string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.ClearCache(a.adbPath, serial, pkg)
}

// Screenshot 对设备截图并保存到指定目录（对应 adb exec-out screencap）。
// 入参:
//   - serial:  目标设备序列号
//   - saveDir: 截图保存目录
//
// 返回: 保存后的 PNG 文件完整路径与错误。
func (a *App) Screenshot(serial, saveDir string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.Screenshot(a.adbPath, serial, saveDir)
}

// InstallAPK 覆盖安装 APK（对应 adb install -r）。
// 入参:
//   - serial:  目标设备序列号
//   - apkPath: 本机 APK 文件绝对路径
//
// 返回: 安装命令输出与错误。
func (a *App) InstallAPK(serial, apkPath string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.InstallAPK(a.adbPath, serial, apkPath)
}

// Uninstall 从设备卸载指定应用（对应 adb uninstall）。
// 入参:
//   - serial: 目标设备序列号
//   - pkg:    应用包名
//
// 返回: 卸载命令输出与错误
func (a *App) Uninstall(serial, pkg string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.Uninstall(a.adbPath, serial, pkg)
}

// ListPackages 列出设备已安装包名（对应 adb shell pm list packages）。
// 入参:
//   - serial: 目标设备序列号
//   - filter: 过滤关键字，为空返回全部
//   - thirdPartyOnly: 为 true 时只列第三方应用（排除系统预装）
//
// 返回: 包名列表与错误。
func (a *App) ListPackages(serial, filter string, thirdPartyOnly bool) ([]string, error) {
	if err := a.ensureAdb(); err != nil {
		return nil, err
	}
	return adb.ListPackages(a.adbPath, serial, filter, thirdPartyOnly)
}

// Pull 从设备拉取文件/目录到本机（对应 adb pull）。
// 入参:
//   - serial: 目标设备序列号
//   - remote: 设备上的远程路径
//   - local:  本机目标路径
//
// 返回: 命令输出与错误。
func (a *App) Pull(serial, remote, local string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.Pull(a.adbPath, serial, remote, local)
}

// Push 把本机文件/目录推送到设备（对应 adb push）。
// 入参:
//   - serial: 目标设备序列号
//   - local:  本机文件/目录路径
//   - remote: 设备上的目标路径
//
// 返回: 命令输出与错误。
func (a *App) Push(serial, local, remote string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	return adb.Push(a.adbPath, serial, local, remote)
}

// SelectFile 弹出系统文件选择框，返回选中的文件完整路径。
// 用于「安装 APK」「push 选本地文件」等场景。
func (a *App) SelectFile() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择文件",
	})
}

// SelectDirectory 弹出系统目录选择框，返回选中的目录完整路径。
// 用于「截图保存目录」「pull 本地目录」等场景。
func (a *App) SelectDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择目录",
	})
}

// StartLogcat 开始对指定设备抓取日志（一键式，无实时终端输出）。
// 日志写入文件保存，界面上只展示"抓取中/文件路径"状态，面向非技术用户。
// 入参:
//   - serial: 目标设备序列号
//   - saveDir: 日志保存根目录；空字符串表示默认位置（程序目录下 logs/）
//
// 返回: 本次抓取的日志文件完整路径与错误。
func (a *App) StartLogcat(serial, saveDir string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}

	// 若已有会话，先停止
	a.StopLogcat()

	// 派生一个可取消的 context，用于停止日志抓取
	ctx, cancel := context.WithCancel(a.ctx)
	a.logCancel = cancel

	// 创建会话并指定日志保存目录（空则由会话内部使用默认 logs/ 目录）
	session := adb.NewLogcatSession(a.adbPath, serial)
	session.SaveDir = saveDir

	if err := session.Start(ctx); err != nil {
		// 启动失败，释放取消函数
		cancel()
		a.logCancel = nil
		return "", err
	}

	a.session = session
	// 把日志文件路径返回给前端，用于状态展示与用户定位文件
	return session.LogPath, nil
}

// StopLogcat 停止当前日志抓取会话（幂等，可重复调用）。
func (a *App) StopLogcat() {
	if a.logCancel != nil {
		a.logCancel()
		a.logCancel = nil
	}
	if a.session != nil {
		a.session.Stop()
		a.session = nil
	}
}

// StartLiveLog 启动指定设备的实时日志推送（参考 Android Studio Logcat）。
// 与一键抓取（StartLogcat 落文件）完全独立，两个会话可同时运行。
// 日志行通过 Wails 事件 "live-log-lines" 批量推送给前端（每批为数组，
// 后端已按 200ms/200 行聚合，前端直接整批渲染）；
// 会话结束（停止/设备断开）时推送 "live-log-ended" 事件（携带原因）。
// 入参:
//   - serial:   目标设备序列号
//   - minLevel: 最低日志级别（V/D/I/W/E/F；非法值按 V 全量处理）
//   - pkg:      包名过滤（参考 AS 的 package: 过滤）。非空时先查该应用的
//               进程号并以 --pid= 过滤——只看这个应用的日志；
//               应用未运行时返回友好错误提示
//
// 返回: 启动失败错误
func (a *App) StartLiveLog(serial, minLevel, pkg string) error {
	if err := a.ensureAdb(); err != nil {
		return err
	}

	// 若已有实时会话，先停止（切换设备/切换级别/改包名都会走到这里）
	a.StopLiveLog()

	// 包名过滤：先解析成 pid（logcat 不认识包名，AS 同样是 pid 方案）
	pid := ""
	if pkg != "" {
		p, err := adb.PidOf(a.adbPath, serial, pkg)
		if err != nil {
			return err
		}
		if p == "" {
			// 应用未运行：给出可操作的提示（AS 的 package:mine 同样要求进程活着）
			return fmt.Errorf("包名 %s 当前没有运行中的进程，请先启动该应用（或清空包名过滤）", pkg)
		}
		pid = p
	}

	// 创建会话：批量行 → 事件推前端；结束 → 事件通知前端
	session := adb.NewLiveLogSession(a.adbPath, serial, minLevel,
		// 批量行回调：把一批结构化日志行推给前端渲染
		func(lines []adb.LiveLogLine) {
			runtime.EventsEmit(a.ctx, "live-log-lines", lines)
		},
		// 结束回调：把结束原因推给前端（用户主动停止/设备断开/adb 退出）
		func(reason string) {
			runtime.EventsEmit(a.ctx, "live-log-ended", map[string]interface{}{
				"reason": reason,
			})
		},
	)
	session.Pid = pid // 非空时 logcat 附加 --pid=<pid>

	if err := session.Start(); err != nil {
		return err
	}

	a.liveSession = session
	return nil
}

// StopLiveLog 停止当前实时日志会话（幂等，可重复调用）。
// 前端关闭开关/切换设备/切换级别时调用
func (a *App) StopLiveLog() {
	if a.liveSession != nil {
		a.liveSession.Stop()
		a.liveSession = nil
	}
}

// IsLiveLogRunning 返回实时日志会话是否正在运行。
// 前端打开面板前检查，避免重复启动
func (a *App) IsLiveLogRunning() bool {
	return a.liveSession != nil && a.liveSession.Running()
}

// RunCmd 执行一条 Windows cmd 命令并返回输出（底部面板「命令行模式」入口）。
// 会话级 cd 持久化：内部维护 cmdCwd，cd 命令更新它，其余命令在其下执行。
// 命令的"业务失败"（非零退出码/找不到文件）不算 error——输出文本即结果；
// error 仅表示命令无法启动（极少发生）。
// 入参:
//   - command: 用户输入的完整命令行（空命令直接返回空输出）
//
// 返回: 命令输出文本与错误
func (a *App) RunCmd(command string) (string, error) {
	if a.cmdCwd == "" {
		// 兜底：startup 未走到（理论不可达）时用当前工作目录
		if abs, err := filepath.Abs("."); err == nil {
			a.cmdCwd = abs
		}
	}
	output, newCwd, err := cmdshell.RunCommand(a.cmdCwd, command)
	if err != nil {
		return "", err
	}
	a.cmdCwd = newCwd // cd 成功时被 RunCommand 更新（失败时保持原值）
	return output, nil
}

// GetCmdCwd 返回命令行模式的当前工作目录（前端显示提示符 "C:\path>" 用）
func (a *App) GetCmdCwd() string {
	if a.cmdCwd == "" {
		if abs, err := filepath.Abs("."); err == nil {
			a.cmdCwd = abs
		}
	}
	return a.cmdCwd
}

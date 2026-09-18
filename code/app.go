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
//   - recSession: 当前录屏会话（同一时间仅一个，与日志抓取互斥）
//   - recCancel: 录屏会话的取消函数
//   - liveSession: 当前实时日志会话（与 session 相互独立，互不影响）
//   - cmdCwd:   命令行模式的会话工作目录（cd 持久化用，初始 = exe 目录）
//   - history:  设备连接历史跟踪器（跟随设备列表刷新记录最近断开的设备）
type App struct {
	ctx         context.Context
	adbPath     string
	session     *adb.LogcatSession
	logCancel   context.CancelFunc
	recSession  *adb.RecordSession
	recCancel   context.CancelFunc
	liveSession *adb.LiveLogSession
	cmdCwd      string
	history     *adb.DeviceHistory
}

// NewApp 创建一个 App 实例。
func NewApp() *App {
	return &App{history: adb.NewDeviceHistory()}
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

// shutdown 在应用关闭时被框架调用，负责清理各类会话。
func (a *App) shutdown(ctx context.Context) {
	a.StopLogcat()
	a.StopLiveLog() // 实时日志会话一并清理（幂等，未启动时调用无害）
	// 录屏会话：仅终止本机 adb 进程（取消 ctx），**不等待回传**——
	// shutdown 需要快速返回，完整「停止→回传→校验」流程可能包含多次
	// adb 调用与最长 15 秒等待，会显著拖慢应用退出。设备端 screenrecord
	// 失去 adb 连接后自行退出（time-limit 兜底），残留文件由下次录屏前的
	// CleanStaleRemote 清理；本次视频因未正常收尾不可回传，属可接受损失。
	a.abortScreenRecord()
}

// abortScreenRecord 快速终止录屏会话（仅杀本机 adb 进程，不回传不等待）。
// 仅供应用退出路径使用；日常停止走 StopScreenRecord（完整回传流程）。
func (a *App) abortScreenRecord() {
	if a.recCancel != nil {
		a.recCancel() // 取消会话 ctx → hiddenCmdContext 终止本机 adb 子进程
		a.recCancel = nil
	}
	a.recSession = nil
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
// 同时把结果喂给历史跟踪器，自动记录"连接过又断开"的设备。
// 返回: 设备列表与错误。
func (a *App) GetDevices() ([]adb.Device, error) {
	if err := a.ensureAdb(); err != nil {
		return nil, err
	}
	devices, err := adb.Devices(a.adbPath)
	if err == nil {
		a.updateHistory(devices)
	}
	return devices, err
}

// updateHistory 把一次设备列表查询结果喂给历史跟踪器。
// 在线设备刷新最后在线时间并清除断开标记（重连后立即从
// 「最近断开的设备」中消失），消失设备记一次断开时间。
// 入参: devices 当前查到的设备列表
func (a *App) updateHistory(devices []adb.Device) {
	serials := make([]string, 0, len(devices))
	for _, d := range devices {
		serials = append(serials, d.Serial)
	}
	a.history.Update(serials)
}

// GetRecentDevices 返回最近连接过但当前已断开的设备记录（最多 3 条，最新在前）。
// 数据来自历史跟踪器（持久化在程序目录 device_history.json，重启不丢）。
// 供「查看设备列表」弹窗的历史区展示。
// 返回: 历史记录列表与错误。
func (a *App) GetRecentDevices() ([]adb.HistoryEntry, error) {
	return a.history.Recent(), nil
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
	out, err := adb.Connect(a.adbPath, address)
	if err == nil {
		// 连接成功后立即用最新设备列表刷新历史：重连设备马上清除断开
		// 标记，无需等 3 秒轮询（否则弹窗里「最近断开的设备」短暂残留）
		if devices, devErr := adb.Devices(a.adbPath); devErr == nil {
			a.updateHistory(devices)
		}
	}
	return out, err
}

// Disconnect 断开一台 TCP 设备（对应 adb disconnect <ip:port>）。
// 入参: 同 Connect 的 address。
// 返回: 命令输出与错误
func (a *App) Disconnect(address string) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}
	out, err := adb.Disconnect(a.adbPath, address)
	if err == nil {
		// 主动断开立即记入历史（不等轮询防抖），断开后弹窗刷新即见
		a.history.MarkDisconnected(address)
	}
	return out, err
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

// StartScreenRecord 开始对指定设备录屏（对应 adb shell screenrecord，V1.0.1 新增）。
// 与一键日志抓取互斥：开始录屏前若日志抓取在进行中则报错（两者共用设备
// shell 通道，并行会互相干扰）；实时日志/命令行模式不受影响。
// 录制期间前端每秒轮询 IsRecording/录屏秒数展示计时；单段最长 3 分钟，
// 到时设备端自动结束（--time-limit 兜底），前端停止时仍可正常回传。
// 入参:
//   - serial:  目标设备序列号
//   - saveDir: 视频保存目录；空字符串表示默认位置（程序目录下 videos/）
//
// 返回: 启动失败错误（设备不支持 screenrecord / 录屏或抓取已在进行中）
func (a *App) StartScreenRecord(serial, saveDir string) error {
	if err := a.ensureAdb(); err != nil {
		return err
	}

	// 与一键日志抓取互斥
	if a.session != nil {
		return fmt.Errorf("日志抓取进行中，请先结束抓取再开始录屏")
	}
	// 已有录屏会话：直接拒绝（前端「开始/停止」同位互斥按钮保证正常流程
	// 不会走到；并发/异常场景宁可报错，也不静默丢弃上一段视频——
	// 旧版在这里幂等预停止会「静默回传上段视频且用户永远看不到路径」）
	if a.recSession != nil {
		return fmt.Errorf("已有录屏会话进行中，请先停止当前录屏")
	}

	// 预清理设备端历史残留（上次异常退出未回传的临时文件）
	cleaner := adb.NewRecordSession(a.adbPath, serial)
	cleaner.CleanStaleRemote()

	// 派生可取消 context：应用退出时通知会话异常终止
	ctx, cancel := context.WithCancel(a.ctx)
	session := adb.NewRecordSession(a.adbPath, serial)
	session.SaveDir = saveDir

	if err := session.Start(ctx); err != nil {
		cancel()
		return err
	}

	a.recSession = session
	// cancel 交由 StopScreenRecord 释放；会话活跃期间由结构体持有
	a.recCancel = cancel

	return nil
}

// StopScreenRecord 停止当前录屏并回传视频到本机（幂等，无会话时无害）。
// 入参:
//   - saveDir: 视频保存目录（与 StartScreenRecord 一致；留空用会话已保存的目录）
//
// 返回: 本机视频文件完整路径与错误
func (a *App) StopScreenRecord() (string, error) {
	if a.recSession == nil {
		return "", nil
	}
	path, err := a.recSession.Stop()
	if a.recCancel != nil {
		a.recCancel()
		a.recCancel = nil
	}
	a.recSession = nil
	return path, err
}

// AbortScreenRecord 立即放弃当前录屏会话（取消会话 ctx 终止本机 adb 进程，
// 不做停止信号与回传）。供设备断开/切换场景：设备已不可达，完整停止+回传
// 流程必然失败且阻塞界面清理链；设备端残留由下次录屏前的 CleanStaleRemote 清理。
func (a *App) AbortScreenRecord() {
	if a.recSession == nil {
		return
	}
	// 触发会话监听 goroutine 的自然结束路径：ctx 取消 → adb 子进程被终止
	// → Wait 返回 → phase 已非 Recording（下面立刻改）→ goroutine 不自动回传。
	// 但 Stop 若被并发调用也无害（幂等保护）；这里直接取消并清会话即可。
	a.abortScreenRecord()
}

// RecordState 是录屏会话状态（IsRecording 的单返回值载体）。
// Wails v2.11 绑定仅支持 1~2 个返回值（boundMethod.Call 无 3 值分支，
// 3 值会静默 marsh 成 null），多字段状态必须打包为 struct。
type RecordState struct {
	// Recording 是否处于录制中
	Recording bool `json:"recording"`
	// Elapsed 已录制秒数（非录制态为 0）
	Elapsed int `json:"elapsed"`
	// Phase 状态字（idle/recording/stopping/finished/error）
	Phase string `json:"phase"`
}

// IsRecording 返回录屏会话状态（前端每秒轮询，驱动计时与按钮互斥）。
// 返回单 struct（Wails 绑定 1 值返回，安全 marshal）。
func (a *App) IsRecording() RecordState {
	if a.recSession == nil {
		return RecordState{
			Recording: false,
			Elapsed:   0,
			Phase:     adb.RecordIdle.String(),
		}
	}
	phase, _ := a.recSession.Phase()
	return RecordState{
		Recording: phase == adb.RecordRecording,
		Elapsed:   a.recSession.ElapsedSec(),
		Phase:     phase.String(),
	}
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
//   - clearBefore: 为 true 时先执行 adb logcat -c 清空设备端日志缓冲，
//     本次抓取只记录新产生的日志（不含启动前的旧日志）
//
// 返回: 本次抓取的日志文件完整路径与错误。
func (a *App) StartLogcat(serial, saveDir string, clearBefore bool) (string, error) {
	if err := a.ensureAdb(); err != nil {
		return "", err
	}

	// 与录屏互斥（录屏进行中共用设备 shell 通道，并行互相干扰）
	if a.recSession != nil {
		return "", fmt.Errorf("录屏进行中，请先停止录屏再开始抓取")
	}

	// 可选的前置清空：失败则中止抓取并返回原因（缓冲未清成功，
	// 抓出来的日志会混入旧日志，与用户勾选的预期不符）
	if clearBefore {
		if _, err := adb.ClearLogcat(a.adbPath, serial); err != nil {
			return "", fmt.Errorf("清空设备日志缓冲失败: %w", err)
		}
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
//     进程号并以 --pid= 过滤——只看这个应用的日志；
//     应用未运行时返回友好错误提示
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

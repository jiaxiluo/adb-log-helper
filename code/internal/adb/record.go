package adb

// ============================================================================
// 文件名称 : record.go
// 功    能 : 设备录屏会话（对应 adb shell screenrecord，V1.0.1 新增）。
//            screenrecord 无法像 screencap 一样经 exec-out 直接取回视频流，
//            采用「录制 → 停止 → 回传」两段式：
//              阶段一  adb -s <serial> shell screenrecord --time-limit 180 /sdcard/xxx.mp4
//                      （--time-limit 兜底：用户忘记停止时到时自动结束并生成完整文件）
//              阶段二  停止（发 SIGINT 生成完整 mp4 索引）→ adb pull 回传本机 →
//                      校验 mp4 头（ftyp box）→ 删除设备端临时文件
// 说明    : 会话生命周期由上层（app.go）持有；并发安全；同一时间仅允许一个会话。
// ============================================================================

import (
	"bytes"
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

// 录屏相关常量（集中定义，避免魔鬼数字）。
const (
	// RecordMaxDurationSec 单段录制的最大时长（秒）。
	// Android screenrecord 硬上限为 3 分钟，到时自动结束并正常生成完整文件。
	RecordMaxDurationSec = 180

	// recordRemotePrefix 设备端临时录像文件名前缀
	recordRemotePrefix = "adbhelper_record_"

	// recordRemoteDir 设备端临时录像文件所在目录
	recordRemoteDir = "/sdcard"

	// ftypMagic 是有效 MP4 文件头前 12 字节中必然出现的 box 类型标识。
	// 录制被强杀时文件缺 moov 索引，但 ftyp 一定已写入；若连 ftyp 都没有，
	// 说明回传根本没成功，文件不可播放。
	ftypMagic = "ftyp"

	// mp4HeaderLen 校验 mp4 头时读取的头部字节数（足够覆盖 ftyp box）。
	mp4HeaderLen = 64

	// recordCmdTimeoutSec 录屏会话内单条 adb 命令（停止信号/回传/清理）的超时秒数。
	// 设备掉线等异常时命令可能永久挂死，超时终止防止 Stop 永不返回
	// （回传大视频时 pull 可能较慢，取宽裕值）。
	recordCmdTimeoutSec = 60

	// recordWaitAfterStopSec 停止信号发出后等待本机 adb 进程退出的秒数。
	// 超时则强杀本机 adb；此时设备端文件很可能未正常收尾（缺 moov），
	// 由「校验 + 不删设备端文件」策略兜底（见 pullAndValidate 的 timedOut 返回）。
	recordWaitAfterStopSec = 15
)

// RecordPhase 描述录屏会话所处的阶段（状态机，供前端展示与测试断言）。
type RecordPhase int

// 录屏会话状态机：Idle → Recording → Stopping → Finished/Error。
const (
	RecordIdle      RecordPhase = iota // 未在录制
	RecordRecording                    // 设备端录制中
	RecordStopping                     // 正在停止并回传（此时不可再停止）
	RecordFinished                     // 已完成（视频已落本机）
	RecordError                        // 出错（Err 携带原因）
)

// String 实现 fmt.Stringer，便于日志输出与测试断言。
func (p RecordPhase) String() string {
	switch p {
	case RecordIdle:
		return "idle"
	case RecordRecording:
		return "recording"
	case RecordStopping:
		return "stopping"
	case RecordFinished:
		return "finished"
	case RecordError:
		return "error"
	default:
		return "unknown"
	}
}

// RecordSession 管理单个设备的录屏会话。
// 字段说明：
//   - Address:  adb -s 使用的设备标识（USB 序列号或 ip:port）
//   - ADBPath:  adb 可执行文件路径
//   - SaveDir:  视频保存目录；空字符串表示默认位置（程序目录下 videos/）
//   - runner:   adb 命令执行器（默认真实执行，测试注入 fake）
//   - spawner:  长驻进程启动器（默认真实执行，测试注入 fake）
//   - phase 及其余字段: 运行期状态，由 Start/Stop 维护，mu 保护并发访问
type RecordSession struct {
	Address string
	ADBPath string
	SaveDir string

	// runner 抽象了"执行一条 adb 命令"的能力，生产环境为真实 adb，
	// 单元测试注入 fake（RecordRunnerFunc）以离线验证状态机与命令拼装。
	runner RecordRunner
	// spawner 抽象了"启动长驻 screenrecord 进程"的能力（同上可注入）。
	spawner RecordSpawner

	mu         sync.Mutex
	phase      RecordPhase
	errText    string          // phase == RecordError 时的原因
	ctx        context.Context // 会话取消信号（Start 时注入）
	cmd        *exec.Cmd       // 阶段一的 screenrecord 子进程（注入测试可为 nil）
	waitDone   chan error      // 广播子进程退出：Wait 仅在监听 goroutine 调用一次，Stop 经此通道取结果
	videoPath  string          // 完成后的本机视频路径（ Finished 时有效）
	remotePath string          // 设备端临时录像路径（清理残留用）
	startAt    time.Time       // 录制开始时刻（计时展示用）
}

// RecordRunner 抽象"执行一条 adb 命令并等它结束"（run 的最小接口，便于测试注入）。
// 返回值语义与 run 一致：output 为组合输出文本，err 非空表示命令失败。
// ctx 提供超时/取消保护：设备异常导致 pull 等命令挂死时自动终止，防止 Stop 永不返回。
type RecordRunner interface {
	Run(ctx context.Context, adbPath string, args ...string) (string, error)
}

// RecordRunnerFunc 适配函数为 RecordRunner。
type RecordRunnerFunc func(ctx context.Context, adbPath string, args ...string) (string, error)

// Run 实现 RecordRunner。
func (f RecordRunnerFunc) Run(ctx context.Context, adbPath string, args ...string) (string, error) {
	return f(ctx, adbPath, args...)
}

// RecordSpawner 抽象"启动一条长驻命令并立即返回"（阶段一 screenrecord 用）。
// 与 RecordRunner 的区别：不等待进程结束，返回启动好的 Cmd 供后续 Wait/停止。
type RecordSpawner interface {
	// Spawn 启动长驻进程；返回 Cmd 供 Stop 阶段 Wait。
	// ctx 关联到进程生命周期：取消时子进程被终止（应用退出场景）。
	// spawned 为 false 表示注入方不产出真实 Cmd（单元测试场景），
	// 此时返回 nil 即可，Stop 阶段跳过进程等待。
	Spawn(ctx context.Context, adbPath string, args ...string) (cmd *exec.Cmd, spawned bool, err error)
}

// recordSpawner 生产环境的启动器：hiddenCmdContext 创建隐藏窗口命令并 Start。
type recordSpawner struct{}

// Spawn 实现 RecordSpawner。
func (recordSpawner) Spawn(ctx context.Context, adbPath string, args ...string) (*exec.Cmd, bool, error) {
	cmd := hiddenCmdContext(ctx, adbPath, args...)
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	return cmd, true, nil
}

// realRunner 生产环境的执行器：带超时取消能力的隐藏窗口命令（hiddenCmdContext）。
func realRunner(ctx context.Context, adbPath string, args ...string) (string, error) {
	cmd := hiddenCmdContext(ctx, adbPath, args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("%s: %w", text, err)
	}
	return text, nil
}

// NewRecordSession 创建录屏会话（默认真实 runner/spawner；测试用 newRecordSessionWithRunner 注入）。
func NewRecordSession(adbPath, address string) *RecordSession {
	return newRecordSessionWithRunner(adbPath, address, RecordRunnerFunc(realRunner))
}

// newRecordSessionWithRunner 创建指定 runner 的会话，spawner 用生产实现
// （Start 里的长驻进程启动仍在 StartNoop 开关下跳过——见 spawnerNoop 注释）。
func newRecordSessionWithRunner(adbPath, address string, r RecordRunner) *RecordSession {
	return &RecordSession{
		ADBPath: adbPath,
		Address: address,
		runner:  r,
		spawner: recordSpawner{},
	}
}

// setSpawner 注入自定义长驻进程启动器（单元测试用）。
func (s *RecordSession) setSpawner(sp RecordSpawner) {
	s.spawner = sp
}

// Start 进入录制阶段（阶段一）。
// 命令: adb -s <serial> shell screenrecord --time-limit 180 /sdcard/adbhelper_record_<时间戳>.mp4
// 成功后立即返回（录制在设备端后台进行）；停止/回传由 Stop 驱动。
// 入参:
//   - ctx: 会话取消信号。应用退出时上层取消该 ctx，关联的 screenrecord
//     子进程被强制终止（hiddenCmdContext），设备端由 time-limit 兜底自愈
//
// 返回: 启动失败时的错误（设备不支持 screenrecord / 设备离线等）
func (s *RecordSession) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.phase != RecordIdle {
		return fmt.Errorf("录屏会话状态异常（%s），请先停止当前会话", s.phase)
	}
	s.ctx = ctx

	// 生成设备端临时文件路径与本地保存路径（时间戳精确到秒，同秒重复录制由
	// 前端「开始/停止」互斥按钮天然避免；即使重名，pull 覆盖也不产生坏文件）
	stamp := time.Now().Format("20060102_150405")
	s.remotePath = fmt.Sprintf("%s/%s%s.mp4", recordRemoteDir, recordRemotePrefix, stamp)

	// 保存目录：用户指定则用之，否则默认程序目录下 videos/
	saveDir := s.SaveDir
	if saveDir == "" {
		saveDir = filepath.Join(".", "videos")
	}
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return fmt.Errorf("创建录屏目录失败: %w", err)
	}

	// 启动设备端录制（长驻进程，退出码非 0 不代表失败——用户停止/SIGINT/
	// time-limit 到时都会以非 0 退出，故仅检查能否成功拉起进程）。
	// ctx 关联到子进程：应用退出取消 ctx 时 adb 进程被终止，
	// 设备端 screenrecord 失去连接后自行退出，由 time-limit 保证最终自愈。
	cmd, spawned, err := s.spawner.Spawn(ctx, s.ADBPath,
		"-s", s.Address, "shell", "screenrecord",
		"--time-limit", fmt.Sprintf("%d", RecordMaxDurationSec),
		s.remotePath)
	if err != nil {
		return fmt.Errorf("启动录屏失败（设备可能不支持 screenrecord，需 Android 4.4+）: %w", err)
	}
	if spawned {
		s.cmd = cmd
	}
	s.startAt = time.Now()
	s.phase = RecordRecording

	// 自然结束监听：--time-limit 到时（或设备端异常退出）时，本机 adb 进程
	// 会自行退出；此时用户还没点「停止录屏」，必须由会话自己完成收尾
	// （停止信号→回传→校验），否则 phase 永远停在 Recording、视频不落盘。
	//
	// Wait 归属设计：exec.Cmd.Wait 不允许并发调用，整个会话中 Wait 只在此
	// goroutine 调用一次，结果通过 s.waitDone 通道广播：
	//   · 本 goroutine：Wait 返回后若 phase 仍为 Recording ⇒ 自然结束，
	//     转入 Stop() 完成回传
	//   · Stop()：不再直接 Wait，而是从 s.waitDone 取结果（带超时，
	//     超时强杀进程——Wait 随 Kill 返回并写入通道）
	if spawned {
		s.waitDone = make(chan error, 1)
		waitDone := s.waitDone
		go func() {
			waitDone <- cmd.Wait() // 会话内唯一一次 Wait
			s.mu.Lock()
			natural := s.phase == RecordRecording
			s.mu.Unlock()
			if natural {
				ui.Warn("[%s] 设备端录制已到时自动结束（%d 秒上限），正在回传视频…",
					s.Address, RecordMaxDurationSec)
				if _, err := s.Stop(); err != nil {
					ui.Warn("[%s] 自动回传失败: %v", s.Address, err)
				}
			}
		}()
	}

	ui.Success("[%s] 录屏已开始: %s", s.Address, s.remotePath)
	return nil
}

// ElapsedSec 返回已录制秒数（录制中有效；其他阶段返回 0）。
// 前端每秒轮询展示 mm:ss 计时。
func (s *RecordSession) ElapsedSec() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != RecordRecording {
		return 0
	}
	return int(time.Since(s.startAt).Seconds())
}

// Phase 返回当前状态（含错误原因），并发安全。
func (s *RecordSession) Phase() (RecordPhase, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase, s.errText
}

// Stop 结束录制并完成回传（阶段二：停止 → pull → 校验 → 清理，幂等）。
// 状态流转 Recording → Stopping → Finished / Error。
//
// 返回: 本机视频文件完整路径与错误
func (s *RecordSession) Stop() (string, error) {
	s.mu.Lock()

	// 幂等：非录制态直接返回既有结果/错误
	switch s.phase {
	case RecordFinished:
		path := s.videoPath
		s.mu.Unlock()
		return path, nil
	case RecordError:
		err := fmt.Errorf("%s", s.errText)
		s.mu.Unlock()
		return "", err
	case RecordStopping:
		s.mu.Unlock()
		return "", fmt.Errorf("正在停止中，请稍候")
	case RecordIdle:
		s.mu.Unlock()
		return "", fmt.Errorf("当前没有进行中的录屏")
	}

	// Recording → Stopping（先改状态再放锁，避免并发 Stop 双跑回传）
	s.phase = RecordStopping
	cmd := s.cmd
	waitDone := s.waitDone
	remote := s.remotePath
	s.mu.Unlock()

	// ① 向设备端 screenrecord 进程发送停止信号（SIGINT）。
	//    直接 Kill 本机 adb 进程无法让设备端正常收尾（mp4 缺 moov 索引不可播放），
	//    必须让设备端进程收到 SIGINT 走正常退出流程。
	finalized := s.stopDeviceRecording()

	// ② 等待本机 adb 进程退出（设备端 screenrecord 结束后 adb shell 才返回）。
	//    Wait 由 Start 的监听 goroutine 独占调用，这里只从 waitDone 通道取结果。
	//    超时则强杀本机 adb（Wait 随 Kill 返回写入通道）；此情形设备端文件很可能
	//    未正常收尾（缺 moov 索引），finalized 置 false——回传校验失败时不再删除
	//    设备端文件（保留可恢复副本）。cmd 为 nil（单测注入）时跳过等待。
	if cmd != nil && waitDone != nil {
		select {
		case <-waitDone:
		case <-time.After(recordWaitAfterStopSec * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitDone
			finalized = false
		}
	}

	// ③ 回传到本机（finalized=false 且校验不通过时保留设备端副本）
	localPath, err := s.pullAndValidate(remote, finalized)
	if err != nil {
		// 回传失败：仅当确认正常收尾时才清理设备端残留后报错；
		// 未确认收尾时保留设备端文件（用户可手动 adb pull 补救）
		if finalized {
			s.removeRemoteQuietly(remote)
		}
		s.mu.Lock()
		s.phase = RecordError
		s.errText = err.Error()
		s.mu.Unlock()
		return "", err
	}

	// ④ 清理设备端临时文件（失败不报错——不占用用户空间即可，下次录屏
	//    开始前也会再清一次同前缀残留）
	s.removeRemoteQuietly(remote)

	s.mu.Lock()
	s.phase = RecordFinished
	s.videoPath = localPath
	s.mu.Unlock()

	ui.Success("[%s] 录屏已保存: %s", s.Address, localPath)
	return localPath, nil
}

// stopDeviceRecording 向设备端 screenrecord 进程发送停止信号（SIGINT），
// 返回是否确认发送成功（用于决定回传失败时是否可清理设备端副本）。
//
// 方案选择依据（toybox 语义核对）：
//   - 方案1 pidof+kill（首选，最通用）：pidof 是 toybox/procps 通用命令，
//     kill -INT 是标准信号语法。单条 shell 复合命令在本机无 shell 解释，
//     故用 "sh -c" 包装让设备端 shell 展开 $(...)。
//   - 方案2 pkill -INT（备选）：部分新系统有 pkill；-SIGNAL（-INT）是
//     killall/pkill 通用的「发信号」语法（注意 -l 在部分实现里是 list 模式
//     而非发信号，不可用 -l2 这种写法）。
//   - 方案3 killall -INT（兜底）：toybox killall 同样接受 -SIGNAL。
//
// 全部失败时返回 false——由 --time-limit 兜底结束；回传校验失败时
// 不删除设备端文件（保留可恢复副本）。
func (s *RecordSession) stopDeviceRecording() bool {
	s.mu.Lock()
	adbPath := s.ADBPath
	serial := s.Address
	s.mu.Unlock()

	// 方案1：pidof 定位进程号 + kill -INT（sh -c 让设备端展开 $()）
	ctx1, cancel1 := context.WithTimeout(context.Background(), recordCmdTimeoutSec*time.Second)
	out, err := s.runner.Run(ctx1, adbPath, "-s", serial, "shell",
		"sh", "-c", "kill -INT $(pidof screenrecord)")
	cancel1()
	if err == nil {
		ui.Warn("[%s] 停止信号已发送（pidof+kill）: %s", serial, strings.TrimSpace(out))
		return true
	}
	// 方案2：pkill -INT（注意不是 -l2：-l 在部分实现里是 list 模式）
	ctx2, cancel2 := context.WithTimeout(context.Background(), recordCmdTimeoutSec*time.Second)
	out, err = s.runner.Run(ctx2, adbPath, "-s", serial, "shell",
		"pkill", "-INT", "-f", "screenrecord")
	cancel2()
	if err == nil {
		ui.Warn("[%s] 停止信号已发送（pkill）: %s", serial, strings.TrimSpace(out))
		return true
	}
	// 方案3：killall -INT
	ctx3, cancel3 := context.WithTimeout(context.Background(), recordCmdTimeoutSec*time.Second)
	out, err = s.runner.Run(ctx3, adbPath, "-s", serial, "shell",
		"killall", "-INT", "screenrecord")
	cancel3()
	if err == nil {
		ui.Warn("[%s] 停止信号已发送（killall）: %s", serial, strings.TrimSpace(out))
		return true
	}
	ui.Warn("[%s] 三种停止命令均未成功，等待 time-limit 到时自动结束", serial)
	return false
}

// pullAndValidate 把设备端录像回传本机并校验 mp4 头。
// 入参:
//   - remote:    设备端录像路径
//   - finalized: 停止信号是否确认成功。false（信号全失败或等待超时强杀）时
//     校验不通过不再删除设备端文件——保留可恢复副本
//
// 返回: 本机文件完整路径与错误（校验失败时删除本地坏文件）
func (s *RecordSession) pullAndValidate(remote string, finalized bool) (string, error) {
	s.mu.Lock()
	adbPath := s.ADBPath
	serial := s.Address
	saveDir := s.SaveDir
	s.mu.Unlock()

	// 本地保存路径：默认程序目录下 videos/（Start 已确保目录存在）
	baseDir := saveDir
	if baseDir == "" {
		baseDir = filepath.Join(".", "videos")
	}
	localName := strings.TrimPrefix(filepath.Base(remote), "/")
	fileName := strings.TrimPrefix(localName, recordRemotePrefix)
	localPath := filepath.Join(baseDir, fileName)

	// 回传（覆盖同名旧文件；60 秒超时防设备异常时挂死）
	ctx, cancel := context.WithTimeout(context.Background(), recordCmdTimeoutSec*time.Second)
	_, err := s.runner.Run(ctx, adbPath, "-s", serial, "pull", remote, localPath)
	cancel()
	if err != nil {
		return "", fmt.Errorf("回传录像失败: %w", err)
	}

	// 校验本地文件头部含 ftyp box（有效 mp4 标志）
	ok, err := validateMp4Header(localPath)
	if err != nil {
		_ = os.Remove(localPath) // 不可读视为坏文件，删除避免误导用户
		return "", fmt.Errorf("录像文件校验失败: %w", err)
	}
	if !ok {
		_ = os.Remove(localPath)
		if finalized {
			return "", fmt.Errorf("录像文件无效（缺少 mp4 头，可能设备端录制未正常结束），请重试")
		}
		return "", fmt.Errorf("录像文件无效（停止信号未成功，设备端录制可能未正常收尾）：" +
			"设备端临时文件已保留，可手动执行 adb pull " + remote + " 尝试恢复")
	}
	return localPath, nil
}

// removeRemoteQuietly 删除设备端临时文件（尽力而为，失败静默）。
func (s *RecordSession) removeRemoteQuietly(remote string) {
	s.mu.Lock()
	adbPath := s.ADBPath
	serial := s.Address
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), recordCmdTimeoutSec*time.Second)
	_, _ = s.runner.Run(ctx, adbPath, "-s", serial, "shell", "rm", "-f", remote)
	cancel()
}

// CleanStaleRemote 清理设备端本工具留下的录屏残留（应用异常退出未能回传时）。
// 供每次开始新录屏前与前端「开始录屏」时调用；失败静默。
func (s *RecordSession) CleanStaleRemote() {
	s.mu.Lock()
	adbPath := s.ADBPath
	serial := s.Address
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), recordCmdTimeoutSec*time.Second)
	_, _ = s.runner.Run(ctx, adbPath, "-s", serial, "shell",
		"rm", "-f", recordRemoteDir+"/"+recordRemotePrefix+"*.mp4")
	cancel()
}

// validateMp4Header 检查本地文件头部是否包含 ftyp box。
// 入参: path 本地文件路径
// 返回: 是否有效 mp4；读取失败时返回错误
func validateMp4Header(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	head := make([]byte, mp4HeaderLen)
	n, err := f.Read(head)
	if err != nil && n == 0 {
		return false, err
	}
	return bytes.Contains(head[:n], []byte(ftypMagic)), nil
}

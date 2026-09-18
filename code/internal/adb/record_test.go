package adb

// ============================================================================
// 文件名称 : record_test.go
// 功    能 : 录屏会话的单元测试（白盒，无需真实设备与 adb 环境）。
//            通过注入 fake runner 离线验证：
//              1. 状态机流转（Idle→Recording→Stopping→Finished/Error）
//              2. 停止命令拼装（pidof+kill 优先，失败回退 pkill/killall）
//              3. mp4 头校验（ftyp box）
//              4. 重复 Stop 幂等 / 非法状态拒绝
//              5. 自然结束监听（time-limit 到时自动回传）
// 运行方式 : 在 code/ 目录执行 go test ./internal/adb/
// ============================================================================

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRecordSession 创建注入了 fake runner、noop spawner 与临时目录的测试会话。
// calls 记录每次 fake runner 收到的命令行（空格拼接，便于断言）。
func newTestRecordSession(t *testing.T, saveDir string) (*RecordSession, *[]string) {
	t.Helper()
	calls := &[]string{}
	s := newRecordSessionWithRunner("fake-adb", "SERIAL1", RecordRunnerFunc(
		func(ctx context.Context, adbPath string, args ...string) (string, error) {
			*calls = append(*calls, strings.Join(args, " "))
			// pull 后要在本地生成一个"伪 mp4"（ftyp+moov）供两级校验
			if len(args) >= 3 && args[2] == "pull" {
				local := args[4]
				_ = os.WriteFile(local, []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00moov"), 0644)
			}
			return "ok", nil
		}))
	// noop spawner：不启动真实进程，仅记录命令行（Start 成功、Stop 跳过进程等待）
	s.setSpawner(fakeSpawner{calls: calls})
	s.SaveDir = saveDir
	return s, calls
}

// fakeSpawner 记录 Spawn 收到的命令行且不产生真实进程。
type fakeSpawner struct{ calls *[]string }

// Spawn 实现 RecordSpawner（spawned=false → 会话 cmd=nil、waitDone=nil，
// Stop 自动跳过进程等待）。
func (f fakeSpawner) Spawn(ctx context.Context, adbPath string, args ...string) (*exec.Cmd, bool, error) {
	*f.calls = append(*f.calls, strings.Join(args, " "))
	return nil, false, nil
}

// --- 状态机：完整成功链路 ---

func TestRecordSessionHappyPath(t *testing.T) {
	saveDir := t.TempDir()
	s, calls := newTestRecordSession(t, saveDir)
	ctx := context.Background()

	// Idle 状态 ElapsedSec 应为 0
	if got := s.ElapsedSec(); got != 0 {
		t.Fatalf("Idle 阶段 ElapsedSec 应为 0，实际 %d", got)
	}

	// 开始录制
	if err := s.Start(ctx); err != nil {
		t.Fatal("Start 不应失败:", err)
	}
	if phase, _ := s.Phase(); phase != RecordRecording {
		t.Fatalf("Start 后应处于 recording，实际 %s", phase)
	}

	// 停止并回传
	path, err := s.Stop()
	if err != nil {
		t.Fatal("Stop 不应失败:", err)
	}
	if phase, _ := s.Phase(); phase != RecordFinished {
		t.Fatalf("Stop 后应处于 finished，实际 %s", phase)
	}
	// 返回路径应位于 SaveDir（临时目录）内且为 .mp4
	if filepath.Dir(path) != saveDir || !strings.HasSuffix(path, ".mp4") {
		t.Fatalf("返回路径不符预期: %s（期望位于 %s）", path, saveDir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("本机视频文件应已生成: %v", err)
	}

	joined := strings.Join(*calls, "\n")
	// 断言停止方案按序尝试：首选 pidof+kill（toybox 语义最可靠）
	if !strings.Contains(joined, "sh -c kill -INT $(pidof screenrecord)") {
		t.Errorf("应首选 pidof+kill 停止方案，实际命令序列:\n%s", joined)
	}
	// pidof+kill 已成功：后续方案（pkill/killall）不应再被调用
	if strings.Contains(joined, "pkill") || strings.Contains(joined, "killall") {
		t.Errorf("pidof+kill 成功后不应再尝试其他停止方案，实际:\n%s", joined)
	}
	// 断言回传与设备端清理
	if !strings.Contains(joined, "pull /sdcard/adbhelper_record_") {
		t.Errorf("应回传设备端录像，实际:\n%s", joined)
	}
	if !strings.Contains(joined, "rm -f /sdcard/adbhelper_record_") {
		t.Errorf("回传后应清理设备端文件，实际:\n%s", joined)
	}
}

// --- 停止方案回退：pidof+kill 失败时改用 pkill ---

func TestRecordStopFallbackToPkill(t *testing.T) {
	calls := &[]string{}
	s := newRecordSessionWithRunner("fake-adb", "SERIAL1", RecordRunnerFunc(
		func(ctx context.Context, adbPath string, args ...string) (string, error) {
			*calls = append(*calls, strings.Join(args, " "))
			joined := strings.Join(args, " ")
			// pidof 不可用（模拟极老系统），其余命令成功
			if strings.Contains(joined, "pidof") {
				return "", errors.New("pidof: not found")
			}
			if len(args) >= 3 && args[2] == "pull" {
				_ = os.WriteFile(args[4], []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00moov"), 0644)
			}
			return "ok", nil
		}))
	s.setSpawner(fakeSpawner{calls: calls})
	s.SaveDir = t.TempDir()

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stop(); err != nil {
		t.Fatal("pidof 失败时应回退到 pkill 并成功:", err)
	}

	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "pidof") || !strings.Contains(joined, "pkill -INT -f screenrecord") {
		t.Fatalf("应先试 pidof 再回退 pkill，实际:\n%s", joined)
	}
	if strings.Contains(joined, "killall") {
		t.Errorf("pkill 已成功，killall 不应被调用:\n%s", joined)
	}
}

// --- 自然结束：time-limit 到时监听 goroutine 自动回传 ---

func TestRecordNaturalEndAutoPull(t *testing.T) {
	dir := t.TempDir()
	calls := &[]string{}

	// fake spawner 产出一个「立刻退出」的真实进程，模拟设备端到时退出
	s := newRecordSessionWithRunner("fake-adb", "SERIAL1", RecordRunnerFunc(
		func(ctx context.Context, adbPath string, args ...string) (string, error) {
			*calls = append(*calls, strings.Join(args, " "))
			if len(args) >= 3 && args[2] == "pull" {
				_ = os.WriteFile(args[4], []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00moov"), 0644)
			}
			return "ok", nil
		}))
	s.setSpawner(fakeExitSpawner{calls: calls})
	s.SaveDir = dir

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 监听 goroutine 应在进程退出后自动 Stop 并完成回传（轮询等待，最长 3 秒）
	deadline := time.Now().Add(3 * time.Second)
	for {
		if phase, _ := s.Phase(); phase == RecordFinished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("自然结束后应自动完成回传（3 秒内未达 finished）")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 自动回传产物应存在
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("自动回传后 SaveDir 应有视频文件")
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "pull") {
		t.Fatalf("自然结束应自动执行 pull:\n%s", joined)
	}
}

// fakeExitSpawner 产出一个立即退出的真实子进程（模拟设备端到时退出），
// 使 Start 的监听 goroutine 拿到真实 Wait 结果并触发自动回传路径。
type fakeExitSpawner struct{ calls *[]string }

func (f fakeExitSpawner) Spawn(ctx context.Context, adbPath string, args ...string) (*exec.Cmd, bool, error) {
	*f.calls = append(*f.calls, strings.Join(args, " "))
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	return cmd, true, nil
}

// --- 回传坏文件：ftyp 校验失败 → 删本地坏文件 + Error 态 ---

func TestRecordBadFileRejected(t *testing.T) {
	s, _ := newTestRecordSession(t, t.TempDir())
	// 临时替换 runner：pull 生成的是无效内容（无 ftyp）
	s.runner = RecordRunnerFunc(func(ctx context.Context, adbPath string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "pull") {
			_ = os.WriteFile(args[4], []byte("not-an-mp4"), 0644)
		}
		return "ok", nil
	})

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := s.Stop()
	if err == nil {
		t.Fatal("无 ftyp 的文件应被拒绝")
	}
	if !strings.Contains(err.Error(), "mp4") {
		t.Fatalf("错误信息应说明 mp4 无效: %v", err)
	}
	if phase, msg := s.Phase(); phase != RecordError || !strings.Contains(msg, "mp4") {
		t.Fatalf("应处于 error 态并携带原因，实际 %s / %s", phase, msg)
	}
}

// --- 信号失败（finalized=false）时校验不通过：错误信息应提示设备端副本已保留 ---

func TestRecordUnfinalizedKeepsRemoteCopy(t *testing.T) {
	calls := &[]string{}
	s := newRecordSessionWithRunner("fake-adb", "SERIAL1", RecordRunnerFunc(
		func(ctx context.Context, adbPath string, args ...string) (string, error) {
			*calls = append(*calls, strings.Join(args, " "))
			joined := strings.Join(args, " ")
			// 全部停止信号失败（含 pidof/pkill/killall）
			if strings.Contains(joined, "pidof") || strings.Contains(joined, "pkill") ||
				strings.Contains(joined, "killall") {
				return "", errors.New("not found")
			}
			// pull 拉回的文件无 ftyp（设备端未正常收尾）
			if len(args) >= 3 && args[2] == "pull" {
				_ = os.WriteFile(args[4], []byte("partial-no-moov"), 0644)
			}
			return "ok", nil
		}))
	s.setSpawner(fakeSpawner{calls: calls})
	s.SaveDir = t.TempDir()

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := s.Stop()
	if err == nil {
		t.Fatal("未收尾且无 ftyp 的文件应被拒绝")
	}
	if !strings.Contains(err.Error(), "已保留") {
		t.Fatalf("错误信息应提示设备端副本已保留: %v", err)
	}
	// 设备端清理不应执行（finalized=false 保留副本）
	joined := strings.Join(*calls, "\n")
	if strings.Contains(joined, "rm -f /sdcard/adbhelper_record_2") {
		t.Fatalf("finalized=false 时不应删除设备端文件:\n%s", joined)
	}
}

// --- 幂等与非法状态 ---

func TestRecordStopIdempotentAndGuards(t *testing.T) {
	s, _ := newTestRecordSession(t, t.TempDir())

	// Idle 时 Stop 应报错
	if _, err := s.Stop(); err == nil {
		t.Fatal("Idle 状态 Stop 应报错")
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Recording 状态重复 Start 应拒绝
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("Recording 状态重复 Start 应报错")
	}

	path1, err := s.Stop()
	if err != nil {
		t.Fatal(err)
	}
	// 二次 Stop 应幂等返回同一路径
	path2, err := s.Stop()
	if err != nil {
		t.Fatal("Finished 状态重复 Stop 应幂等成功:", err)
	}
	if path1 != path2 {
		t.Fatalf("幂等 Stop 应返回同一路径: %s vs %s", path1, path2)
	}
}

// --- mp4 头校验纯函数 ---

func TestValidateMp4Header(t *testing.T) {
	dir := t.TempDir()

	// 有效：头部含 ftyp
	okPath := filepath.Join(dir, "ok.mp4")
	_ = os.WriteFile(okPath, []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), 0644)
	if ok, err := validateMp4Header(okPath); err != nil || !ok {
		t.Fatalf("含 ftyp 的文件应通过校验: ok=%v err=%v", ok, err)
	}

	// 无效：纯文本
	badPath := filepath.Join(dir, "bad.mp4")
	_ = os.WriteFile(badPath, []byte("screencap: failed to record"), 0644)
	if ok, err := validateMp4Header(badPath); err != nil || ok {
		t.Fatalf("无 ftyp 的文件应不通过: ok=%v err=%v", ok, err)
	}

	// 无效：空文件（Read 返回 EOF 且 n=0，应报错路径）
	emptyPath := filepath.Join(dir, "empty.mp4")
	_ = os.WriteFile(emptyPath, []byte{}, 0644)
	if ok, err := validateMp4Header(emptyPath); err == nil && ok {
		t.Fatal("空文件应不通过校验")
	}

	// 不存在的文件
	if _, err := validateMp4Header(filepath.Join(dir, "nope.mp4")); err == nil {
		t.Fatal("不存在的文件应返回错误")
	}
}

// --- CleanStaleRemote 命令拼装（仅清过期残留，保留当天副本） ---

func TestRecordCleanStaleCommand(t *testing.T) {
	s, calls := newTestRecordSession(t, t.TempDir())
	s.CleanStaleRemote()
	joined := strings.Join(*calls, "\n")
	// 预清理经 sh -c 逐文件判断：当天时间戳的副本保留（可能是「缺 moov
	// 保留副本」错误引导用户手动恢复的文件），隔天的清掉
	if !strings.Contains(joined, "for f in /sdcard/adbhelper_record_*.mp4") ||
		!strings.Contains(joined, time.Now().Format("20060102")) {
		t.Fatalf("清理残留命令不符（应保留当天副本）:\n%s", joined)
	}
}

// --- moov 播放索引校验 ---

func TestHasMoovBox(t *testing.T) {
	dir := t.TempDir()

	// 含 moov：正常收尾的 mp4（关键字不在块边界也行——单文件一次读完）
	moovPath := filepath.Join(dir, "moov.mp4")
	_ = os.WriteFile(moovPath, []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00moov..."), 0644)
	if !hasMoovBox(moovPath) {
		t.Fatal("含 moov 的文件应检出 true")
	}

	// 缺 moov：强杀产物（有 ftyp 无索引）
	badPath := filepath.Join(dir, "nomoov.mp4")
	_ = os.WriteFile(badPath, []byte("\x00\x00\x00\x18ftypmp42"+strings.Repeat("mdat-data", 1000)), 0644)
	if hasMoovBox(badPath) {
		t.Fatal("缺 moov 的文件应检出 false")
	}

	// 关键字跨 64KB 块边界：拼接窗口必须命中
	// （"m" 落在第一块最后一个字节，"oov" 在第二块开头）
	splitPath := filepath.Join(dir, "split.mp4")
	big := make([]byte, 64*1024+4)
	copy(big[64*1024-1:], "moov")
	_ = os.WriteFile(splitPath, big, 0644)
	if !hasMoovBox(splitPath) {
		t.Fatal("跨块边界的 moov 应被检出（窗口拼接逻辑失效）")
	}

	// 文件不存在：false 不 panic
	if hasMoovBox(filepath.Join(dir, "nope.mp4")) {
		t.Fatal("不存在的文件应返回 false")
	}
}

// --- finalized=true 路径缺 moov 也必须拒绝（统一 moov 校验） ---

func TestRecordFinalizedButNoMoovRejected(t *testing.T) {
	s, _ := newTestRecordSession(t, t.TempDir())
	s.runner = RecordRunnerFunc(func(ctx context.Context, adbPath string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		// 停止信号成功（finalized=true），但 pull 回传的文件有 ftyp 无 moov
		if strings.Contains(joined, "pidof") {
			return "ok", nil
		}
		if len(args) >= 3 && args[2] == "pull" {
			// 注意：内容里不能出现 "moov" 字样（哪怕是 "no-moov" 也会被子串扫描命中）
			_ = os.WriteFile(args[4], []byte("\x00\x00\x00\x18ftypmp42mdatmdat"), 0644)
		}
		return "ok", nil
	})

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := s.Stop()
	if err == nil {
		t.Fatal("finalized=true 但缺 moov 的文件也应被拒绝")
	}
	if phase, _ := s.Phase(); phase != RecordError {
		t.Fatalf("应处于 error 态，实际 %s", phase)
	}
}

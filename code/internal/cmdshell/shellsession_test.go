//go:build windows

package cmdshell

// ============================================================================
// 文件名称 : shellsession_test.go
// 功    能 : 交互式 shell 会话的单元测试。
//            被测进程用 cmd.exe 代替 adb shell——stdin/stdout 管道交互、
//            逐行输出回调、退出通知的行为完全一致，且不依赖设备与 adb，
//            离线可跑（与真实 adb 的差异仅在于命令语法）。
// ============================================================================

import (
	"strings"
	"testing"
	"time"
)

// waitForText 在超时内从输出通道等到包含指定子串的行。
// cmd.exe 启动会先吐 banner（版本/版权两行），断言必须按内容等而非按行序等
func waitForText(t *testing.T, ch chan string, substr string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line := <-ch:
			if strings.Contains(line, substr) {
				return
			} // banner 等无关行：继续等
		case <-deadline:
			t.Fatalf("等待输出包含 %q 超时", substr)
		}
	}
}

// TestShellSessionRoundTrip 全链路：启动 → 写命令收输出 → exit 自然退出 → 回调收尾
func TestShellSessionRoundTrip(t *testing.T) {
	outCh := make(chan string, 64)
	endCh := make(chan struct{})

	s := NewShellSession("cmd.exe", []string{"/Q"}, func(line string) {
		outCh <- line
	}, func() {
		close(endCh)
	})

	if s.Running() {
		t.Fatal("未 Start 前不应为运行态")
	}
	if err := s.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer s.Stop()
	if !s.Running() {
		t.Fatal("Start 后应为运行态")
	}

	// 写两行命令，各自应回显输出（banner 之后按内容等到 probe 行）
	if err := s.Write("echo shell_probe_one"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	waitForText(t, outCh, "shell_probe_one")

	if err := s.Write("echo shell_probe_two"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	waitForText(t, outCh, "shell_probe_two")

	// exit：进程自然退出 → onEnd 恰好触发一次 → Running 归 false
	if err := s.Write("exit"); err != nil {
		t.Fatalf("写入 exit 失败: %v", err)
	}
	select {
	case <-endCh:
	case <-time.After(10 * time.Second):
		t.Fatal("exit 后未收到结束回调")
	}
	if s.Running() {
		t.Error("进程退出后 Running 应为 false")
	}

	// 退出后再写：明确报错而不是静默丢命令
	if err := s.Write("echo after_exit"); err == nil {
		t.Error("会话结束后 Write 应返回错误")
	}
}

// TestShellSessionStop 主动停止：Kill 后收到一次结束回调，重复 Stop 不再触发
func TestShellSessionStop(t *testing.T) {
	outCh := make(chan string, 64)
	endCh := make(chan int, 8) // 记录回调次数

	s := NewShellSession("cmd.exe", []string{"/Q"}, func(line string) {
		outCh <- line
	}, func() {
		endCh <- 1
	})
	if err := s.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	s.Stop()
	select {
	case <-endCh:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop 后未收到结束回调")
	}

	// 幂等：重复 Stop 不应再次触发回调
	s.Stop()
	select {
	case n := <-endCh:
		t.Fatalf("重复 Stop 不应再触发结束回调, got %d", n)
	case <-time.After(500 * time.Millisecond):
	}
	if s.Running() {
		t.Error("Stop 后 Running 应为 false")
	}
}

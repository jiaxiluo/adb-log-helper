package adb

// ============================================================================
// 文件名称 : livelog_test.go
// 功    能 : 实时日志模块的单元测试（纯函数部分，不依赖设备与 adb）：
//            1. ParseLiveLogLine —— logcat -v time 各类行的解析
//            2. LogcatFilterExpr —— 最低级别 → logcat filter 表达式的转换
// ============================================================================

import "testing"

// TestParseLiveLogLineStandard 验证标准格式行的五段解析（时间/级别/Tag/PID/正文）
func TestParseLiveLogLineStandard(t *testing.T) {
	// 老系统常见样式：PID 前有空格填充对齐
	line := "08-21 10:23:45.123 D/ActivityManager( 1234): Starting activity"
	got := ParseLiveLogLine(line)

	if got.Time != "08-21 10:23:45.123" {
		t.Errorf("时间解析错误: got %q", got.Time)
	}
	if got.Level != "D" {
		t.Errorf("级别解析错误: got %q, want D", got.Level)
	}
	if got.Tag != "ActivityManager" {
		t.Errorf("Tag 解析错误: got %q", got.Tag)
	}
	if got.Pid != "1234" {
		t.Errorf("PID 解析错误: got %q, want 1234（应剥离空格填充）", got.Pid)
	}
	if got.Message != "Starting activity" {
		t.Errorf("正文解析错误: got %q", got.Message)
	}
}

// TestParseLiveLogLineNoPad 验证新系统样式（PID 无空格填充）的解析
func TestParseLiveLogLineNoPad(t *testing.T) {
	line := "08-21 09:00:00.001 E/System.err(5678): FATAL EXCEPTION"
	got := ParseLiveLogLine(line)

	if got.Level != "E" || got.Tag != "System.err" || got.Pid != "5678" {
		t.Errorf("解析错误: %+v", got)
	}
	if got.Message != "FATAL EXCEPTION" {
		t.Errorf("正文解析错误: got %q", got.Message)
	}
}

// TestParseLiveLogLineSpecial 验证非标准行不报错、整行作为正文、级别置 "?"
func TestParseLiveLogLineSpecial(t *testing.T) {
	cases := []string{
		"--------- beginning of main", // logcat 头部行
		"--------- switch to system",  // 缓冲区切换行
		"",                            // 空行
		"一些非日志输出",                     // 异常文本
	}
	for _, c := range cases {
		got := ParseLiveLogLine(c)
		if got.Level != "?" {
			t.Errorf("非标准行级别应为 \"?\": 输入 %q got %q", c, got.Level)
		}
		if got.Message != c {
			t.Errorf("非标准行应整行入正文: 输入 %q got %q", c, got.Message)
		}
		if got.Raw != c {
			t.Errorf("Raw 应保留原始行: 输入 %q got %q", c, got.Raw)
		}
	}
}

// TestParseLiveLogLineMessageWithColon 验证正文中含冒号/括号时不被误切
func TestParseLiveLogLineMessageWithColon(t *testing.T) {
	line := "08-21 10:23:45.999 W/MyTag(42): key=value (ok): done"
	got := ParseLiveLogLine(line)
	if got.Message != "key=value (ok): done" {
		t.Errorf("含冒号正文解析错误: got %q", got.Message)
	}
}

// TestParseLiveLogLinePaddedTag 验证真实机顶盒设备的对齐格式：
// Tag 与 "(" 之间有对齐空格填充（真机 172.31.8.161 实测样本），
// 解析后 Tag 必须去掉尾随空格，否则显示与精确匹配都会受影响
func TestParseLiveLogLinePaddedTag(t *testing.T) {
	// 真机抓取的原始行：adbd 与 ( 之间有 4 个空格
	line := "08-21 17:53:17.839 I/adbd    (  532): UsbFfsConnection constructed"
	got := ParseLiveLogLine(line)

	if got.Tag != "adbd" {
		t.Errorf("对齐格式 Tag 应去除尾随空格: got %q, want \"adbd\"", got.Tag)
	}
	if got.Level != "I" || got.Pid != "532" {
		t.Errorf("对齐格式级别/PID 解析错误: %+v", got)
	}
	if got.Message != "UsbFfsConnection constructed" {
		t.Errorf("对齐格式正文解析错误: got %q", got.Message)
	}
}

// TestLogcatFilterExpr 验证最低级别到 filter 表达式的映射规则
func TestLogcatFilterExpr(t *testing.T) {
	cases := []struct {
		in   string // 用户选择的最低级别
		want string // 期望的 filter 表达式（空 = 不过滤）
	}{
		{"V", ""},    // Verbose 全量：不附加过滤
		{"D", "*:D"}, // Debug 及以上
		{"I", "*:I"}, // Info 及以上
		{"W", "*:W"}, // Warn 及以上
		{"E", "*:E"}, // Error 及以上
		{"F", "*:F"}, // Fatal
		{"X", ""},    // 非法值：安全兜底不过滤
		{"", ""},     // 空值：不过滤
	}
	for _, c := range cases {
		if got := LogcatFilterExpr(c.in); got != c.want {
			t.Errorf("LogcatFilterExpr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

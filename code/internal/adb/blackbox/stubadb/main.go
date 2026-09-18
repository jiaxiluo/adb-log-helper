package main

// ============================================================================
// 文件名称 : main.go（stubadb —— 黑盒测试用的假 adb）
// 功    能 : 把收到的命令行参数逐行写入 STUB_OUT 环境变量指定的文件，
//            并把 STUB_REPLY 指定的文本打印到 stdout，模拟真实 adb 的行为。
//            由 blackbox_test.go 在测试时编译为临时 exe 使用。
//            V1.0.1 扩展：支持录屏端到端模拟 ——
//              · 识别 shell screenrecord ...：在 STUB_FILE 指定路径写一个
//                带 ftyp 头的伪 mp4，模拟设备端已生成录像文件
//              · 识别 shell rm：删除 STUB_FILE（模拟设备端清理）
// ============================================================================

import (
	"os"
	"strings"
)

// fakeMp4 是带 ftyp box 的最小伪 mp4 内容（满足头部校验即可，无需真实编码）
const fakeMp4 = "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"

func main() {
	args := os.Args[1:]

	out := os.Getenv("STUB_OUT")
	if out != "" {
		_ = os.WriteFile(out, []byte(strings.Join(args, "\n")), 0644)
	}

	// 录屏模拟：收到 screenrecord 命令（参数形如 -s <serial> shell
	// screenrecord --time-limit 180 <remote>）→ 在 STUB_FILE 指定的本机路径
	// 写一个带 ftyp 头的伪 mp4，模拟"设备端已生成录像文件"
	// （stub 与被测程序同机，无法真造 /sdcard 文件）
	for i, a := range args {
		if a == "screenrecord" && i+3 < len(args) && args[i+1] == "--time-limit" {
			if f := os.Getenv("STUB_FILE"); f != "" {
				_ = os.WriteFile(f, []byte(fakeMp4), 0644)
			}
			break
		}
	}

	// 回传模拟：收到 pull 命令（参数形如 -s <serial> pull <remote> <local>）→
	// 把 STUB_FILE 拷贝为 <local>，模拟"设备端文件被拉回本机"
	for i, a := range args {
		if a == "pull" && i+2 < len(args) {
			if f := os.Getenv("STUB_FILE"); f != "" {
				if data, err := os.ReadFile(f); err == nil {
					_ = os.WriteFile(args[i+2], data, 0644)
				}
			}
			break
		}
	}

	reply := os.Getenv("STUB_REPLY")
	if reply != "" {
		os.Stdout.WriteString(reply)
	}
}

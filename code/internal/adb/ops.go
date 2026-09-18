package adb

// 本文件封装了除日志抓取之外的常用 adb 操作，供桌面 GUI 前端调用。
// 所有操作统一以 exec.Command 执行 adb 命令，返回标准输出/标准错误的组合文本，
// 前端拿到结果后直接展示，无需理解 adb 命令行细节。

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Device 表示 adb devices 列表中一台设备的简要信息。
// 对应命令: adb devices
type Device struct {
	Serial string `json:"serial"` // 设备序列号，如 192.168.1.100:5555（TCP）或 emulator-5554（USB/模拟器）
	State  string `json:"state"`  // 设备状态，如 device / offline / unauthorized
}

// DeviceDetail 表示一台设备的完整信息，用于「查看设备列表」弹窗展示。
// 在 Device 基础上补充连接方式与设备属性（型号 / 安卓版本 / 品牌）。
type DeviceDetail struct {
	Serial    string `json:"serial"`    // 设备序列号
	State     string `json:"state"`     // 设备状态：device / offline / unauthorized 等
	Transport string `json:"transport"` // 连接方式：USB / TCP / 模拟器
	Model     string `json:"model"`     // 设备型号（ro.product.model），不可用或查询超时为 "未知"/"-"
	Version   string `json:"version"`   // 安卓版本（ro.build.version.release），不可用或查询超时为 "未知"/"-"
	Brand     string `json:"brand"`     // 设备品牌（ro.product.brand），不可用或查询超时为 "未知"/"-"
}

// propQueryTimeout 是单台设备属性查询（getprop）的超时时间。
// 设备状态异常时 adb shell 可能长时间无响应，超时保护避免一台设备拖垮整个列表。
const propQueryTimeout = 4 * time.Second

// run 执行一条 adb 命令，并返回去除首尾空白后的组合输出（stdout+stderr）。
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - args:    adb 命令参数（不含 adb 本身）
//
// 返回:
//   - output: 命令的输出文本（成功或失败均返回，便于前端展示）
//   - err:    命令退出码非 0 时返回错误，错误信息中包含输出内容
func run(adbPath string, args ...string) (string, error) {
	cmd := hiddenCmd(adbPath, args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		// 命令执行失败，把输出一并塞进错误，前端可直接展示具体原因
		return text, fmt.Errorf("%s: %w", text, err)
	}
	return text, nil
}

// Devices 返回当前已连接的设备列表。
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//
// 返回:
//   - []Device: 解析后的设备列表
//   - error:    命令执行出错时返回
func Devices(adbPath string) ([]Device, error) {
	output, err := run(adbPath, "devices")
	if err != nil {
		return nil, err
	}
	return parseDevices(output), nil
}

// classifyTransport 依据序列号特征推断设备的连接方式。
// 规则（与 adb 惯例一致）：
//   - 以 "emulator" 开头 → 本地模拟器（如 emulator-5554）
//   - 序列号含 ":"      → TCP/IP 连接（如 192.168.1.100:5555）
//   - 其余              → USB 连接（厂商序列号一般为纯字母数字）
//
// 入参: serial 设备序列号
// 返回: 连接方式描述文本（"模拟器" / "TCP" / "USB"）
func classifyTransport(serial string) string {
	if strings.HasPrefix(serial, "emulator") {
		return "模拟器"
	}
	if strings.Contains(serial, ":") {
		return "TCP"
	}
	return "USB"
}

// parseGetpropOutput 解析「型号 + 安卓版本 + 品牌」组合 getprop 命令的输出。
// 输入对应命令: getprop ro.product.model ; getprop ro.build.version.release ; getprop ro.product.brand
// 正常为三行文本（第一行型号、第二行版本、第三行品牌）；设备端多为 CRLF 换行，需去除 \r。
// 独立成函数便于单元测试（无需真实设备）。
// 入参: output 组合命令的原始输出文本
// 返回: 型号、安卓版本、品牌；对应行缺失或为空时该字段返回占位符 "未知"
func parseGetpropOutput(output string) (model string, version string, brand string) {
	model = "未知"
	version = "未知"
	brand = "未知"

	lines := strings.Split(strings.ReplaceAll(output, "\r", ""), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		model = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		version = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 && strings.TrimSpace(lines[2]) != "" {
		brand = strings.TrimSpace(lines[2])
	}
	return model, version, brand
}

// fetchDeviceProps 查询单台设备的型号、安卓版本与品牌。
// 对应命令: adb -s <serial> shell getprop ro.product.model ; getprop ro.build.version.release ; getprop ro.product.brand
// （三条 getprop 合并为一次 shell 调用，减少与设备的往返次数）
// 带超时保护：超时或执行失败时返回占位符 "未知"，不向调用方报错——
// 属性属于增强信息，获取失败不应影响设备列表整体的展示。
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//
// 返回: 型号、安卓版本、品牌
func fetchDeviceProps(adbPath, serial string) (string, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), propQueryTimeout)
	defer cancel()

	// 各 token 作为独立参数传递，由 adb 拼接后在设备 shell 中执行，
	// 避免 Windows 引号转义问题
	cmd := hiddenCmdContext(ctx, adbPath, "-s", serial, "shell",
		"getprop", "ro.product.model", ";",
		"getprop", "ro.build.version.release", ";",
		"getprop", "ro.product.brand")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "未知", "未知", "未知"
	}
	return parseGetpropOutput(string(output))
}

// DevicesDetail 返回所有设备的完整信息列表（序列号/状态/连接方式/型号/安卓版本/品牌）。
// 供前端「查看设备列表」弹窗使用。流程：
//  1. adb devices 获取序列号与状态（一次命令）
//  2. 对每台 state=device 的设备并行查询属性（getprop），互不阻塞
//
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//
// 返回:
//   - []DeviceDetail: 设备完整信息列表（无设备时为空切片）
//   - error:          adb devices 本身执行失败时返回
func DevicesDetail(adbPath string) ([]DeviceDetail, error) {
	devices, err := Devices(adbPath)
	if err != nil {
		return nil, err
	}

	// 先用 adb devices 的结果填充基础字段；属性字段并行补齐。
	// 每个协程只写自己的下标，无需加锁
	details := make([]DeviceDetail, len(devices))
	var wg sync.WaitGroup
	for i, dev := range devices {
		details[i] = DeviceDetail{
			Serial:    dev.Serial,
			State:     dev.State,
			Transport: classifyTransport(dev.Serial),
			Model:     "-", // 非 device 状态无法响应 shell 命令，用占位符
			Version:   "-",
			Brand:     "-",
		}
		if dev.State != "device" {
			continue // offline/unauthorized 等状态查属性必失败，直接跳过
		}
		wg.Add(1)
		go func(idx int, serial string) {
			defer wg.Done()
			details[idx].Model, details[idx].Version, details[idx].Brand = fetchDeviceProps(adbPath, serial)
		}(i, dev.Serial)
	}
	wg.Wait()
	return details, nil
}

// parseDevices 解析 adb devices 命令的文本输出，得到设备列表。
// 独立成函数便于单元测试（无需真实设备/adb 环境）。
// 入参:
//   - output: adb devices 的原始输出文本
//
// 返回: 解析出的设备列表（无设备时返回空切片）
func parseDevices(output string) []Device {
	devices := make([]Device, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// 跳过表头 "List of devices attached" 和空行
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		devices = append(devices, Device{Serial: fields[0], State: fields[1]})
	}
	return devices
}

// parseResolveActivity 从 cmd package resolve-activity --brief 的输出中
// 提取应用的启动组件名（Activity）。
// 典型输出（不同系统行数不一，组件名固定在最后一行）：
//
//	com.example.app/.MainActivity
//
// 部分系统的首行是包的安装路径等杂项信息，因此从末行向前查找。
// 独立成函数便于单元测试（无需真实设备）。
// 入参:
//   - output: resolve-activity --brief 的原始输出文本
//   - pkg:    应用包名（用于校验找到的行确实是该包的组件）
//
// 返回: 启动组件名（形如 "pkg/.MainActivity"，am start -n 可直接使用）；
// 未找到合法组件行时返回空字符串
func parseResolveActivity(output, pkg string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r", ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		// 合法组件行必须以 "pkg/" 开头（相对写法，am start 会自动补全包名）
		if strings.HasPrefix(line, pkg+"/") {
			return line
		}
	}
	return ""
}

// StartApp 在设备上启动指定应用（拉起到前台，等价于点击桌面图标）。
// 实现策略（两步，逐步兜底，兼顾新旧的 Android 系统）：
//  1. 优先用 cmd package resolve-activity --brief 解析出包的启动入口
//     Activity，再用 am start -n 显式启动 —— 标准做法，语义最明确
//  2. 旧系统不支持 resolve-activity、或解析/启动失败时，
//     回退用 monkey -p <pkg> -c android.intent.category.LAUNCHER 1 拉起
//     （monkey 是兼容性最好的按包名启动方式，覆盖几乎所有设备）
//
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - pkg:     应用包名，如 com.example.app
//
// 返回: 最终执行的启动命令输出与错误
func StartApp(adbPath, serial, pkg string) (string, error) {
	// 第一步：解析启动入口。失败不直接报错，进入 monkey 兜底
	resolveOut, resolveErr := run(adbPath, "-s", serial, "shell",
		"cmd", "package", "resolve-activity", "--brief", pkg)
	if resolveErr == nil {
		if component := parseResolveActivity(resolveOut, pkg); component != "" {
			startOut, startErr := run(adbPath, "-s", serial, "shell",
				"am", "start", "-n", component)
			if startErr == nil {
				return startOut, nil
			}
			// am start 失败（如组件临时不可用）：继续走 monkey 兜底
		}
	}

	// 第二步：monkey 兜底。"-c LAUNCHER 1" 表示以启动器类别拉起一次
	return run(adbPath, "-s", serial, "shell",
		"monkey", "-p", pkg, "-c", "android.intent.category.LAUNCHER", "1")
}

// ForceStop 强制停止指定应用（对应 am force-stop）。
// 效果：立即杀掉应用进程，未保存的运行中状态会丢失（磁盘数据不受影响）。
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - pkg:     应用包名
//
// 返回: 命令输出文本与错误（成功时输出通常为空）
func ForceStop(adbPath, serial, pkg string) (string, error) {
	return run(adbPath, "-s", serial, "shell", "am", "force-stop", pkg)
}

// ClearCache 清理指定包名的应用缓存。
// 对应命令: adb -s <serial> shell pm clear <pkg>
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - pkg:     应用包名，如 com.example.app
//
// 返回: 命令输出文本与错误
func ClearCache(adbPath, serial, pkg string) (string, error) {
	return run(adbPath, "-s", serial, "shell", "pm", "clear", pkg)
}

// Screenshot 对指定设备截图，保存为 PNG 文件到目标目录。
// 对应命令: adb -s <serial> exec-out screencap -p
//
// 关键点：
//  1. 截图是二进制数据，不能在 Windows 下通过 shell 重定向 ">" 落盘
//     （会因 CRLF 转换损坏 PNG）。这里直接捕获 stdout 字节写入文件。
//  2. exec-out 的 stdout 是 PNG 二进制，错误信息走 stderr，需单独捕获。
//  3. 校验 PNG 文件头，避免设备离线/锁屏时返回空数据却被当作成功截图。
//
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - saveDir: 保存目录（空字符串 = 默认程序目录下 screenshots/；不存在时自动创建）
//
// 返回:
//   - string: 保存后的 PNG 文件完整路径
//   - error:  截图或保存失败时返回
func Screenshot(adbPath, serial, saveDir string) (string, error) {
	// 零输入设计：未指定目录时使用默认位置（程序目录下 screenshots/），
	// 用户点击「截图」即可，无需任何路径输入
	if saveDir == "" {
		saveDir = filepath.Join(".", "screenshots")
	}

	// 确保保存目录存在
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return "", fmt.Errorf("创建截图目录失败: %w", err)
	}

	// 文件名带时间戳（精确到毫秒），避免多次截图互相覆盖
	filename := fmt.Sprintf("screenshot_%s.png", time.Now().Format("20060102_150405.000"))
	filePath := filepath.Join(saveDir, filename)

	// exec-out：stdout=PNG 二进制，stderr=错误信息
	cmd := hiddenCmd(adbPath, "-s", serial, "exec-out", "screencap", "-p")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("截图失败: %w（stderr: %s）", err, strings.TrimSpace(stderr.String()))
	}

	// 校验 PNG 文件签名：有效 PNG 以 89 50 4E 47 0D 0A 1A 0A 开头。
	// 设备离线/锁屏/无显示时 screencap 可能返回空数据或错误文本，需拦下。
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if len(data) < len(pngHeader) || !bytes.HasPrefix(data, pngHeader) {
		hint := strings.TrimSpace(stderr.String())
		if hint == "" {
			hint = "设备可能离线/锁屏，未返回有效图像数据"
		}
		return "", fmt.Errorf("截图失败：未获得有效 PNG 数据（%s）", hint)
	}

	// 原样写入文件，不做任何文本转换
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("保存截图失败: %w", err)
	}
	return filePath, nil
}

// InstallAPK 覆盖安装（-r）一个 APK 到指定设备。
// 对应命令: adb -s <serial> install -r <apkPath>
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - apkPath: 本机 APK 文件绝对路径
//
// 返回: 安装命令输出（含 Success / Failure 等）与错误
func InstallAPK(adbPath, serial, apkPath string) (string, error) {
	return run(adbPath, "-s", serial, "install", "-r", apkPath)
}

// Uninstall 从指定设备卸载应用。
// 对应命令: adb -s <serial> uninstall <pkg>
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - pkg:     应用包名，如 com.example.app
//
// 返回: 卸载命令输出（含 Success / Failure）与错误
func Uninstall(adbPath, serial, pkg string) (string, error) {
	return run(adbPath, "-s", serial, "uninstall", pkg)
}

// ListPackages 列出指定设备已安装的应用包名，可按关键字过滤。
// 对应命令: adb -s <serial> shell pm list packages [-3]
// 入参:
//   - adbPath:       adb 可执行文件的绝对路径
//   - serial:        目标设备序列号
//   - filter:        过滤关键字，为空时返回全部包名
//   - thirdPartyOnly: 为 true 时只列第三方应用（加 -3 参数，排除系统预装应用）
//
// 返回: 去除了 "package:" 前缀的包名列表与错误
func ListPackages(adbPath, serial, filter string, thirdPartyOnly bool) ([]string, error) {
	// 组装命令参数：仅第三方应用时追加 -3（系统应用与第三方应用互斥，-3 排除系统预装）
	args := []string{"-s", serial, "shell", "pm", "list", "packages"}
	if thirdPartyOnly {
		args = append(args, "-3")
	}

	output, err := run(adbPath, args...)
	if err != nil {
		return nil, err
	}
	return parsePackages(output, filter), nil
}

// parsePackages 解析 pm list packages 的文本输出，得到包名列表。
// 兼容两种输出格式：
//   - 新版系统（Android 4.1+）: "package:com.example.app"
//   - 旧版系统（约 4.0 及之前，常见于机顶盒）: "package:/data/app/xxx-1.apk=com.example.app"，
//     行内含 apk 路径前缀，"=" 之后才是包名。若不剥离路径部分，包名会被误判为
//     "/data/app/xxx-1.apk=com.example.app"，导致 pm clear / am start 等按包名
//     执行的操作全部失败
//
// 独立成函数便于单元测试（无需真实设备/adb 环境）。
// 入参:
//   - output: pm list packages 的原始输出文本
//   - filter: 包名关键字过滤，空字符串返回全部
//
// 返回: 包名列表（无匹配时返回空切片）
func parsePackages(output string, filter string) []string {
	pkgs := make([]string, 0, 64)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// 每行形如 "package:com.example.app"
		if !strings.HasPrefix(line, "package:") {
			continue
		}
		name := strings.TrimPrefix(line, "package:")
		// 旧版格式带 "路径=包名" 前缀，取最后一个 "=" 之后的部分作为包名
		// （包名本身不含 "="，用 LastIndex 防止路径中偶发的 "=" 干扰）
		if idx := strings.LastIndex(name, "="); idx >= 0 {
			name = name[idx+1:]
		}
		// 关键字过滤
		if filter != "" && !strings.Contains(name, filter) {
			continue
		}
		pkgs = append(pkgs, name)
	}
	return pkgs
}

// Pull 从设备拉取文件或目录到本机。
// 对应命令: adb -s <serial> pull <remote> <local>
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - remote:  设备上的远程路径
//   - local:   本机目标路径（目录或文件名）
//
// 返回: 命令输出文本与错误
func Pull(adbPath, serial, remote, local string) (string, error) {
	return run(adbPath, "-s", serial, "pull", remote, local)
}

// Push 把本机文件或目录推送到设备。
// 对应命令: adb -s <serial> push <local> <remote>
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//   - local:   本机文件/目录路径
//   - remote:  设备上的目标路径
//
// 返回: 命令输出文本与错误
func Push(adbPath, serial, local, remote string) (string, error) {
	return run(adbPath, "-s", serial, "push", local, remote)
}

// ClearLogcat 清空设备端 logcat 日志缓冲（对应 adb -s <serial> logcat -c）。
// 供日志抓取前的可选操作使用：勾选后本次抓取只包含新产生的日志，
// 不带出启动前积累的旧日志。
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - serial:  目标设备序列号
//
// 返回: 命令输出文本与错误
func ClearLogcat(adbPath, serial string) (string, error) {
	return run(adbPath, "-s", serial, "logcat", "-c")
}

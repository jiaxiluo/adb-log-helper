package adb

// ============================================================================
// 文件名称 : connect.go
// 功    能 : TCP/IP 设备连接（面向 GUI 的单次连接/断开）。
//            所有操作都是非阻塞的单次命令执行，结果文本直接返回前端展示。
//            （历史版本中的 ConnectWithRetry 死循环重试逻辑适用于终端交互，
//             GUI 下不适合，已移除。）
// ============================================================================

import (
	"fmt"
	"net"
	"strings"
)

// NormalizeAddress 校验并规范化设备地址。
// 入参允许 "192.168.1.100"、"192.168.1.100:5555"、"host:port" 三种形式，
// 不带端口时自动补默认端口 5555。
// 返回: 规范化后的 "host:port" 地址; 格式非法时返回错误
func NormalizeAddress(input string) (string, error) {
	address := strings.TrimSpace(input)
	if address == "" {
		return "", fmt.Errorf("设备地址不能为空")
	}

	// 不带端口 → 补默认端口；带端口 → 交由 SplitHostPort 校验格式
	if !strings.Contains(address, ":") {
		address = address + ":5555"
	}

	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("设备地址格式不正确（应为 IP 或 IP:端口）: %s", input)
	}
	if net.ParseIP(host) == nil && host != "localhost" {
		return "", fmt.Errorf("IP 地址格式不正确: %s", host)
	}
	if portStr == "" {
		return "", fmt.Errorf("端口号不能为空")
	}

	return address, nil
}

// Connect 单次执行 adb connect 连接 TCP 设备（GUI 用，非阻塞）。
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - address: 设备地址，"192.168.1.100" 或 "192.168.1.100:5555"
//
// 返回:
//   - string: adb connect 的输出文本（含 connected / already connected / 失败原因）
//   - error:  地址非法或连接失败时返回
func Connect(adbPath string, address string) (string, error) {
	// 先做本地格式校验（不合法直接报错，不发起连接）
	normalized, err := NormalizeAddress(address)
	if err != nil {
		return "", err
	}

	output, err := hiddenCmd(adbPath, "connect", normalized).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("%s: %w", text, err)
	}
	return text, nil
}

// Disconnect 断开一台 TCP 设备（对应 adb disconnect <address>）。
// 入参:
//   - adbPath: adb 可执行文件的绝对路径
//   - address: 设备地址，规范同 Connect（不带端口自动补 5555）
//
// 返回: 命令输出文本与错误
func Disconnect(adbPath string, address string) (string, error) {
	normalized, err := NormalizeAddress(address)
	if err != nil {
		return "", err
	}

	output, err := hiddenCmd(adbPath, "disconnect", normalized).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("%s: %w", text, err)
	}
	return text, nil
}

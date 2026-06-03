//go:build !windows

package gui

// 非 Windows 平台不需要图标
var (
	iconRed    []byte
	iconYellow []byte
	iconGreen  []byte
)

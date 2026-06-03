//go:build windows

package gui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

// generateIcon 生成 ICO 格式的图标（Windows systray 要求 .ico 格式）
func generateIcon(r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))

	// 深色背景
	bgColor := color.RGBA{R: 30, G: 30, B: 50, A: 255}
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, bgColor)
		}
	}

	// 画圆形指示灯
	circleColor := color.RGBA{R: r, G: g, B: b, A: 255}
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			dx, dy := x-16, y-16
			if dx*dx+dy*dy <= 144 { // 半径12
				img.Set(x, y, circleColor)
			}
		}
	}

	// 编码为 PNG
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)
	pngData := pngBuf.Bytes()

	// 构建 ICO 文件
	// ICONDIR (6 bytes) + ICONDIRENTRY (16 bytes) + PNG data
	icoSize := 6 + 16 + len(pngData)
	ico := make([]byte, icoSize)

	// ICONDIR header
	ico[0] = 0 // reserved
	ico[1] = 0
	ico[2] = 1 // type: ICO
	ico[3] = 0
	ico[4] = 1 // count: 1 image
	ico[5] = 0

	// ICONDIRENTRY
	ico[6] = 32 // width
	ico[7] = 32 // height
	ico[8] = 0 // color palette
	ico[9] = 0 // reserved
	binary.LittleEndian.PutUint16(ico[10:12], 1)  // color planes
	binary.LittleEndian.PutUint16(ico[12:14], 32)  // bits per pixel
	binary.LittleEndian.PutUint32(ico[14:18], uint32(len(pngData))) // image data size
	binary.LittleEndian.PutUint32(ico[18:22], 22)                   // offset to image data

	// PNG data
	copy(ico[22:], pngData)

	return ico
}

// 预生成不同状态的图标
var (
	iconRed    = generateIcon(239, 83, 80)   // 未连接 - 红色
	iconYellow = generateIcon(255, 167, 38)  // 连接中 - 黄色
	iconGreen  = generateIcon(102, 187, 106) // 已连接 - 绿色
)

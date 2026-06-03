//go:build windows

package gui

import (
	"fmt"
	"os"
	"time"

	"github.com/getlantern/systray"
)

func (a *App) runWithSystray() error {
	systray.Run(a.onSystrayReady, a.onSystrayExit)
	return nil
}

func (a *App) onSystrayReady() {
	systray.SetIcon(iconRed)
	systray.SetTitle("x-tunnel")
	systray.SetTooltip("x-tunnel 客户端 - 未连接")

	// 菜单项
	mOpen := systray.AddMenuItem("打开控制面板", "打开 Web 控制面板")
	mStatus := systray.AddMenuItem("状态: 未连接", "当前连接状态")
	mStatus.Disable()
	systray.AddSeparator()
	mConnect := systray.AddMenuItem("连接", "连接到服务器")
	mDisconnect := systray.AddMenuItem("断开", "断开连接")
	mDisconnect.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出程序")

	// 自动打开控制面板
	go func() {
		time.Sleep(500 * time.Millisecond)
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d", a.port))
	}()

	// 处理菜单事件
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(fmt.Sprintf("http://127.0.0.1:%d", a.port))
			case <-mConnect.ClickedCh:
				go a.connectFromTray()
			case <-mDisconnect.ClickedCh:
				go a.disconnectFromTray()
			case <-mQuit.ClickedCh:
				a.mu.Lock()
				a.manualStop = true
				select {
				case <-a.stopCh:
				default:
					close(a.stopCh)
				}
				a.mu.Unlock()
				systray.Quit()
				return
			}
		}
	}()

	// 更新状态和图标
	go func() {
		for {
			time.Sleep(2 * time.Second)
			a.mu.RLock()
			status := a.status
			a.mu.RUnlock()

			switch status {
			case "已连接":
				systray.SetIcon(iconGreen)
				systray.SetTooltip("x-tunnel 客户端 - 已连接")
				mStatus.SetTitle("状态: 已连接")
				mConnect.Disable()
				mDisconnect.Enable()
			case "连接中...":
				systray.SetIcon(iconYellow)
				systray.SetTooltip("x-tunnel 客户端 - 连接中...")
				mStatus.SetTitle("状态: 连接中...")
				mConnect.Disable()
				mDisconnect.Enable()
			default:
				systray.SetIcon(iconRed)
				systray.SetTooltip("x-tunnel 客户端 - 未连接")
				mStatus.SetTitle("状态: 未连接")
				mConnect.Enable()
				mDisconnect.Disable()
			}
		}
	}()
}

func (a *App) onSystrayExit() {
	a.mu.Lock()
	a.manualStop = true
	a.status = "未连接"
	a.pool = nil
	a.proxy = nil
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	a.mu.Unlock()
	os.Exit(0)
}

func (a *App) connectFromTray() {
	a.mu.Lock()
	if a.status == "已连接" || a.status == "连接中..." {
		a.mu.Unlock()
		return
	}
	a.status = "连接中..."
	a.manualStop = false
	a.stopCh = make(chan struct{})
	a.mu.Unlock()

	go a.connectWithRetry()
}

func (a *App) disconnectFromTray() {
	a.mu.Lock()
	a.manualStop = true
	a.status = "未连接"
	a.pool = nil
	a.proxy = nil
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	a.mu.Unlock()
	a.addLog("INFO", "已断开连接")
}

//go:build !windows

package gui

import "errors"

func (a *App) runWithSystray() error {
	return errors.New("系统托盘仅支持 Windows")
}

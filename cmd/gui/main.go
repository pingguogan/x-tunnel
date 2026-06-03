package main

import (
	"log"

	"github.com/x-tunnel/internal/gui"
)

func main() {
	app := gui.NewApp()
	if err := app.Run(); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

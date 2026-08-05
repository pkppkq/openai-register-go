// Command openai-register is the Wails desktop app: a Go backend (internal/*)
// driving a webview frontend (frontend/), replacing the Python/Tkinter tool.
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/pkppkq/openai-register-go/internal/ui"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := ui.New()

	err := wails.Run(&options.App{
		Title:     "OpenAI 注册 / 支付链接工具",
		Width:     1360,
		Height:    860,
		MinWidth:  1040,
		MinHeight: 680,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.Startup,
		OnShutdown: app.Shutdown,
		Bind:       []any{app},
		Windows: &windows.Options{
			// The Tk app was routinely covered by the automation's own Chromium
			// windows; keeping this off means the user can raise it normally.
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if err != nil {
		panic(err)
	}
}

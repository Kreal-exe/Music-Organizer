// Music Library Organizer brings a music collection's artist list back under
// control: it strips the album-artist credits that make players invent extra
// artists, merges spellings of the same name, splits guest credits, and fills
// in missing lyrics.
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// The interface is plain HTML, CSS and JavaScript, so it is embedded as it is
// written — there is no bundler step to run before building.
//
//go:embed all:frontend/dist
var assets embed.FS

// The size the window would like to have. It is only a wish: startup shrinks
// it to whatever the screen actually offers, because a window taller than the
// desktop puts the footer buttons behind the taskbar.
const (
	windowWidth  = 1160
	windowHeight = 800
)

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Music Library Organizer",
		Width:     windowWidth,
		Height:    windowHeight,
		MinWidth:  820,
		MinHeight: 520,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 22, G: 23, B: 27, A: 1},
		OnStartup:        app.startup,
		// The window is fitted to the screen once the page exists: resizing it
		// before the webview is attached leaves the two at different sizes and
		// the interface ends up cut off at the edges.
		OnDomReady: app.domReady,
		OnShutdown: app.shutdown,
		Bind:       []any{app},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
		Mac: &mac.Options{
			// The title bar is hidden behind the top bar, which drags the
			// window. Wails' inset style puts a toolbar there to move the
			// window buttons in, and the toolbar takes every click in that
			// strip: Open library, Rescan and the theme switch did nothing.
			// So the title bar is see-through and has no toolbar.
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: true,
				HideTitle:                  true,
				FullSizeContent:            true,
			},
			About: &mac.AboutInfo{
				Title:   "Music Library Organizer",
				Message: "Keep a music library's artist list in order",
			},
		},
	})
	if err != nil {
		log.Fatalf("could not start the application: %v", err)
	}
}

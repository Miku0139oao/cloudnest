package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"cloudnest/internal/dto"
	"cloudnest/internal/engine"
	"cloudnest/internal/platform"
	"cloudnest/internal/settings"
	"cloudnest/services"
)

// The frontend build is embedded into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

// The tray icon is the same mark the installer uses.
//
//go:embed build/appicon.png
var trayIcon []byte

// instanceID identifies this application for the single-instance lock. It is a
// variable so a smoke-test build can run next to an installed copy
// (-ldflags "-X main.instanceID=...").
var instanceID = "app.cloudnest.desktop"

func main() {
	_ = platform.EnsureDirs()
	dataDir, err := platform.DataDir()
	if err != nil {
		log.Fatal(err)
	}

	logFile, err := os.OpenFile(filepath.Join(dataDir, "logs", "cloudnest.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()

	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
	appLogger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store, err := settings.New(dataDir)
	if err != nil {
		logger.Error("settings", "err", err)
	}

	ctx := context.Background()
	rclonePath, versionInfo, resolveErr := engine.ResolveBinary(ctx, "")
	if resolveErr != nil {
		logger.Warn("rclone not found", "err", resolveErr)
	} else {
		logger.Info("rclone resolved", "path", rclonePath, "version", versionInfo.String())
	}

	prefs := store.Get()
	eng := engine.New(engine.Options{
		Logger: logger,
		LogWriter: func(line string) {
			fmt.Fprintln(logFile, line)
		},
		// The engine can only pick these up from its environment, so they are
		// applied at launch and re-applied (plus a restart) when they change.
		Verbose:  prefs.Advanced.Verbose,
		UseProxy: prefs.Advanced.Proxy,
	})

	var app *application.App
	svc := services.New(nil, eng, store, rclonePath, logger)

	app = application.New(application.Options{
		Name:        "Cloudnest",
		Description: "Cloudnest 雲栖 — simple cloud backup",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Logger:   appLogger,
		LogLevel: slog.LevelWarn,
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: instanceID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if w, ok := app.Window.GetByName("main"); ok {
					w.Restore()
					w.Focus()
				}
			},
		},
	})
	services.AttachApp(svc, app)

	// Window geometry: reuse the saved size when it is at least the minimum,
	// otherwise fall back to 1280x820. The screen manager is not reliable
	// before Run, so the saved values are taken first and only validated
	// against the work area when it can be queried.
	minWidth, minHeight := 1080, 720
	width, height := 1280, 820
	startX, startY := 0, 0
	restorePos := false
	if saved := prefs.Window; saved.Width >= minWidth && saved.Height >= minHeight {
		width, height = saved.Width, saved.Height
		if saved.X != 0 || saved.Y != 0 {
			startX, startY = saved.X, saved.Y
			restorePos = true
		}
	}
	if screen := app.Screen.GetPrimary(); screen != nil {
		if wa := screen.WorkArea; wa.Width > 0 && wa.Height > 0 {
			if width > wa.Width-40 {
				width = wa.Width - 40
			}
			if height > wa.Height-40 {
				height = wa.Height - 40
			}
			if restorePos && (startX < wa.X || startY < wa.Y ||
				startX >= wa.X+wa.Width-200 || startY >= wa.Y+wa.Height-120) {
				restorePos = false
			}
		}
	}
	if minWidth > width {
		minWidth = width
	}
	if minHeight > height {
		minHeight = height
	}
	logger.Info("window geometry",
		"saved", fmt.Sprintf("%dx%d@%d,%d", prefs.Window.Width, prefs.Window.Height, prefs.Window.X, prefs.Window.Y),
		"using", fmt.Sprintf("%dx%d@%d,%d", width, height, startX, startY),
		"restorePos", restorePos,
	)

	windowOpts := application.WebviewWindowOptions{
		Name:             "main",
		Title:            "雲栖 Cloudnest",
		Width:            width,
		Height:           height,
		MinWidth:         minWidth,
		MinHeight:        minHeight,
		Frameless:        true,
		BackgroundColour: application.NewRGB(254, 247, 255),
		URL:              "/",
		EnableFileDrop:   true,
	}
	if restorePos {
		// The start position has to go through the options: calling
		// SetPosition before the window is shown has no effect.
		windowOpts.InitialPosition = application.WindowXY
		windowOpts.X = startX
		windowOpts.Y = startY
	}
	mainWindow := app.Window.NewWithOptions(windowOpts)

	// Remember the geometry. Saving on every resize would rewrite settings.json
	// continuously, so the write is debounced and also flushed when closing.
	var geometryTimer *time.Timer
	saveGeometry := func() {
		if w, ok := app.Window.GetByName("main"); ok {
			wd, ht := w.Size()
			x, y := w.Position()
			if wd > 0 && ht > 0 {
				_ = store.Update(func(cur *dto.Settings) {
					cur.Window.Width = wd
					cur.Window.Height = ht
					cur.Window.X = x
					cur.Window.Y = y
				})
			}
		}
	}
	debounceGeometry := func() {
		if geometryTimer != nil {
			geometryTimer.Stop()
		}
		geometryTimer = time.AfterFunc(time.Second, saveGeometry)
	}
	mainWindow.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { debounceGeometry() })
	mainWindow.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { debounceGeometry() })

	// Closing during a transfer asks first: the transfers are stopped either
	// way, but nobody loses a backup by accident.
	mainWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		logger.Info("window closing", "transfers", services.ActiveTransfers(svc), "schedule", store.Get().Schedule.Enabled, "confirmed", services.CloseConfirmed(svc))
		if !services.CloseConfirmed(svc) && services.ActiveTransfers(svc) > 0 {
			e.Cancel()
			app.Event.Emit("cloudnest:close-requested")
			return
		}
		// With a schedule configured the window close hides to the tray, so
		// the daily backup still runs. The tray menu offers a real quit.
		if !services.CloseConfirmed(svc) && store.Get().Schedule.Enabled {
			e.Cancel()
			mainWindow.Hide()
			return
		}
		saveGeometry()
	})

	// Dropped files carry real filesystem paths only on the Go side, so the
	// payload is forwarded to the frontend, which decides which pane it hit.
	mainWindow.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		ctx := e.Context()
		if ctx == nil {
			return
		}
		files := ctx.DroppedFiles()
		if len(files) == 0 {
			return
		}
		payload := map[string]any{"files": files}
		if target := ctx.DropTargetDetails(); target != nil {
			payload["x"] = target.X
			payload["y"] = target.Y
			payload["elementId"] = target.ElementID
		}
		app.Event.Emit("cloudnest:files-dropped", payload)
	})

	// Tray icon: the window can be closed while the app keeps running in the
	// background, which is what makes scheduled backups useful.
	tray := app.SystemTray.New()
	tray.SetIcon(trayIcon)
	tray.SetTooltip("雲栖 Cloudnest")
	trayMenu := application.NewMenu()
	trayMenu.Add("開啟雲栖 Cloudnest").OnClick(func(*application.Context) {
		if w, ok := app.Window.GetByName("main"); ok {
			w.Show()
			w.Restore()
			w.Focus()
		}
	})
	trayMenu.AddSeparator()
	trayMenu.Add("離開 Cloudnest").OnClick(func(*application.Context) {
		services.AllowClose(svc)
		app.Quit()
	})
	tray.SetMenu(trayMenu)

	// Scheduled backups only run while the app is open; the goroutine stops
	// with the process.
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	go services.StartScheduler(svc, schedCtx)

	go func() {
		if err := eng.Start(ctx); err != nil {
			logger.Error("engine start", "err", err)
		}
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}

	stopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	services.UnmountAll(svc)
	_ = eng.Stop(stopCtx)
}

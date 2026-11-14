package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

	"cloudnest/internal/engine"
	"cloudnest/internal/platform"
	"cloudnest/internal/settings"
	"cloudnest/services"
)

// The frontend build is embedded into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

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
			UniqueID: "app.cloudnest.desktop",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if w, ok := app.Window.GetByName("main"); ok {
					w.Restore()
					w.Focus()
				}
			},
		},
	})
	services.AttachApp(svc, app)

	// Fit the default window to the primary display's work area. On high-DPI
	// laptops the logical desktop can be smaller than 1280x820.
	width, height := 1280, 820
	if screen := app.Screen.GetPrimary(); screen != nil {
		if wa := screen.WorkArea; wa.Width > 0 && wa.Height > 0 {
			if width > wa.Width-40 {
				width = wa.Width - 40
			}
			if height > wa.Height-40 {
				height = wa.Height - 40
			}
		}
	}
	minWidth, minHeight := 1080, 720
	if minWidth > width {
		minWidth = width
	}
	if minHeight > height {
		minHeight = height
	}

	app.Window.NewWithOptions(application.WebviewWindowOptions{
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
	})

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
	_ = eng.Stop(stopCtx)
}

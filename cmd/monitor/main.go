package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"server-monitor/internal/monitor"
)

func main() {
	env := flag.String("env", ".env", "path file .env (opsional jika default tidak ada)")
	once := flag.Bool("once", false, "ambil snapshot, kirim laporan Telegram, lalu keluar")
	flag.Parse()
	cfg, err := monitor.LoadConfig(*env)
	if err != nil {
		slog.Error("konfigurasi tidak valid", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := monitor.New(cfg)
	if err != nil {
		slog.Error("gagal memulai monitor", "error", err)
		os.Exit(1)
	}
	defer app.Close()
	if err := app.Run(ctx, *once); err != nil && ctx.Err() == nil {
		slog.Error("monitor berhenti", "error", err)
		os.Exit(1)
	}
}

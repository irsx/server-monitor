package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type alertState struct {
	Active   bool
	LastSent time.Time
}
type App struct {
	cfg      Config
	db       *sql.DB
	telegram *Telegram
	states   map[string]alertState
}

func New(c Config) (*App, error) {
	a := &App{cfg: c, states: map[string]alertState{}, telegram: &Telegram{
		client: &http.Client{Timeout: c.TelegramTimeout},
		url:    "https://api.telegram.org/bot" + c.Token + "/sendMessage", chatID: c.ChatID,
	}}
	if c.MySQLEnabled {
		dsn := mysql.NewConfig()
		dsn.User, dsn.Passwd = c.MySQLUser, c.MySQLPassword
		dsn.Net, dsn.Addr = "tcp", net.JoinHostPort(c.MySQLHost, strconv.Itoa(c.MySQLPort))
		dsn.Timeout, dsn.ReadTimeout, dsn.WriteTimeout = c.MySQLTimeout, c.MySQLTimeout, c.MySQLTimeout
		dsn.TLSConfig = c.MySQLTLS
		connector, err := mysql.NewConnector(dsn)
		if err != nil {
			return nil, fmt.Errorf("konfigurasi koneksi MySQL tidak valid")
		}
		a.db = sql.OpenDB(connector)
		a.db.SetMaxOpenConns(1)
		a.db.SetMaxIdleConns(1)
		a.db.SetConnMaxLifetime(5 * time.Minute)
	}
	return a, nil
}

func (a *App) Close() {
	if a.db != nil {
		a.db.Close()
	}
	a.telegram.client.CloseIdleConnections()
}

func (a *App) report(readings []Reading, title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nServer: %s\nWaktu: %s\n", title, a.cfg.Name, time.Now().In(a.cfg.Location).Format("2006-01-02 15:04:05 MST"))
	for _, r := range readings {
		status := "OK"
		if r.Bad {
			status = "ALERT"
		}
		fmt.Fprintf(&b, "\n[%s] %s", status, r.Text)
	}
	if !a.cfg.MySQLEnabled {
		b.WriteString("\n\nMySQL monitor: OFF")
	}
	return b.String()
}

func alertAction(s alertState, r Reading, now time.Time, cooldown time.Duration) string {
	if r.Bad && (!s.Active || now.Sub(s.LastSent) >= cooldown) {
		return "🚨 ALERT"
	}
	if !r.Bad && s.Active {
		return "✅ PULIH"
	}
	return ""
}

func (a *App) alerts(ctx context.Context, readings []Reading) {
	for _, r := range readings {
		now := time.Now()
		action := alertAction(a.states[r.Key], r, now, a.cfg.Cooldown)
		if action == "" {
			continue
		}
		if err := a.telegram.Send(ctx, a.report([]Reading{r}, action)); err != nil {
			slog.Error("gagal mengirim alert; dicoba lagi pada pemeriksaan berikutnya", "metric", r.Key, "error", err)
			continue
		}
		a.states[r.Key] = alertState{Active: r.Bad, LastSent: now}
	}
}

func (a *App) Run(ctx context.Context, once bool) error {
	if once {
		return a.telegram.Send(ctx, a.report(a.collect(ctx), "📊 Laporan manual"))
	}
	slog.Info("monitor aktif", "server", a.cfg.Name, "interval", a.cfg.Interval, "mysql_enabled", a.cfg.MySQLEnabled, "timezone", a.cfg.Location.String())
	next := nextReport(time.Now(), a.cfg)
	reportTimer := time.NewTimer(time.Until(next))
	defer reportTimer.Stop()
	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()
	a.alerts(ctx, a.collect(ctx))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			a.alerts(ctx, a.collect(ctx))
		case <-reportTimer.C:
			readings := a.collect(ctx)
			if err := a.telegram.Send(ctx, a.report(readings, "📊 Laporan harian")); err != nil {
				slog.Error("gagal mengirim laporan; dicoba lagi dalam 1 menit", "error", err)
				reportTimer.Reset(time.Minute)
			} else {
				next = nextReport(time.Now(), a.cfg)
				slog.Info("laporan harian terkirim", "next_report", next)
				reportTimer.Reset(time.Until(next))
			}
			a.alerts(ctx, readings)
		}
	}
}

package monitor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Name, Token, ChatID                            string
	Interval, CPUSample, Cooldown, TelegramTimeout time.Duration
	CPUThreshold, RAMThreshold, DiskThreshold      float64
	DiskPaths                                      []string
	Location                                       *time.Location
	ReportHour, ReportMinute                       int
	MySQLEnabled                                   bool
	MySQLHost, MySQLUser, MySQLPassword, MySQLTLS  string
	MySQLPort, LagThreshold                        int
	MySQLTimeout                                   time.Duration
}

func LoadConfig(path string) (Config, error) {
	if err := godotenv.Load(path); err != nil && !(path == ".env" && os.IsNotExist(err)) {
		return Config{}, fmt.Errorf("gagal membaca .env: %w", err)
	}
	return parseConfig(os.Getenv)
}

func parseConfig(get func(string) string) (c Config, err error) {
	value := func(key, fallback string) string {
		if v := strings.TrimSpace(get(key)); v != "" {
			return v
		}
		return fallback
	}
	duration := func(key, fallback string) time.Duration {
		v, e := time.ParseDuration(value(key, fallback))
		if e != nil || v <= 0 {
			err = fmt.Errorf("%s harus durasi positif, contoh 60s atau 30m", key)
		}
		return v
	}
	percent := func(key, fallback string) float64 {
		v, e := strconv.ParseFloat(value(key, fallback), 64)
		if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 100 {
			err = fmt.Errorf("%s harus angka > 0 dan <= 100", key)
		}
		return v
	}
	integer := func(key, fallback string, minimum, maximum int) int {
		v, e := strconv.Atoi(value(key, fallback))
		if e != nil || v < minimum || v > maximum {
			err = fmt.Errorf("%s harus angka %d sampai %d", key, minimum, maximum)
		}
		return v
	}
	c.Name = value("SERVER_NAME", "")
	if c.Name == "" {
		c.Name, _ = os.Hostname()
	}
	c.Token, c.ChatID = value("TELEGRAM_BOT_TOKEN", ""), value("TELEGRAM_CHAT_ID", "")
	if c.Token == "" || c.ChatID == "" {
		return c, fmt.Errorf("TELEGRAM_BOT_TOKEN dan TELEGRAM_CHAT_ID wajib diisi")
	}
	c.Interval = duration("CHECK_INTERVAL", "60s")
	c.CPUSample = duration("CPU_SAMPLE_DURATION", "1s")
	c.Cooldown = duration("ALERT_COOLDOWN", "30m")
	c.TelegramTimeout = duration("TELEGRAM_TIMEOUT", "15s")
	c.CPUThreshold = percent("CPU_THRESHOLD", "80")
	c.RAMThreshold = percent("RAM_THRESHOLD", "80")
	c.DiskThreshold = percent("DISK_THRESHOLD", "85")
	seen := map[string]bool{}
	for _, p := range strings.Split(value("DISK_PATHS", "/"), ",") {
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			return c, fmt.Errorf("DISK_PATHS harus berisi path absolut yang tidak kosong")
		}
		p = filepath.Clean(p)
		if !seen[p] {
			c.DiskPaths = append(c.DiskPaths, p)
			seen[p] = true
		}
	}
	loc, e := time.LoadLocation(value("TIMEZONE", "Asia/Makassar"))
	if e != nil {
		return c, fmt.Errorf("TIMEZONE tidak valid")
	}
	c.Location = loc
	t, e := time.Parse("15:04", value("REPORT_TIME", "06:00"))
	if e != nil {
		return c, fmt.Errorf("REPORT_TIME harus HH:MM, contoh 06:00")
	}
	c.ReportHour, c.ReportMinute = t.Hour(), t.Minute()
	c.MySQLEnabled, e = strconv.ParseBool(value("MYSQL_ENABLED", "false"))
	if e != nil {
		return c, fmt.Errorf("MYSQL_ENABLED harus true atau false")
	}
	if c.MySQLEnabled {
		c.MySQLHost = value("MYSQL_HOST", "127.0.0.1")
		c.MySQLPort = integer("MYSQL_PORT", "3306", 1, 65535)
		c.MySQLUser = value("MYSQL_USER", "server_monitor")
		c.MySQLPassword = get("MYSQL_PASSWORD")
		c.MySQLTLS = value("MYSQL_TLS", "false")
		if c.MySQLTLS != "true" && c.MySQLTLS != "false" {
			return c, fmt.Errorf("MYSQL_TLS harus true atau false")
		}
		c.LagThreshold = integer("MYSQL_LAG_THRESHOLD", "60", 0, 2147483647)
		c.MySQLTimeout = duration("MYSQL_TIMEOUT", "5s")
	}
	if c.CPUSample >= c.Interval {
		return c, fmt.Errorf("CPU_SAMPLE_DURATION harus lebih kecil dari CHECK_INTERVAL")
	}
	return c, err
}

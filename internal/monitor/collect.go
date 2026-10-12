package monitor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

type Reading struct {
	Key, Text string
	Bad       bool
}

func usage(key string, percent, limit float64) Reading {
	return Reading{key, fmt.Sprintf("%s: %.1f%% (batas %.1f%%)", key, percent, limit), percent > limit}
}

func (a *App) collect(ctx context.Context) []Reading {
	c := a.cfg
	var out []Reading
	p, err := cpu.PercentWithContext(ctx, c.CPUSample, false)
	if err != nil || len(p) == 0 {
		out = append(out, Reading{"CPU", "CPU: gagal membaca metrik", true})
	} else {
		out = append(out, usage("CPU", p[0], c.CPUThreshold))
	}
	m, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		out = append(out, Reading{"RAM", "RAM: gagal membaca metrik", true})
	} else {
		// Available memperhitungkan cache Linux yang bisa digunakan kembali.
		r := usage("RAM", 100*float64(m.Total-m.Available)/float64(m.Total), c.RAMThreshold)
		r.Text += fmt.Sprintf("; %.2f / %.2f GiB", float64(m.Total-m.Available)/(1<<30), float64(m.Total)/(1<<30))
		out = append(out, r)
	}
	for _, path := range c.DiskPaths {
		key := "Disk " + path
		d, err := disk.UsageWithContext(ctx, path)
		if err != nil {
			out = append(out, Reading{key, key + ": gagal membaca filesystem", true})
			continue
		}
		r := usage(key, d.UsedPercent, c.DiskThreshold)
		r.Text += fmt.Sprintf("; tersedia %.2f GiB", float64(d.Free)/(1<<30))
		out = append(out, r)
	}
	if c.MySQLEnabled {
		r, err := a.replication(ctx)
		if err != nil {
			out = append(out, Reading{"MySQL", "MySQL: " + err.Error(), true})
		} else {
			out = append(out, r)
		}
	}
	return out
}

func (a *App) replication(ctx context.Context) (Reading, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.MySQLTimeout)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, "SHOW REPLICA STATUS")
	if err != nil {
		var mysqlErr *mysql.MySQLError
		// Hanya fallback untuk syntax yang belum didukung server lama.
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1064 {
			rows, err = a.db.QueryContext(ctx, "SHOW SLAVE STATUS")
		}
	}
	if err != nil {
		return Reading{}, fmt.Errorf("gagal query status replikasi (cek koneksi dan hak REPLICATION CLIENT)")
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return Reading{}, fmt.Errorf("gagal membaca kolom status")
	}
	var channels []Reading
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		args := make([]any, len(columns))
		for i := range values {
			args[i] = &values[i]
		}
		if err := rows.Scan(args...); err != nil {
			return Reading{}, fmt.Errorf("gagal membaca status replikasi")
		}
		fields := make(map[string]sql.NullString, len(columns))
		for i, name := range columns {
			fields[name] = values[i]
		}
		channels = append(channels, replicationReading(fields, a.cfg.LagThreshold))
	}
	if err := rows.Err(); err != nil {
		return Reading{}, fmt.Errorf("pembacaan status replikasi terputus")
	}
	if len(channels) == 0 {
		return Reading{}, fmt.Errorf("replikasi tidak dikonfigurasi (status kosong)")
	}
	r := Reading{Key: "MySQL"}
	for i, channel := range channels {
		if i > 0 {
			r.Text += "\n"
		}
		r.Text += channel.Text
		r.Bad = r.Bad || channel.Bad
	}
	return r, nil
}

func replicationReading(fields map[string]sql.NullString, threshold int) Reading {
	field := func(modern, legacy string) sql.NullString {
		if v, ok := fields[modern]; ok {
			return v
		}
		return fields[legacy]
	}
	name := fields["Channel_Name"].String
	if name == "" {
		name = "default"
	}
	io := field("Replica_IO_Running", "Slave_IO_Running").String
	sqlThread := field("Replica_SQL_Running", "Slave_SQL_Running").String
	lag := field("Seconds_Behind_Source", "Seconds_Behind_Master")
	seconds, err := strconv.ParseInt(lag.String, 10, 64)
	bad := !lag.Valid || err != nil || seconds < 0 || seconds > int64(threshold) || io != "Yes" || sqlThread != "Yes"
	lagText := "NULL/tidak diketahui"
	if lag.Valid && err == nil && seconds >= 0 {
		lagText = fmt.Sprintf("%d detik", seconds)
	}
	return Reading{"MySQL", fmt.Sprintf("MySQL [%s]: lag %s (batas %d detik); IO=%s; SQL=%s", name, lagText, threshold, io, sqlThread), bad}
}

func nextReport(now time.Time, c Config) time.Time {
	local := now.In(c.Location)
	next := time.Date(local.Year(), local.Month(), local.Day(), c.ReportHour, c.ReportMinute, 0, 0, c.Location)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func testConfig(t *testing.T, overrides map[string]string) (Config, error) {
	t.Helper()
	values := map[string]string{"TELEGRAM_BOT_TOKEN": "test", "TELEGRAM_CHAT_ID": "123"}
	for k, v := range overrides {
		values[k] = v
	}
	return parseConfig(func(k string) string { return values[k] })
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	c, err := testConfig(t, nil)
	if err != nil || c.MySQLEnabled || c.ReportHour != 6 || c.Location.String() != "Asia/Makassar" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for _, input := range []map[string]string{
		{"CPU_THRESHOLD": "NaN"}, {"RAM_THRESHOLD": "101"}, {"DISK_THRESHOLD": "-1"},
		{"CHECK_INTERVAL": "0s"}, {"CPU_SAMPLE_DURATION": "60s"}, {"TIMEZONE": "unknown"},
		{"REPORT_TIME": "25:00"}, {"DISK_PATHS": "/,"}, {"MYSQL_ENABLED": "maybe"},
		{"MYSQL_ENABLED": "true", "MYSQL_PORT": "0"}, {"MYSQL_ENABLED": "true", "MYSQL_LAG_THRESHOLD": "-1"},
		{"MYSQL_ENABLED": "true", "MYSQL_TLS": "skip-verify"}, {"TELEGRAM_BOT_TOKEN": ""},
	} {
		if _, err := testConfig(t, input); err == nil {
			t.Errorf("accepted invalid config: %v", input)
		}
	}
	if _, err := testConfig(t, map[string]string{"MYSQL_PORT": "ignored when off"}); err != nil {
		t.Fatal(err)
	}
}

func TestNextReport(t *testing.T) {
	c, _ := testConfig(t, nil)
	for _, tc := range []struct{ now, want string }{
		{"2026-10-12T05:59:59+08:00", "2026-10-12T06:00:00+08:00"},
		{"2026-10-12T06:00:00+08:00", "2026-10-13T06:00:00+08:00"},
		{"2026-12-31T23:00:00+08:00", "2027-01-01T06:00:00+08:00"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := nextReport(now, c).Format(time.RFC3339); got != tc.want {
			t.Errorf("got %s want %s", got, tc.want)
		}
	}
}

func TestReplicationHealth(t *testing.T) {
	for _, modern := range []bool{false, true} {
		lag, io, sqlThread := "Seconds_Behind_Master", "Slave_IO_Running", "Slave_SQL_Running"
		if modern {
			lag, io, sqlThread = "Seconds_Behind_Source", "Replica_IO_Running", "Replica_SQL_Running"
		}
		for _, tc := range []struct {
			lag     string
			valid   bool
			io, sql string
			bad     bool
		}{
			{"0", true, "Yes", "Yes", false}, {"60", true, "Yes", "Yes", false},
			{"61", true, "Yes", "Yes", true}, {"", false, "Yes", "Yes", true},
			{"0", true, "Connecting", "Yes", true}, {"0", true, "Yes", "No", true},
			{"invalid", true, "Yes", "Yes", true}, {"-1", true, "Yes", "Yes", true},
		} {
			fields := map[string]sql.NullString{lag: {String: tc.lag, Valid: tc.valid}, io: {String: tc.io, Valid: true}, sqlThread: {String: tc.sql, Valid: true}}
			if got := replicationReading(fields, 60); got.Bad != tc.bad {
				t.Errorf("%+v got %+v", tc, got)
			}
		}
	}
}

func TestAlertLifecycleAndFailedDelivery(t *testing.T) {
	c, _ := testConfig(t, nil)
	a, _ := New(c)
	defer a.Close()
	fail := true
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if fail {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	a.telegram.url = srv.URL
	bad := []Reading{usage("CPU", 90, 80)}
	a.alerts(context.Background(), bad)
	if a.states["CPU"].Active {
		t.Fatal("failed delivery marked as sent")
	}
	fail = false
	a.alerts(context.Background(), bad)
	a.alerts(context.Background(), bad)
	if calls != 2 || !a.states["CPU"].Active {
		t.Fatal("cooldown/delivery wrong")
	}
	a.states["CPU"] = alertState{true, time.Now().Add(-c.Cooldown)}
	a.alerts(context.Background(), bad)
	if calls != 3 {
		t.Fatal("missing cooldown reminder")
	}
	a.alerts(context.Background(), []Reading{usage("CPU", 80, 80)})
	if calls != 4 || a.states["CPU"].Active {
		t.Fatal("missing recovery")
	}
	a.alerts(context.Background(), []Reading{usage("CPU", 70, 80)})
	if calls != 4 {
		t.Fatal("duplicate recovery")
	}
}

func TestTelegramResponseAndUnicode(t *testing.T) {
	message := strings.Repeat("🚨 server\n", 1000)
	var received strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["chat_id"] != "123" || len(utf16.Encode([]rune(body["text"]))) > 3500 {
			t.Error("invalid payload")
		}
		received.WriteString(body["text"])
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tg := &Telegram{client: srv.Client(), url: srv.URL, chatID: "123"}
	if err := tg.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if received.String() != message {
		t.Fatal("message truncated")
	}
	for _, body := range []string{`{"ok":false,"error_code":429}`, `invalid`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer s.Close()
			tg.url = s.URL
			if err := tg.Send(context.Background(), "hello"); err == nil {
				t.Fatal("API failure accepted")
			}
		})
	}
}

func TestOnceCollectsMetricsWithoutMySQL(t *testing.T) {
	c, err := testConfig(t, map[string]string{"CPU_SAMPLE_DURATION": "10ms"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.db != nil {
		t.Fatal("MySQL connection initialized while disabled")
	}
	var message string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		message += body["text"]
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	a.telegram.url = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CPU:", "RAM:", "Disk /:", "MySQL monitor: OFF", "Laporan manual"} {
		if !strings.Contains(message, want) {
			t.Errorf("missing %q in report", want)
		}
	}
	if strings.Contains(message, "gagal membaca") {
		t.Fatalf("metrics collection failed: %s", message)
	}
}

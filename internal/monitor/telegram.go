package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"unicode/utf16"
)

type Telegram struct {
	client      *http.Client
	url, chatID string
}

func (t *Telegram) Send(ctx context.Context, message string) error {
	// Batas Telegram 4096 karakter; gunakan margin dan hitung UTF-16.
	for _, part := range splitMessage(message, 3500) {
		body, _ := json.Marshal(map[string]string{"chat_id": t.chatID, "text": part})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("request Telegram tidak valid")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := t.client.Do(req)
		// Jangan log URL/error transport karena URL berisi token bot.
		if err != nil {
			return fmt.Errorf("koneksi Telegram gagal atau timeout")
		}
		var result struct {
			OK        bool `json:"ok"`
			ErrorCode int  `json:"error_code"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || !result.OK {
			return fmt.Errorf("Telegram menolak pesan (HTTP %d, kode %d)", resp.StatusCode, result.ErrorCode)
		}
	}
	return nil
}

func splitMessage(s string, limit int) []string {
	var parts []string
	start, units := 0, 0
	for i, r := range s {
		n := utf16.RuneLen(r)
		if units+n > limit {
			parts = append(parts, s[start:i])
			start, units = i, 0
		}
		units += n
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

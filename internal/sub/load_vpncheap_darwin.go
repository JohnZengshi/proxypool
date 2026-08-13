//go:build darwin

package sub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/john/proxypool/internal/config"
)

func loadVPNCheap(ctx context.Context, src config.Source) ([]byte, error) {
	if src.Path == "" {
		return nil, fmt.Errorf("vpncheap cache path is empty")
	}
	db, err := sql.Open("sqlite", vpncheapCacheDSN(src.Path))
	if err != nil {
		return nil, fmt.Errorf("open vpncheap cache %s: %w", src.Path, err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT r.entry_ID, r.time_stamp, d.receiver_data
		FROM cfurl_cache_response r
		JOIN cfurl_cache_receiver_data d ON d.entry_ID = r.entry_ID
		WHERE d.isDataOnFS = 1 AND d.receiver_data IS NOT NULL
		ORDER BY r.time_stamp DESC, r.entry_ID DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query vpncheap cache %s: %w", src.Path, err)
	}
	defer rows.Close()

	var lastErr error
	for rows.Next() {
		var id int64
		var timestamp string
		var receiverData []byte
		if err := rows.Scan(&id, &timestamp, &receiverData); err != nil {
			return nil, fmt.Errorf("read vpncheap cache %s: %w", src.Path, err)
		}
		payloadPath, err := vpncheapCachePayloadPath(src.Path, string(receiverData))
		if err != nil {
			lastErr = err
			continue
		}
		data, err := os.ReadFile(payloadPath)
		if err != nil {
			lastErr = fmt.Errorf("read vpncheap cache data %s: %w", payloadPath, err)
			continue
		}
		if err := vpncheapSingBoxResponse(data); err != nil {
			lastErr = fmt.Errorf("parse vpncheap cache response %s: %w", payloadPath, err)
			continue
		}
		return data, nil
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read vpncheap cache %s: %w", src.Path, err)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("vpncheap cache %s: no cached sing-box response", src.Path)
}

func vpncheapCacheDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("mode", "ro")
	q.Add("_pragma", "busy_timeout(3000)")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	return u.String()
}

func vpncheapCachePayloadPath(dbPath, receiverData string) (string, error) {
	if !vpncheapCacheUUID(receiverData) {
		return "", fmt.Errorf("vpncheap cache %s: unsafe payload reference", dbPath)
	}
	payloadDir := filepath.Join(filepath.Dir(dbPath), "fsCachedData")
	payloadPath := filepath.Join(payloadDir, receiverData)
	if !vpncheapPathWithin(payloadDir, payloadPath) {
		return "", fmt.Errorf("vpncheap cache %s: payload reference escapes fsCachedData", dbPath)
	}
	return payloadPath, nil
}

func vpncheapPathWithin(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func vpncheapCacheUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !vpncheapHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func vpncheapHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func vpncheapSingBoxResponse(data []byte) error {
	var doc struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Outbounds) == 0 {
		return fmt.Errorf("no usable outbounds")
	}
	return nil
}

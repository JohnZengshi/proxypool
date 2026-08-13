//go:build darwin

package sub

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/john/proxypool/internal/config"
)

type vpncheapDarwinCacheRow struct {
	timestamp string
	onDisk    int
	ref       string
	payload   []byte
}

func TestLoadVPNCheapDarwinNewestValid(t *testing.T) {
	newest := vpncheapDarwinSingBox("newest.example.com", "synthetic-secret-newest")
	older := vpncheapDarwinSingBox("older.example.com", "synthetic-secret-older")
	dbPath := writeVPNCheapCache(t, t.TempDir(),
		vpncheapDarwinCacheRow{
			timestamp: "2026-08-01 10:00:00",
			onDisk:    1,
			ref:       "11111111-1111-4111-8111-111111111111",
			payload:   newest,
		},
		vpncheapDarwinCacheRow{
			timestamp: "2026-07-01 10:00:00",
			onDisk:    1,
			ref:       "22222222-2222-4222-8222-222222222222",
			payload:   older,
		},
	)

	data, err := loadVPNCheap(context.Background(), config.Source{
		Tag:  "vpncheap",
		Type: config.SourceVPNCheap,
		Path: dbPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, newest) {
		t.Fatal("expected newest cached payload to be returned unchanged")
	}
	result, err := Parse(config.Source{Tag: "vpncheap", Type: config.SourceVPNCheap}, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Server != "newest.example.com" || result.Nodes[0].Port != 8388 {
		t.Fatalf("unexpected parsed nodes: %+v", result.Nodes)
	}
}

func TestLoadVPNCheapDarwinSkipsInvalidNewest(t *testing.T) {
	older := vpncheapDarwinSingBox("older.example.com", "synthetic-secret-older")
	tests := []struct {
		name   string
		newest []byte
	}{
		{
			name:   "malformed_json",
			newest: []byte(`{"secret":"SUPERSECRET-DARWIN",`),
		},
		{
			name:   "empty_outbounds",
			newest: []byte(`{"outbounds":[]}`),
		},
		{
			name:   "missing_payload_file",
			newest: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := writeVPNCheapCache(t, t.TempDir(),
				vpncheapDarwinCacheRow{
					timestamp: "2026-08-01 10:00:00",
					onDisk:    1,
					ref:       "33333333-3333-4333-8333-333333333333",
					payload:   tt.newest,
				},
				vpncheapDarwinCacheRow{
					timestamp: "2026-07-01 10:00:00",
					onDisk:    1,
					ref:       "44444444-4444-4444-8444-444444444444",
					payload:   older,
				},
			)

			data, err := loadVPNCheap(context.Background(), config.Source{Path: dbPath})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, older) {
				t.Fatal("expected loader to fall back to older valid payload")
			}
		})
	}
}

func TestLoadVPNCheapDarwinErrors(t *testing.T) {
	tests := []struct {
		name   string
		dbPath string
		want   string
		secret string
	}{
		{
			name:   "missing_database",
			dbPath: filepath.Join(t.TempDir(), "missing", "Cache.db"),
			want:   "query vpncheap cache",
		},
		{
			name: "unsafe_uuid",
			dbPath: writeVPNCheapCache(t, t.TempDir(), vpncheapDarwinCacheRow{
				timestamp: "2026-08-01 10:00:00",
				onDisk:    1,
				ref:       "../escape",
			}),
			want: "unsafe payload reference",
		},
		{
			name: "missing_payload_file",
			dbPath: writeVPNCheapCache(t, t.TempDir(), vpncheapDarwinCacheRow{
				timestamp: "2026-08-01 10:00:00",
				onDisk:    1,
				ref:       "55555555-5555-4555-8555-555555555555",
			}),
			want: "read vpncheap cache data",
		},
		{
			name: "malformed_payload",
			dbPath: writeVPNCheapCache(t, t.TempDir(), vpncheapDarwinCacheRow{
				timestamp: "2026-08-01 10:00:00",
				onDisk:    1,
				ref:       "66666666-6666-4666-8666-666666666666",
				payload:   []byte(`{"secret":"SUPERSECRET-DARWIN",`),
			}),
			want:   "parse vpncheap cache response",
			secret: "SUPERSECRET-DARWIN",
		},
		{
			name: "empty_outbounds",
			dbPath: writeVPNCheapCache(t, t.TempDir(), vpncheapDarwinCacheRow{
				timestamp: "2026-08-01 10:00:00",
				onDisk:    1,
				ref:       "77777777-7777-4777-8777-777777777777",
				payload:   []byte(`{"outbounds":[]}`),
			}),
			want: "no usable outbounds",
		},
		{
			name: "no_external_records",
			dbPath: writeVPNCheapCache(t, t.TempDir(), vpncheapDarwinCacheRow{
				timestamp: "2026-08-01 10:00:00",
				onDisk:    0,
				ref:       "88888888-8888-4888-8888-888888888888",
				payload:   []byte(`{"outbounds":[]}`),
			}),
			want: "no cached sing-box response",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadVPNCheap(context.Background(), config.Source{Path: tt.dbPath})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q should contain %q", err.Error(), tt.want)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Fatalf("error leaked cache content: %v", err)
			}
		})
	}
}

func TestLoadVPNCheapDarwinLockedDatabase(t *testing.T) {
	payload := vpncheapDarwinSingBox("locked.example.com", "synthetic-secret-locked")
	dbPath := writeVPNCheapCache(t, t.TempDir(), vpncheapDarwinCacheRow{
		timestamp: "2026-08-01 10:00:00",
		onDisk:    1,
		ref:       "12121212-1212-4121-8121-121212121212",
		payload:   payload,
	})
	lock, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = lock.Exec(`COMMIT`)
		_ = lock.Close()
	})

	_, err = loadVPNCheap(context.Background(), config.Source{Path: dbPath})
	if err == nil {
		t.Fatal("expected locked cache database to fail")
	}
	if !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("unexpected locked database error: %v", err)
	}
}

func TestLoadVPNCheapDarwinNoNetworkFallback(t *testing.T) {
	payload := vpncheapDarwinSingBox("local.example.com", "synthetic-secret-local")
	dbPath := writeVPNCheapCache(t, t.TempDir(), vpncheapDarwinCacheRow{
		timestamp: "2026-08-01 10:00:00",
		onDisk:    1,
		ref:       "99999999-9999-4999-8999-999999999999",
		payload:   payload,
	})
	data, err := loadVPNCheap(context.Background(), config.Source{
		Path: dbPath,
		URL:  "http://127.0.0.1:1/cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("expected local cache payload")
	}
}

func TestLoadVPNCheapDarwinConfigDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	current := filepath.Join(home, "Library", "Containers", "com.vpncheap.macnative", "Data", "Library", "Caches", "com.vpncheap.macnative", "Cache.db")
	payload := vpncheapDarwinSingBox("discovered.example.com", "synthetic-secret-discovered")
	writeVPNCheapCache(t, filepath.Dir(current), vpncheapDarwinCacheRow{
		timestamp: "2026-08-01 10:00:00",
		onDisk:    1,
		ref:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		payload:   payload,
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("sources:\n  - tag: vpncheap\n    type: vpncheap\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources[0].Path != current {
		t.Fatalf("expected discovered path %q, got %q", current, cfg.Sources[0].Path)
	}
	data, err := loadVPNCheap(context.Background(), cfg.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("expected discovered cache payload")
	}
}

func writeVPNCheapCache(t *testing.T, dir string, rows ...vpncheapDarwinCacheRow) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "fsCachedData"), 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "Cache.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE cfurl_cache_response(entry_ID INTEGER PRIMARY KEY AUTOINCREMENT UNIQUE, version INTEGER, hash_value INTEGER, storage_policy INTEGER, request_key TEXT UNIQUE, time_stamp NOT NULL DEFAULT CURRENT_TIMESTAMP, partition TEXT)`,
		`CREATE TABLE cfurl_cache_receiver_data(entry_ID INTEGER PRIMARY KEY, isDataOnFS INTEGER, receiver_data BLOB)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for i, row := range rows {
		entryID := int64(i + 1)
		if _, err := db.Exec(
			`INSERT INTO cfurl_cache_response(entry_ID, version, hash_value, storage_policy, request_key, time_stamp, partition) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			entryID, 0, 0, 0, fmt.Sprintf("https://cache.example/%d", i), row.timestamp, "",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO cfurl_cache_receiver_data(entry_ID, isDataOnFS, receiver_data) VALUES (?, ?, ?)`,
			entryID, row.onDisk, row.ref,
		); err != nil {
			t.Fatal(err)
		}
		if len(row.payload) > 0 {
			if err := os.WriteFile(filepath.Join(dir, "fsCachedData", row.ref), row.payload, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dbPath
}

func vpncheapDarwinSingBox(server, secret string) []byte {
	return []byte(fmt.Sprintf(
		`{"outbounds":[{"type":"shadowsocks","tag":"ss","server":"%s","server_port":8388,"method":"aes-128-gcm","password":"%s"}]}`,
		server,
		secret,
	))
}

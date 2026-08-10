//go:build windows

package sub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/john/proxypool/internal/config"
)

func TestLoadVPNCheapCacheWith(t *testing.T) {
	plain := vpncheapPlaintext(t,
		`{"type":"vmess","tag":"vm1","server":"a.com","server_port":443,"uuid":"u","security":"auto"}`,
		`{"type":"direct","tag":"direct"}`,
	)
	state := []byte(`{"secret":"SUPERSECRET","xboard_nodes_enc":"dpapi1:ZmFrZQ=="}`)
	data, err := loadVPNCheapCacheWith(
		`C:\cache\app_state.json`,
		func(string) ([]byte, error) { return state, nil },
		func(encoded []byte) ([]byte, error) {
			if string(encoded) != "dpapi1:ZmFrZQ==" {
				t.Fatalf("unexpected encoded cache %q", encoded)
			}
			return plain, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	var doc rawOutbounds
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Outbounds) != 2 {
		t.Fatalf("expected 2 raw outbounds, got %d", len(doc.Outbounds))
	}

	result, err := Parse(config.Source{Tag: "vpncheap", Type: config.SourceVPNCheap}, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 dialable node, got %d", len(result.Nodes))
	}
	if result.Nodes[0].Server != "a.com" || result.Nodes[0].Port != 443 {
		t.Fatalf("unexpected node %+v", result.Nodes[0])
	}
	if result.Skipped != 1 {
		t.Fatalf("expected 1 skipped non-dialable outbound, got %d", result.Skipped)
	}
}

func TestLoadVPNCheapCacheWithErrors(t *testing.T) {
	validState := []byte(`{"secret":"SUPERSECRET","xboard_nodes_enc":"dpapi1:ZmFrZQ=="}`)
	tests := []struct {
		name    string
		read    readCacheFile
		decrypt decryptCache
		want    string
	}{
		{
			name: "missing_file",
			read: func(string) ([]byte, error) {
				return nil, os.ErrNotExist
			},
			want: "read vpncheap cache",
		},
		{
			name: "malformed_json",
			read: func(string) ([]byte, error) {
				return []byte(`{"secret":"SUPERSECRET",`), nil
			},
			want: "parse vpncheap cache",
		},
		{
			name: "missing_cache_field",
			read: func(string) ([]byte, error) {
				return []byte(`{"secret":"SUPERSECRET"}`), nil
			},
			want: "missing xboard_nodes_enc",
		},
		{
			name: "bad_prefix",
			read: func(string) ([]byte, error) {
				return []byte(`{"xboard_nodes_enc":"plain"}`), nil
			},
			decrypt: decryptVPNCheapCache,
			want:    "unsupported encoding prefix",
		},
		{
			name: "decrypt_error",
			read: func(string) ([]byte, error) {
				return validState, nil
			},
			decrypt: func([]byte) ([]byte, error) { return nil, errors.New("decrypt failed") },
			want:    "decrypt failed",
		},
		{
			name: "invalid_raw_outbound",
			read: func(string) ([]byte, error) {
				return validState, nil
			},
			decrypt: func([]byte) ([]byte, error) {
				return vpncheapPlaintext(t, "null"), nil
			},
			want: "node 0 has no raw_outbound",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app_state.json")
			_, err := loadVPNCheapCacheWith(path, tt.read, tt.decrypt)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q should contain %q", err.Error(), tt.want)
			}
			if strings.Contains(err.Error(), "SUPERSECRET") {
				t.Fatalf("error leaked cache content: %v", err)
			}
		})
	}
}

func vpncheapPlaintext(t *testing.T, raws ...string) []byte {
	t.Helper()
	nodes := make([]vpncheapCacheNode, 0, len(raws))
	for _, raw := range raws {
		nodes = append(nodes, vpncheapCacheNode{RawOutbound: json.RawMessage(raw)})
	}
	data, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

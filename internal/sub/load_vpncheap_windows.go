//go:build windows

package sub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"github.com/john/proxypool/internal/config"
	"golang.org/x/sys/windows"
)

const vpncheapCachePrefix = "dpapi1:"

var vpncheapEntropy = []byte("vpncheap/ingress-cache/v1")

type readCacheFile func(string) ([]byte, error)
type decryptCache func([]byte) ([]byte, error)

func loadVPNCheap(_ context.Context, src config.Source) ([]byte, error) {
	if src.Path == "" {
		return nil, fmt.Errorf("vpncheap cache path is empty")
	}
	return loadVPNCheapCacheWith(src.Path, os.ReadFile, decryptVPNCheapCache)
}

func loadVPNCheapCacheWith(path string, read readCacheFile, decrypt decryptCache) ([]byte, error) {
	raw, err := read(path)
	if err != nil {
		return nil, fmt.Errorf("read vpncheap cache %s: %w", path, err)
	}
	var state struct {
		NodesEnc string `json:"xboard_nodes_enc"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse vpncheap cache %s: %w", path, err)
	}
	if state.NodesEnc == "" {
		return nil, fmt.Errorf("vpncheap cache %s: missing xboard_nodes_enc", path)
	}
	plain, err := decrypt([]byte(state.NodesEnc))
	if err != nil {
		return nil, err
	}
	return convertVPNCheapCache(plain)
}

func decryptVPNCheapCache(encoded []byte) ([]byte, error) {
	if !bytes.HasPrefix(encoded, []byte(vpncheapCachePrefix)) {
		return nil, errors.New("vpncheap cache: unsupported encoding prefix")
	}
	data, err := base64.StdEncoding.DecodeString(string(encoded[len(vpncheapCachePrefix):]))
	if err != nil {
		return nil, fmt.Errorf("vpncheap cache: base64 decode failed: %w", err)
	}
	return dpapiUnprotect(data, vpncheapEntropy)
}

func dpapiUnprotect(data, entropy []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("vpncheap cache: empty protected data")
	}
	input := windows.DataBlob{Data: &data[0], Size: uint32(len(data))}
	var ent windows.DataBlob
	if len(entropy) > 0 {
		ent = windows.DataBlob{Data: &entropy[0], Size: uint32(len(entropy))}
	}
	var output windows.DataBlob
	err := windows.CryptUnprotectData(&input, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	if err != nil {
		return nil, fmt.Errorf("vpncheap cache: decrypt failed: %w", err)
	}
	if output.Data != nil && output.Size > 0 {
		defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
		return bytes.Clone(unsafe.Slice(output.Data, int(output.Size))), nil
	}
	return nil, errors.New("vpncheap cache: decrypt returned no data")
}

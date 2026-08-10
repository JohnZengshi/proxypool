package sub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/john/proxypool/internal/config"
	singjson "github.com/sagernet/sing/common/json"
)

// SourceLoader loads one configured source body. Manager uses this seam so
// tests can inject cache and network loaders without touching production code.
type SourceLoader func(context.Context, config.Source) ([]byte, error)

// Load fetches or reads a configured source according to its type and platform.
func Load(ctx context.Context, src config.Source) ([]byte, error) {
	if src.Type == config.SourceVPNCheap {
		return loadVPNCheap(ctx, src)
	}
	return Fetch(ctx, src.URL)
}

type vpncheapCacheNode struct {
	RawOutbound json.RawMessage `json:"raw_outbound"`
}

func convertVPNCheapCache(plain []byte) ([]byte, error) {
	var nodes []vpncheapCacheNode
	if err := json.Unmarshal(plain, &nodes); err != nil {
		return nil, fmt.Errorf("parse vpncheap nodes: %w", err)
	}
	outbounds := make([]singjson.RawMessage, 0, len(nodes))
	for i, n := range nodes {
		if len(n.RawOutbound) == 0 || string(n.RawOutbound) == "null" {
			return nil, fmt.Errorf("parse vpncheap nodes: node %d has no raw_outbound", i)
		}
		outbounds = append(outbounds, singjson.RawMessage(n.RawOutbound))
	}
	return json.Marshal(rawOutbounds{Outbounds: outbounds})
}

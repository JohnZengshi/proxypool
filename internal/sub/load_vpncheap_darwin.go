//go:build darwin

package sub

import (
	"context"

	"github.com/john/proxypool/internal/config"
)

func loadVPNCheap(ctx context.Context, src config.Source) ([]byte, error) {
	return Fetch(ctx, src.URL)
}

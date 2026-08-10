//go:build !windows && !darwin

package sub

import (
	"context"
	"fmt"
	"runtime"

	"github.com/john/proxypool/internal/config"
)

func loadVPNCheap(_ context.Context, _ config.Source) ([]byte, error) {
	return nil, fmt.Errorf("vpncheap source is not supported on %s", runtime.GOOS)
}

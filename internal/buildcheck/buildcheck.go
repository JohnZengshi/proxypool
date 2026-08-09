package buildcheck

import (
	"context"
	"errors"
	"fmt"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

// VerifyBuildTags returns a non-nil error if the binary was built without the
// tags required to dial every protocol in the pool's subscriptions. It must not
// touch the network.
func VerifyBuildTags() error {
	registry := include.OutboundRegistry()
	ctx := service.ContextWith[option.OutboundOptionsRegistry](context.Background(), registry)
	_, err := registry.CreateOutbound(ctx, nil, log.NewNOPFactory().Logger(), "selfcheck", "hysteria2", &option.Hysteria2OutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 1},
		Password:      "selfcheck",
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: &option.OutboundTLSOptions{Enabled: true, Insecure: true},
		},
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, constant.ErrQUICNotIncluded) {
		return fmt.Errorf("built without required tags: hysteria2 unavailable; rebuild with -tags \"with_quic with_utls\"")
	}
	return nil
}

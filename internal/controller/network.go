package controller

import (
	"context"
	"remoteops/pkg/protocol"
	"tailscale.com/client/local"
	"time"
)

func (s *Server) networkStatus(ctx context.Context) protocol.NetworkStatus {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result := protocol.NetworkStatus{State: "unavailable", Addresses: []string{}}
	status, err := (&local.Client{}).Status(ctx)
	if err != nil {
		result.Error = "无法读取本机 Tailscale 状态，请确认官方客户端已登录并运行"
		return result
	}
	result.State = "disconnected"
	if status.BackendState == "Running" {
		result.State = "connected"
	}
	for _, address := range status.TailscaleIPs {
		result.Addresses = append(result.Addresses, address.String())
	}
	return result
}

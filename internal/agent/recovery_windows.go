package agent

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func (a *Agent) ensureTunnel(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c := a.Config()
	manager, err := mgr.Connect()
	if err != nil {
		return "", err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService("WireGuardTunnel$" + c.Interface)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) && c.TunnelConfig != "" {
		// Only recreate a tunnel whose configuration is owned by this agent.
		if err = a.installTunnel(); err != nil {
			return "", err
		}
		return "已自动重新安装受管 WireGuard 隧道", nil
	}
	if err != nil {
		return "", err
	}
	defer service.Close()
	return startStoppedTunnel(service)
}

type tunnelService interface {
	Query() (svc.Status, error)
	Start(...string) error
}

func startStoppedTunnel(service tunnelService) (string, error) {
	status, err := service.Query()
	if err != nil {
		return "", err
	}
	if status.State != svc.Stopped {
		return "", nil
	} // Running or transitioning: do not reset networking.
	if err = service.Start(); err != nil {
		return "", err
	}
	return "已自动启动停止的 WireGuard 隧道服务", nil
}

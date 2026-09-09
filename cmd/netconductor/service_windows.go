package main

import (
	"context"
	"golang.org/x/sys/windows/svc"
)

type service struct{ run func(context.Context) }

func (s service) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status <- svc.Status{State: svc.StartPending}
	go s.run(ctx)
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for r := range requests {
		switch r.Cmd {
		case svc.Interrogate:
			status <- r.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}
	return false, 0
}
func runService(run func(context.Context)) bool {
	yes, e := svc.IsWindowsService()
	if e == nil && yes {
		svc.Run("NetConductorAgent", service{run})
		return true
	}
	return false
}

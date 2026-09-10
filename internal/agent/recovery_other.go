//go:build !windows

package agent

import "context"

func (a *Agent) ensureTunnel(context.Context) (string, error) { return "", nil }

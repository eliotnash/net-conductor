package agent

import (
	"context"
	"time"
)

// Failures back off without treating loss of Internet or a proxy as a broken tunnel.
type recoveryBackoff struct {
	next  time.Time
	delay time.Duration
}

func (b *recoveryBackoff) record(now time.Time, failed bool) {
	if !failed {
		b.delay = 0
		b.next = now.Add(30 * time.Second)
		return
	}
	if b.delay == 0 {
		b.delay = 30 * time.Second
	} else {
		b.delay *= 2
	}
	if b.delay > 5*time.Minute {
		b.delay = 5 * time.Minute
	}
	b.next = now.Add(b.delay)
}
func (a *Agent) recoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var backoff recoveryBackoff
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.Before(backoff.next) {
				continue
			}
			a.op.Lock()
			c := a.Config()
			if c.ID == "" {
				a.op.Unlock()
				continue
			}
			attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
			action, err := a.ensureTunnel(attempt)
			cancel()
			a.op.Unlock()
			backoff.record(now, err != nil)
			if action != "" || err != nil {
				a.mu.Lock()
				if err != nil {
					a.event("recovery.failed", "隧道自动恢复未完成，将延迟重试；请检查隧道服务及权限")
				} else {
					a.event("recovery.ok", action)
				}
				a.mu.Unlock()
			}
		}
	}
}

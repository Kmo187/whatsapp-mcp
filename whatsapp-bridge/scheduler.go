package main

import (
	"context"
	"fmt"
	"time"
)

// maxScheduleLateness: messages that are more than this overdue (for example
// because the computer was off) are not sent silently days later.
const maxScheduleLateness = 6 * time.Hour

// runScheduler sends due scheduled messages until ctx is cancelled.
func (a *App) runScheduler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sendDueScheduled(ctx, time.Now())
		}
	}
}

// sendDueScheduled sends every pending message whose time has come. It returns how many were attempted.
func (a *App) sendDueScheduled(ctx context.Context, now time.Time) int {
	if a.client == nil || !a.client.IsConnected() {
		return 0
	}
	due, err := a.store.DueScheduled(now)
	if err != nil {
		a.log.Warnf("Scheduler: failed to load due messages: %v", err)
		return 0
	}
	for _, m := range due {
		if now.Sub(m.SendAt) > maxScheduleLateness {
			a.store.FinishScheduled(m.ID, fmt.Errorf("skipped: bridge was offline at %s and the message is more than %s overdue", m.SendAt.Local().Format("2006-01-02 15:04"), maxScheduleLateness))
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err := a.send(sendCtx, SendRequest{Recipient: m.Recipient, Message: m.Message, MediaPath: m.MediaPath, ReplyTo: m.ReplyTo})
		cancel()
		if ferr := a.store.FinishScheduled(m.ID, err); ferr != nil {
			a.log.Warnf("Scheduler: failed to record result for #%d: %v", m.ID, ferr)
		}
		if err != nil {
			a.log.Warnf("Scheduler: message #%d failed: %v", m.ID, err)
		} else {
			fmt.Printf("Scheduler: sent scheduled message #%d to %s\n", m.ID, m.Recipient)
		}
	}
	return len(due)
}

// Package scheduler fires the daily mailbox check at a fixed local time and
// survives clock changes and notebook sleep, because it re-evaluates the next
// run time on every tick instead of relying on a single long timer.
package scheduler

import (
	"context"
	"sync"
	"time"
)

// DefaultTick is the resolution of the scheduler loop.
const DefaultTick = 20 * time.Second

// Scheduler runs a callback once a day at Hour:Minute.
type Scheduler struct {
	Hour     int
	Minute   int
	Location *time.Location
	Tick     time.Duration
	Now      func() time.Time
	OnRun    func(ctx context.Context)

	mu    sync.Mutex
	next  time.Time
	timer *time.Timer
	stop  chan struct{}
	done  chan struct{}
}

// New returns a scheduler configured with the given time of day.
func New(hour, minute int) *Scheduler {
	return &Scheduler{Hour: hour, Minute: minute, Now: time.Now}
}

func (s *Scheduler) loc() *time.Location {
	if s.Location != nil {
		return s.Location
	}
	return time.Local
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Next returns the first run time strictly after the given moment.
func (s *Scheduler) Next(after time.Time) time.Time {
	loc := s.loc()
	after = after.In(loc)
	candidate := time.Date(after.Year(), after.Month(), after.Day(), s.Hour, s.Minute, 0, 0, loc)
	if !candidate.After(after) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

// NextRun returns the planned moment of the next check (zero before Start).
func (s *Scheduler) NextRun() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// Start launches the background loop. Calling Start twice is a no-op.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	s.stop = stop
	s.done = done
	s.next = s.Next(s.now())
	s.mu.Unlock()

	go s.loop(ctx, stop, done)
}

func (s *Scheduler) loop(ctx context.Context, stop chan struct{}, done chan struct{}) {
	defer close(done)
	tick := s.Tick
	if tick <= 0 {
		tick = DefaultTick
	}
	timer := time.NewTimer(tick)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-timer.C:
		}

		s.mu.Lock()
		now := s.now()
		next := s.next
		if next.IsZero() {
			next = s.Next(now)
		}
		due := !now.Before(next)
		if due {
			s.next = s.Next(now)
			next = s.next
		}
		s.mu.Unlock()

		if due && s.OnRun != nil {
			runCtx := ctx
			s.OnRun(runCtx)
		}
		delay := tick
		if until := time.Until(next); until > 0 && until < delay {
			delay = until
		}
		if delay < time.Second {
			delay = time.Second
		}
		timer.Reset(delay)
	}
}

// Stop terminates the loop and waits for it to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	stop := s.stop
	done := s.done
	s.stop = nil
	s.done = nil
	s.next = time.Time{}
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

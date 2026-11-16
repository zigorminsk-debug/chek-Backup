package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	s := New(9, 30)
	s.Location = time.UTC

	cases := []struct {
		name string
		from time.Time
		want time.Time
	}{
		{
			name: "до времени проверки",
			from: time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC),
		},
		{
			name: "ровно в момент проверки уходим на следующие сутки",
			from: time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC),
			want: time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC),
		},
		{
			name: "после времени проверки",
			from: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC),
		},
		{
			name: "переход через конец месяца",
			from: time.Date(2026, 11, 30, 23, 59, 0, 0, time.UTC),
			want: time.Date(2026, 12, 1, 9, 30, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := s.Next(c.from)
			if !got.Equal(c.want) {
				t.Fatalf("Next(%s) = %s, хотим %s", c.from, got, c.want)
			}
		})
	}
}

func TestFiresOnceAtScheduledTime(t *testing.T) {
	base := time.Date(2026, 11, 16, 9, 29, 50, 0, time.UTC)
	var mu sync.Mutex
	now := base

	fired := make(chan time.Time, 4)
	s := New(9, 30)
	s.Location = time.UTC
	s.Tick = 5 * time.Millisecond
	s.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	s.OnRun = func(ctx context.Context) {
		fired <- s.Now()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	mu.Lock()
	now = base.Add(12 * time.Second)
	mu.Unlock()

	select {
	case at := <-fired:
		if at.Hour() != 9 || at.Minute() != 30 {
			t.Fatalf("проверка сработала в %s", at)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("проверка не сработала по расписанию")
	}

	select {
	case at := <-fired:
		t.Fatalf("повторный запуск в тот же день: %s", at)
	case <-time.After(60 * time.Millisecond):
	}
}

func TestNextRunIsExposed(t *testing.T) {
	s := New(7, 15)
	s.Location = time.UTC
	s.Tick = time.Hour
	s.Now = func() time.Time { return time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC) }
	s.OnRun = func(ctx context.Context) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	want := time.Date(2026, 11, 16, 7, 15, 0, 0, time.UTC)
	if got := s.NextRun(); !got.Equal(want) {
		t.Fatalf("NextRun() = %s, хотим %s", got, want)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	s := New(10, 0)
	s.OnRun = func(ctx context.Context) {}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	s.Stop()
	s.Stop()
}

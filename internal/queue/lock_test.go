package queue

import (
	"errors"
	"os"
	"testing"
)

func TestLockScheduler(t *testing.T) {
	s := bare(t)
	if _, err := s.Scheduler(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Scheduler before any ran = %v", err)
	}
	unlock, err := s.LockScheduler()
	if err != nil {
		t.Fatal(err)
	}
	if pid, err := s.Scheduler(); err != nil || pid != os.Getpid() {
		t.Errorf("Scheduler = %d, %v", pid, err)
	}
	if _, err := s.LockScheduler(); !errors.Is(err, ErrRunning) {
		t.Errorf("second lock = %v, want ErrRunning", err)
	}
	// Another repo has its own scheduler.
	other, err := bare(t).LockScheduler()
	if err != nil {
		t.Errorf("a second repo's lock = %v", err)
	} else {
		other()
	}
	unlock()
	if _, err := s.Scheduler(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Scheduler after it stopped = %v", err)
	}
}

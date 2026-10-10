package queue

import (
	"testing"
	"time"
)

func TestStuck(t *testing.T) {
	s := bare(t)
	if st, err := s.Stuck(); err != nil || st != nil {
		t.Fatalf("Stuck before = %+v, %v", st, err)
	}
	for i, msg := range []string{"bad config", "bad config"} {
		if err := s.SetStuck(msg, time.Unix(int64(100+i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := s.Stuck(); st == nil || st.Error != "bad config" || st.Since.Unix() != 100 {
		t.Errorf("Stuck = %+v, want it since the first time", st)
	}
	if err := s.SetStuck("worse", time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stuck(); st.Since.Unix() != 200 {
		t.Errorf("a new error kept the old time: %+v", st)
	}
	if err := s.SetStuck("", time.Unix(300, 0)); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stuck(); st != nil {
		t.Errorf("Stuck after clearing = %+v", st)
	}
	if err := s.SetStuck("", time.Unix(300, 0)); err != nil {
		t.Errorf("clearing twice = %v", err)
	}
}

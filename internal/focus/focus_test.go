package focus

import (
	"path/filepath"
	"testing"
)

func TestFocus(t *testing.T) {
	repo := t.TempDir()
	f := In(repo)
	if f.Path != filepath.Join(repo, ".diatom", "focus.yaml") {
		t.Errorf("In = %s", f.Path)
	}
	if fc, err := f.Read(); err != nil || fc != (Focus{}) {
		t.Errorf("Read before Write = %+v, %v", fc, err)
	}
	want := Focus{Goal: "set"}
	if err := f.Write(want); err != nil {
		t.Fatal(err)
	}
	if fc, err := f.Read(); err != nil || fc != want {
		t.Errorf("Read = %+v, %v", fc, err)
	}
}

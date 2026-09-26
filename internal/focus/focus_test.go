package focus

import (
	"path/filepath"
	"testing"
)

func TestFocus(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "diatom", "focus.yaml")}
	if fc, err := f.Read(); err != nil || fc != (Focus{}) {
		t.Errorf("Read before Write = %+v, %v", fc, err)
	}
	want := Focus{Repo: "/code/vex", Goal: "set"}
	if err := f.Write(want); err != nil {
		t.Fatal(err)
	}
	if fc, err := f.Read(); err != nil || fc != want {
		t.Errorf("Read = %+v, %v", fc, err)
	}
	t.Setenv("XDG_STATE_HOME", "/state")
	if d, _ := Default(); d.Path != "/state/diatom/focus.yaml" {
		t.Errorf("Default = %s", d.Path)
	}
}

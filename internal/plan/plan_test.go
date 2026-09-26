package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

const sample = `summary: Add ward and the cards that use it.
workstreams:
  - name: engine
  - name: cards
    dependsOn: [engine]
tasks:
  - key: keeper
    title: Implement Ward Keeper
    workstream: cards
    after: [ward-card]
  - key: ward
    title: Add the ward keyword
    workstream: engine
    profile: implementation
    body: Ward stops the next damage.
  - key: ward-card
    title: Implement Warden
    workstream: cards
`

func TestParseAndOrder(t *testing.T) {
	p, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	ordered, err := p.order()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, task := range ordered {
		keys = append(keys, task.Key)
	}
	if strings.Join(keys, " ") != "ward ward-card keeper" {
		t.Errorf("order = %v, want engine before cards and keeper after its card", keys)
	}
	deps := p.deps()
	if strings.Join(deps["keeper"], " ") != "ward-card ward" ||
		strings.Join(deps["ward"], " ") != "" {
		t.Errorf("deps = %v", deps)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct{ name, plan, want string }{
		{"empty", "summary: x\n", "at least one workstream"},
		{"unknown field", "summry: x\n", "field summry not found"},
		{
			"bad workstream name",
			"workstreams: [{name: Engine}]\ntasks: [{key: a, title: A, workstream: Engine}]\n",
			"not a valid name",
		},
		{
			"unknown workstream",
			"workstreams: [{name: engine}]\ntasks: [{key: a, title: A, workstream: web}]\n",
			`workstream "web"`,
		},
		{
			"duplicate key",
			"workstreams: [{name: e}]\ntasks: [{key: a, title: A, workstream: e}, {key: a, title: B, workstream: e}]\n",
			"used twice",
		},
		{
			"unknown after",
			"workstreams: [{name: e}]\ntasks: [{key: a, title: A, workstream: e, after: [z]}]\n",
			`after "z"`,
		},
		{
			"loop",
			"workstreams: [{name: e}]\ntasks: [{key: a, title: A, workstream: e, after: [b]}, {key: b, title: B, workstream: e, after: [a]}]\n",
			"loop",
		},
		{
			"workstream loop",
			"workstreams: [{name: e, dependsOn: [f]}, {name: f, dependsOn: [e]}]\ntasks: [{key: a, title: A, workstream: e}, {key: b, title: B, workstream: f}]\n",
			"loop",
		},
		{"no title", "workstreams: [{name: e}]\ntasks: [{key: a, workstream: e}]\n", "no title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.plan))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse error = %v, want %q", err, tt.want)
			}
		})
	}
	p, _ := Parse([]byte(sample))
	p.Tasks[0].Profile = "nope"
	if err := p.Validate(map[string]config.Profile{"implementation": {}}); err == nil ||
		!strings.Contains(err.Error(), `unknown profile "nope"`) {
		t.Errorf("Validate with profiles = %v", err)
	}
}

func newRepo(t *testing.T) *queue.Store {
	t.Helper()
	ctx := context.Background()
	r := git.Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"}, {"commit", "--allow-empty", "-m", "chore: start"},
	} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	return queue.Open(r.Dir)
}

func TestNewGoal(t *testing.T) {
	ctx := context.Background()
	s := newRepo(t)
	now := time.Unix(100, 0).UTC()
	g, err := NewGoal(
		ctx,
		s,
		"",
		"Implement the new KeyForge set: Grim Reminders!",
		"All 400 cards.",
		queue.Origin{Type: "intake"},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "implement-the-new-keyforge-set" || g.State != queue.GoalPlanning ||
		g.Base != "main" {
		t.Errorf("goal = %+v", g)
	}
	tasks, _ := s.Tasks(g.Name)
	if len(tasks) != 1 || tasks[0].Kind != queue.Grilling || tasks[0].Profile != "planning" ||
		tasks[0].Body != "All 400 cards.\n" {
		t.Errorf("tasks = %+v", tasks)
	}
	again, err := NewGoal(ctx, s, "", "Implement the new KeyForge set", "", queue.Origin{}, now)
	if err != nil || again.Name != "implement-the-new-keyforge-set-2" {
		t.Errorf("second goal = %+v, %v", again, err)
	}
	if blank, _ := NewGoal(ctx, s, "", "!!!", "", queue.Origin{}, now); blank.Name != "goal" {
		t.Errorf("goal from a title with no words = %s", blank.Name)
	}
}

func TestApprove(t *testing.T) {
	ctx := context.Background()
	s := newRepo(t)
	now := time.Unix(100, 0).UTC()
	g, err := NewGoal(ctx, s, "set", "New set", "", queue.Origin{}, now)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ADR: config.ADR{Dir: "docs/adr"},
		Profiles: map[string]config.Profile{"implementation": {}, "planning": {}}}
	if err := Approve(
		ctx,
		s,
		cfg,
		g.Name,
		now,
	); err == nil ||
		!strings.Contains(err.Error(), "no plan yet") {
		t.Fatalf("Approve without a plan = %v", err)
	}

	p, _ := Parse([]byte(sample))
	if err := Save(s.GoalDir(g.Name), p); err != nil {
		t.Fatal(err)
	}
	drafts := DraftsDir(s.GoalDir(g.Name))
	if err := os.MkdirAll(drafts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(drafts, "0012-ward-is-a-keyword.md"),
		[]byte("# 12. Ward\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := Approve(ctx, s, cfg, g.Name, now); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Goal(g.Name)
	if got.State != queue.GoalActive || len(got.Workstreams) != 2 {
		t.Errorf("goal after sign-off = %+v", got)
	}
	tasks, _ := s.Tasks(g.Name)
	byTitle := map[string]*queue.Task{}
	for _, task := range tasks {
		byTitle[task.Title] = task
	}
	ward, warden, keeper := byTitle["Add the ward keyword"], byTitle["Implement Warden"], byTitle["Implement Ward Keeper"]
	if ward == nil || warden == nil || keeper == nil {
		t.Fatalf("tasks = %v", byTitle)
	}
	if len(ward.DependsOn) != 0 || strings.Join(warden.DependsOn, " ") != ward.ID ||
		strings.Join(keeper.DependsOn, " ") != warden.ID+" "+ward.ID {
		t.Errorf(
			"deps: ward %v, warden %v, keeper %v",
			ward.DependsOn,
			warden.DependsOn,
			keeper.DependsOn,
		)
	}
	if ward.Body != "Ward stops the next damage.\n" || ward.Origin.Ref != "ward" ||
		keeper.Priority <= ward.Priority {
		t.Errorf("ward = %+v", ward)
	}

	// The ADR draft landed on the integration branch, attached to grilling.
	repo := git.Repo{Dir: s.Repo()}
	adr, err := repo.Run(ctx, "show", g.IntegrationBranch()+":docs/adr/0012-ward-is-a-keyword.md")
	if err != nil || adr != "# 12. Ward" {
		t.Errorf("ADR on integration = %q, %v", adr, err)
	}
	grill := byTitle["Grill the goal: New set"]
	if grill == nil || len(grill.Commits) != 1 {
		t.Errorf("grilling task = %+v, want the ADR commit on it", grill)
	}
	if err := Approve(ctx, s, cfg, g.Name, now); err == nil {
		t.Error("an active goal was signed off again")
	}
}

func TestSupersede(t *testing.T) {
	dir := t.TempDir()
	if err := Supersede(dir, time.Now()); err != nil {
		t.Errorf("Supersede without a plan = %v", err)
	}
	p, _ := Parse([]byte(sample))
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}
	if err := Supersede(dir, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(dir); got != nil {
		t.Error("the plan is still there after Supersede")
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.19700101T000000Z.superseded.yaml")); err != nil {
		t.Errorf("superseded plan missing: %v", err)
	}
}

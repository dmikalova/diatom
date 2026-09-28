package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/ledger"
	"github.com/dmikalova/diatom/internal/spend"
)

// cmdLedger prints what the repo's landed goals came to, taking in first any
// finished goal the ledger lacks: each goal's lines of code, its cost and
// lines per dollar, then each week's.
func cmdLedger(ctx context.Context, stdout io.Writer) error {
	s, err := here(ctx)
	if err != nil {
		return err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	path := ledger.Path(paths)
	if _, err := ledger.Backfill(ctx, path, s, spend.New()); err != nil {
		_, _ = fmt.Fprintf(stdout, "some finished goals couldn't be measured: %v\n\n", err)
	}
	all, err := ledger.Load(path)
	if err != nil {
		return err
	}
	all = slices.DeleteFunc(all, func(l ledger.Landed) bool { return l.Repo != s.Repo() })
	if len(all) == 0 {
		_, err := fmt.Fprintln(stdout, "No goal of this repo has landed yet.")
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(w, "finished\tgoal\tcode\ttests\tdocs\tcost\tlines/$\t")
	for _, l := range all {
		p := ledger.Sum([]ledger.Landed{l})
		_, _ = fmt.Fprintf(
			w,
			"%s\t%s\t%d\t%d\t%d\t$%.2f\t%.0f\t\n",
			l.Finished.Local().Format("Jan 2"),
			l.Goal,
			l.Code.Added,
			l.Tests.Added,
			l.Docs.Added,
			l.CostUSD,
			p.PerDollar(),
		)
	}
	_, _ = fmt.Fprintln(w, "\t\t\t\t\t\t\t")
	_, _ = fmt.Fprintln(w, "week of\tgoals\tlines of code\t\t\tcost\tlines/$\t")
	for _, p := range ledger.Weeks(all) {
		_, _ = fmt.Fprintf(
			w,
			"%s\t%d\t%d\t\t\t$%.2f\t%.0f\t\n",
			p.Start.Format("Jan 2"),
			p.Goals,
			p.LOC,
			p.CostUSD,
			p.PerDollar(),
		)
	}
	sum := ledger.Sum(all)
	_, _ = fmt.Fprintf(
		w,
		"all\t%d\t%d\t\t\t$%.2f\t%.0f\t\n",
		sum.Goals,
		sum.LOC,
		sum.CostUSD,
		sum.PerDollar(),
	)
	return w.Flush()
}

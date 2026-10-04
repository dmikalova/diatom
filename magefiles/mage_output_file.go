//go:build ignore
// +build ignore

package main

import (
	_context "context"
	_flag "flag"
	_fmt "fmt"
	_io "io"
	_log "log"
	_os "os"
	_signal "os/signal"
	_filepath "path/filepath"
	_sort "sort"
	_strconv "strconv"
	_strings "strings"
	_syscall "syscall"
	_tabwriter "text/tabwriter"
	_time "time"
	ci_mageimport "github.com/dmikalova/project-standards/ci"
	
)

func main() {
	// Use local types and functions in order to avoid name conflicts with additional magefiles.
	type arguments struct {
		Verbose       bool          // print out log statements
		List          bool          // print out a list of targets
		Help          bool          // print out help for a specific target
		Timeout       _time.Duration // set a timeout to running the targets
		Args          []string      // args contain the non-flag command-line arguments
	}

	parseBool := func(env string) bool {
		val := _os.Getenv(env)
		if val == "" {
			return false
		}		
		b, err := _strconv.ParseBool(val)
		if err != nil {
			_log.Printf("warning: environment variable %s is not a valid bool value: %v", env, val)
			return false
		}
		return b
	}

	parseDuration := func(env string) _time.Duration {
		val := _os.Getenv(env)
		if val == "" {
			return 0
		}		
		d, err := _time.ParseDuration(val)
		if err != nil {
			_log.Printf("warning: environment variable %s is not a valid duration value: %v", env, val)
			return 0
		}
		return d
	}
	args := arguments{}
	fs := _flag.FlagSet{}
	fs.SetOutput(_os.Stdout)

	// default flag set with ExitOnError and auto generated PrintDefaults should be sufficient
	fs.BoolVar(&args.Verbose, "v", parseBool("MAGEFILE_VERBOSE"), "show verbose output when running targets")
	fs.BoolVar(&args.List, "l", parseBool("MAGEFILE_LIST"), "list targets for this binary")
	fs.BoolVar(&args.Help, "h", parseBool("MAGEFILE_HELP"), "print out help for a specific target")
	fs.DurationVar(&args.Timeout, "t", parseDuration("MAGEFILE_TIMEOUT"), "timeout in duration parsable format (e.g. 5m30s)")
	fs.Usage = func() {
		_fmt.Fprintf(_os.Stdout, `
%s [options] [target]

Commands:
  -l    list targets in this binary
  -h    show this help

Options:
  -h    show description of a target
  -t <string>
        timeout in duration parsable format (e.g. 5m30s)
  -v    show verbose output when running targets
 `[1:], _filepath.Base(_os.Args[0]))
	}
	if err := fs.Parse(_os.Args[1:]); err != nil {
		// flag will have printed out an error already.
		return
	}
	args.Args = fs.Args()
	if args.Help && len(args.Args) == 0 {
		fs.Usage()
		return
	}
		
	var printName = func(str string) string {
	// color is ANSI color type
	type color int

	// If you add/change/remove any items in this constant,
	// you will need to run "stringer -type=color" in this directory again.
	// NOTE: Please keep the list in an alphabetical order.
	const (
		black color = iota
		red
		green
		yellow
		blue
		magenta
		cyan
		white
		brightblack
		brightred
		brightgreen
		brightyellow
		brightblue
		brightmagenta
		brightcyan
		brightwhite
	)

	// AnsiColor are ANSI color codes for supported terminal colors.
	var ansiColor = map[color]string{
		black:         "\u001b[30m",
		red:           "\u001b[31m",
		green:         "\u001b[32m",
		yellow:        "\u001b[33m",
		blue:          "\u001b[34m",
		magenta:       "\u001b[35m",
		cyan:          "\u001b[36m",
		white:         "\u001b[37m",
		brightblack:   "\u001b[30;1m",
		brightred:     "\u001b[31;1m",
		brightgreen:   "\u001b[32;1m",
		brightyellow:  "\u001b[33;1m",
		brightblue:    "\u001b[34;1m",
		brightmagenta: "\u001b[35;1m",
		brightcyan:    "\u001b[36;1m",
		brightwhite:   "\u001b[37;1m",
	}

	const colorName = "blackredgreenyellowbluemagentacyanwhitebrightblackbrightredbrightgreenbrightyellowbrightbluebrightmagentabrightcyanbrightwhite"

	var colorIndex = [...]uint8{0, 5, 8, 13, 19, 23, 30, 34, 39, 50, 59, 70, 82, 92, 105, 115, 126}

	colorToLowerString := func(i color) string {
		if i < 0 || i >= color(len(colorIndex)-1) {
			return "color(" + _strconv.FormatInt(int64(i), 10) + ")"
		}
		return colorName[colorIndex[i]:colorIndex[i+1]]
	}

	// ansiColorReset is an ANSI color code to reset the terminal color.
	const ansiColorReset = "\033[0m"

	// defaultTargetAnsiColor is a default ANSI color for colorizing targets.
	// It is set to Cyan as an arbitrary color, because it has a neutral meaning
	var defaultTargetAnsiColor = ansiColor[cyan]

	getAnsiColor := func(color string) (string, bool) {
		colorLower := _strings.ToLower(color)
		for k, v := range ansiColor {
			colorConstLower := colorToLowerString(k)
			if colorConstLower == colorLower {
				return v, true
			}
		}
		return "", false
	}

	// Terminals which  don't support color:
	//
	//	TERM=vt100
	//	TERM=cygwin
	//	TERM=xterm-mono
	var noColorTerms = map[string]bool{
		"vt100":      false,
		"cygwin":     false,
		"xterm-mono": false,
	}

	// terminalSupportsColor checks if the current console supports color output
	//
	// Supported:
	//
	//	linux, mac, or windows's ConEmu, Cmder, putty, git-bash.exe, pwsh.exe
	//
	// Not supported:
	//
	//	windows cmd.exe, powerShell.exe
	terminalSupportsColor := func() bool {
		envTerm := _os.Getenv("TERM")
		if _, ok := noColorTerms[envTerm]; ok {
			return false
		}
		return true
	}

	// enableColor reports whether the user has requested to enable a color output.
	enableColor := func() bool {
		b, _ := _strconv.ParseBool(_os.Getenv("MAGEFILE_ENABLE_COLOR"))
		return b
	}

	// targetColor returns the ANSI color which should be used to colorize targets.
	targetColor := func() string {
		s, exists := _os.LookupEnv("MAGEFILE_TARGET_COLOR")
		if exists {
			if c, ok := getAnsiColor(s); ok {
				return c
			}
		}
		return defaultTargetAnsiColor
	}

	// store the color terminal variables, so that the detection isn't repeated for each target
	var enableColorValue = enableColor() && terminalSupportsColor()
	var targetColorValue = targetColor()

	if enableColorValue {
		return _fmt.Sprintf("%s%s%s", targetColorValue, str, ansiColorReset)
	}

	return str
}


	list := func() error {
		_fmt.Println(`diatom developer tasks, all from project-standards' shared ci package. Run 'mage -l' to list them, and 'mage ci:fix && mage ci:check' before calling work done.
`)
		targets := map[string]string{
			"ci:build": "builds every package, then runs the project's ExtraBuilds.",
			"ci:check": "verifies what ci:fix would change and runs every check.",
			"ci:commits": "lints commit messages not yet on the default branch.",
			"ci:cover": "fails if a CoverGates area is below 100% statement coverage.",
			"ci:drift": "fails if a generated config differs from what ci:fix writes.",
			"ci:fix": "applies every autofix and regenerates the generated configs.",
			"ci:format": "fails if a formatter or go fix would change a Go file.",
			"ci:lint": "runs golangci-lint, fixing nothing.",
			"ci:markdown": "lints Markdown with goldmark-lint, fixing nothing.",
			"ci:secrets": "scans history, staged and unstaged changes for secrets.",
			"ci:spell": "checks spelling in Go comments and every other text file.",
			"ci:test": "runs every package's tests and prints an aligned report.",
			"ci:tidy": "fails if go mod tidy would change go.mod or go.sum.",
			"ci:vet": "runs go vet over every package.",
			"ci:vuln": "fails if govulncheck finds a vulnerability the code reaches.",
		}

		keys := make([]string, 0, len(targets))
		for name := range targets {
			keys = append(keys, name)
		}
		_sort.Strings(keys)

		_fmt.Println("Targets:")
		w := _tabwriter.NewWriter(_os.Stdout, 0, 4, 4, ' ', 0)
		for _, name := range keys {
			_fmt.Fprintf(w, "  %v\t%v\n", printName(name), targets[name])
		}
		err := w.Flush()
		return err
	}

	var ctx _context.Context
	ctxCancel := func(){}

	// by deferring in a closure, we let the cancel function get replaced
	// by the getContext function.
	defer func() {
		ctxCancel()
	}()

	getContext := func() (_context.Context, func()) {
		if ctx == nil {
			if args.Timeout != 0 {
				ctx, ctxCancel = _context.WithTimeout(_context.Background(), args.Timeout)
			} else {
				ctx, ctxCancel = _context.WithCancel(_context.Background())
			}
		}

		return ctx, ctxCancel
	}

	runTarget := func(logger *_log.Logger, fn func(_context.Context) error) interface{} {
		var err interface{}
		ctx, cancel := getContext()
		d := make(chan interface{})
		go func() {
			defer func() {
				err := recover()
				d <- err
			}()
			err := fn(ctx)
			d <- err
		}()
		sigCh := make(chan _os.Signal, 1)
		_signal.Notify(sigCh, _syscall.SIGINT)
		select {
		case <-sigCh:
			logger.Println("cancelling mage targets, waiting up to 5 seconds for cleanup...")
			cancel()
			cleanupCh := _time.After(5 * _time.Second)

			select {
			// target exited by itself
			case err = <-d:
				return err
			// cleanup timeout exceeded
			case <-cleanupCh:
				return _fmt.Errorf("cleanup timeout exceeded")
			// second SIGINT received
			case <-sigCh:
				logger.Println("exiting mage")
				return _fmt.Errorf("exit forced")
			}
		case <-ctx.Done():
			cancel()
			e := ctx.Err()
			_fmt.Printf("ctx err: %v\n", e)
			return e
		case err = <-d:
			// we intentionally don't cancel the context here, because
			// the next target will need to run with the same _context.
			return err
		}
	}
	// This is necessary in case there aren't any targets, to avoid an unused
	// variable error.
	_ = runTarget

	handleError := func(logger *_log.Logger, err interface{}) {
		if err != nil {
			logger.Printf("Error: %+v\n", err)
			type code interface {
				ExitStatus() int
			}
			if c, ok := err.(code); ok {
				_os.Exit(c.ExitStatus())
			}
			_os.Exit(1)
		}
	}
	_ = handleError

	// Set MAGEFILE_VERBOSE so mg.Verbose() reflects the flag value.
	if args.Verbose {
		_os.Setenv("MAGEFILE_VERBOSE", "1")
	} else {
		_os.Setenv("MAGEFILE_VERBOSE", "0")
	}

	_log.SetFlags(0)
	if !args.Verbose {
		_log.SetOutput(_io.Discard)
	}
	logger := _log.New(_os.Stderr, "", 0)
	if args.List {
		if err := list(); err != nil {
			_log.Println(err)
			_os.Exit(1)
		}
		return
	}

	if args.Help {
		if len(args.Args) < 1 {
			logger.Println("no target specified")
			_os.Exit(2)
		}
		switch _strings.ToLower(args.Args[0]) {
			case "ci:build":
				_fmt.Println("Build builds every package, then runs the project's ExtraBuilds.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:build\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:check":
				_fmt.Println("Check verifies what ci:fix would change and runs every check. It never writes a file. The independent checks run in parallel; tests and coverage then run after them, in order, so their reports read as two clean blocks rather than interleaving.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:check\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:commits":
				_fmt.Println("Commits lints commit messages not yet on the default branch. By default the range is from the merge-base with the default branch to HEAD. With no such commits, or no default branch to compare with (a shallow clone, a repository without a remote), it passes. Merge commits are skipped.  CI sets CI_COMMIT_RANGE to \"<from>..<to>\" to lint exactly that range, such as the commits a push to the default branch added, which the default range never sees. A from of all zeros, which a forge sends for the first push of a new branch, lints from the merge-base of to with the default branch. When the variable is set, a range that cannot be resolved is an error, not a skip: the clone must hold both ends, so CI needs full history.  Only the branch's own commits are linted, never the default branch's history: those were linted when they were made, and a project adopting the standard should not fail on commits it can no longer reword.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:commits\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:cover":
				_fmt.Println("Cover fails if a CoverGates area is below 100% statement coverage. It lists each short area's functions that are not fully covered. With no gates set it passes.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:cover\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:drift":
				_fmt.Println("Drift fails if a generated config differs from what ci:fix writes. Generated configs are the base configs merged with mklv.config.json; a hand edit to one is drift, and the fix is to move the change into mklv.config.json.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:drift\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:fix":
				_fmt.Println("Fix applies every autofix and regenerates the generated configs. It writes the generated configs first, so the linters run with them, then runs go mod tidy, go fix, golangci-lint --fix, goimports (to repair the imports those rewrites need), the formatters, goldmark-lint --fix and misspell -w. Issues a fixer cannot fix are left for ci:check to report; Fix fails only when a tool itself fails.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:fix\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:format":
				_fmt.Println("Format fails if a formatter or go fix would change a Go file. The formatters are golines (gofmt plus long-line shortening) and gci. It writes nothing: each tool lists what it would change, and ci:fix applies the changes.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:format\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:lint":
				_fmt.Println("Lint runs golangci-lint, fixing nothing. It reads the generated .golangci.yaml.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:lint\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:markdown":
				_fmt.Println("Markdown lints Markdown with goldmark-lint, fixing nothing.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:markdown\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:secrets":
				_fmt.Println("Secrets scans history, staged and unstaged changes for secrets. It runs gitleaks with the generated .gitleaks.toml, redacting what it finds. Covering all three means a secret is caught before it is committed (the pre-commit hook runs ci:check) and cannot hide in an older commit. Every scan runs even after one finds a leak, so one run reports them all.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:secrets\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:spell":
				_fmt.Println("Spell checks spelling in Go comments and every other text file. It uses misspell with the tools.misspell settings from mklv.config.json and fixes nothing.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:spell\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:test":
				_fmt.Println("Test runs every package's tests and prints an aligned report.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:test\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:tidy":
				_fmt.Println("Tidy fails if go mod tidy would change go.mod or go.sum.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:tidy\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:vet":
				_fmt.Println("Vet runs go vet over every package.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:vet\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				case "ci:vuln":
				_fmt.Println("Vuln fails if govulncheck finds a vulnerability the code reaches. It checks the project's dependencies and the standard library it builds with, so a new advisory fails the next commit and gets fixed the same day. The weekly conformance run bumps affected modules too, as a backstop for projects nobody commits to (ADR 0007). It needs network access to fetch the vulnerability database.")
				_fmt.Println()
				
				_fmt.Print("Usage:\n\n\tmage ci:vuln\n\n")
				var aliases []string
				if len(aliases) > 0 {
					_sort.Strings(aliases)
					_fmt.Printf("Aliases: %s\n\n", _strings.Join(aliases, ", "))
				}
				return
				default:
				logger.Printf("Unknown target: %q\n", args.Args[0])
				_os.Exit(2)
		}
	}
	if len(args.Args) < 1 {
		if err := list(); err != nil {
			logger.Println("Error:", err)
			_os.Exit(1)
		}
		return
	}
	for x := 0; x < len(args.Args); {
		target := args.Args[x]
		x++

		// resolve aliases
		switch _strings.ToLower(target) {
		
		}

		switch _strings.ToLower(target) {
		
		
		
			
				case "ci:build":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Build\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Build")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Build(ctx)
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:check":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Check\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Check")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Check(ctx)
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:commits":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Commits\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Commits")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Commits()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:cover":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Cover\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Cover")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Cover()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:drift":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Drift\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Drift")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Drift()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:fix":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Fix\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Fix")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Fix()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:format":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Format\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Format")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Format()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:lint":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Lint\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Lint")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Lint()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:markdown":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Markdown\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Markdown")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Markdown()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:secrets":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Secrets\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Secrets")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Secrets()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:spell":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Spell\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Spell")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Spell()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:test":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Test\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Test")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Test()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:tidy":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Tidy\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Tidy")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Tidy()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:vet":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Vet\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Vet")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Vet()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
				case "ci:vuln":
					expected := x + 0
					if expected > len(args.Args) {
						// note that expected and args at this point include the arg for the target itself
						// so we subtract 1 here to show the number of args without the target.
						logger.Printf("not enough arguments for target \"ci:Vuln\", expected %v, got %v\n", expected-1, len(args.Args)-1)
						_os.Exit(2)
					}
					if args.Verbose {
						logger.Println("Running target:", "ci:Vuln")
					}
					
				wrapFn := func(ctx _context.Context) error {
					return ci_mageimport.Vuln()
				}
				ret := runTarget(logger, wrapFn)
					handleError(logger, ret)
		default:
			logger.Printf("Unknown target specified: %q\n", target)
			_os.Exit(2)
		}
	}
}





// Command assistant is the assistant application.
//
//	assistant serve                       start the HTTP server (the default)
//	assistant migrate                     apply the database schema
//	assistant workflow validate <file>... check workflow files
//	assistant models validate <file>...   check models files
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"assistant/internal/workflow"
)

const usage = `usage: assistant workflow validate [--tools name,name] <file>...

Checks workflow files against the workflow schema and lints them. Without
--tools the tool names used by states are not checked.
exit codes: 0 all files valid, 1 problems found, 2 bad usage`

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

// dispatch picks the command. "workflow" needs no configuration and so no
// database; the others read the environment.
func dispatch(args []string) int {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "serve":
		return serve()
	case "migrate":
		return migrate()
	case "workflow":
		return run(args, os.Stdout, os.Stderr)
	case "models":
		return runModels(args, os.Stdout, os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (expected serve, migrate, workflow or models)\n", command)
		return 2
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "workflow" || args[1] != "validate" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return validate(args[2:], stdout, stderr)
}

func validate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workflow validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	toolList := fs.String("tools", "", "comma-separated tool names that exist; omit to skip the tool check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	var tools workflow.ToolSet
	if *toolList != "" {
		names := workflow.NameSet{}
		for _, n := range strings.Split(*toolList, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names[n] = true
			}
		}
		tools = names
	}

	failed := false
	for _, file := range files {
		if !validateFile(file, tools, stdout, stderr) {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func validateFile(file string, tools workflow.ToolSet, stdout, stderr io.Writer) bool {
	wf, err := workflow.Load(file)
	if err != nil {
		var lerr *workflow.LoadError
		if errors.As(err, &lerr) {
			fmt.Fprintln(stderr, lerr.Error())
		} else {
			fmt.Fprintf(stderr, "%s: %v\n", file, err)
		}
		return false
	}
	findings := workflow.Lint(wf, tools)
	for _, f := range findings {
		fmt.Fprintf(stderr, "%s:%d: %s: %s\n", file, f.Line, f.Path, f.Message)
	}
	if len(findings) > 0 {
		return false
	}
	fmt.Fprintf(stdout, "%s: ok: workflow %q version %d, %d states\n", file, wf.ID, wf.Version, len(wf.States))
	return true
}

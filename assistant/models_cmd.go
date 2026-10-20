package main

import (
	"errors"
	"fmt"
	"io"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/yamlschema"
)

const modelsUsage = `usage: assistant models validate <file>...

Checks models files against the models schema and for inconsistencies: ids used
twice, a model on a provider that is not listed, a base_url that is not a plain
http(s) address or that carries credentials.
exit codes: 0 all files valid, 1 problems found, 2 bad usage`

// runModels is the "models" command: args are the command line from "models".
func runModels(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 || args[1] != "validate" {
		fmt.Fprintln(stderr, modelsUsage)
		return 2
	}
	failed := false
	for _, file := range args[2:] {
		cfg, err := modelcfg.Load(file)
		if err != nil {
			var found *yamlschema.Error
			if errors.As(err, &found) {
				fmt.Fprintln(stderr, found.Error())
			} else {
				fmt.Fprintf(stderr, "%s: %v\n", file, err)
			}
			failed = true
			continue
		}
		fmt.Fprintf(stdout, "%s: ok: %d providers, %d models\n", file, len(cfg.Providers), len(cfg.Models))
	}
	if failed {
		return 1
	}
	return 0
}

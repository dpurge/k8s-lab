package workflow

import (
	"fmt"
	"os"
	"sync"

	"assistant/internal/yamlschema"
	"assistant/schemas"
)

// Finding is one problem in a workflow file, located by key path and line.
type Finding = yamlschema.Finding

// LoadError reports every problem found in one workflow file.
type LoadError = yamlschema.Error

var (
	compileOnce    sync.Once
	compiledSchema *yamlschema.Schema
	compileErr     error
)

func workflowSchema() (*yamlschema.Schema, error) {
	compileOnce.Do(func() {
		compiledSchema, compileErr = yamlschema.Compile(schemas.Workflow, "workflow.schema.json")
	})
	return compiledSchema, compileErr
}

// Load reads and validates the workflow file at path.
func Load(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow %s: %w", path, err)
	}
	return LoadBytes(path, data)
}

// LoadBytes validates data against the workflow schema and decodes it. name
// is only used in error messages. A file that breaks the schema yields a
// *LoadError listing every problem with its key path and line.
func LoadBytes(name string, data []byte) (*Workflow, error) {
	schema, err := workflowSchema()
	if err != nil {
		return nil, err
	}
	root, err := schema.Check(name, data)
	if err != nil {
		return nil, err
	}
	var wf Workflow
	if err := root.Decode(&wf); err != nil {
		return nil, fmt.Errorf("decode workflow %s: %w", name, err)
	}
	wf.root = root
	return &wf, nil
}

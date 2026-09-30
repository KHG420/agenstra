package main

import (
	"encoding/json"
	"flag"
	"fmt"
	agenstra "github.com/KHG420/agenstra"
	"os"
	"strings"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }
func main() {
	spec := flag.String("spec", "", "OpenAPI JSON file")
	out := flag.String("out", "", "REST pack JSON output")
	name := flag.String("name", "", "pack name")
	base := flag.String("base-url-env", "", "base URL variable")
	token := flag.String("token-env", "", "bearer token variable")
	var operations, effects list
	flag.Var(&operations, "operation", "operation ID (repeatable)")
	flag.Var(&effects, "effect", "OPERATION_ID=EFFECT (repeatable)")
	flag.Parse()
	if *spec == "" || *out == "" || *name == "" || *base == "" || len(operations) == 0 {
		fail(fmt.Errorf("--spec, --out, --name, --base-url-env and --operation are required"))
	}
	overrides := map[string]string{}
	for _, entry := range effects {
		id, effect, ok := strings.Cut(entry, "=")
		if !ok || id == "" || overrides[id] != "" {
			fail(fmt.Errorf("--effect requires unique OPERATION_ID=EFFECT"))
		}
		overrides[id] = effect
	}
	draft, e := agenstra.ImportOpenAPI(*spec, *name, *base, operations, overrides, *token)
	if e != nil {
		fail(e)
	}
	b, e := json.MarshalIndent(draft, "", "  ")
	if e != nil {
		fail(e)
	}
	if e = os.WriteFile(*out, append(b, '\n'), 0644); e != nil {
		fail(e)
	}
}
func fail(e error) { fmt.Fprintln(os.Stderr, "agenstra-import-openapi:", e); os.Exit(2) }

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	agenstra "github.com/KHG420/agenstra"
)

type grants []string

func (g *grants) String() string     { return fmt.Sprint([]string(*g)) }
func (g *grants) Set(v string) error { *g = append(*g, v); return nil }
func main() {
	code, e := run()
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
	}
	os.Exit(code)
}
func run() (int, error) {
	pack := flag.String("pack", "", "pack.json path")
	instruction := flag.String("instruction", "", "task instruction")
	inspect := flag.Bool("inspect", false, "inspect catalog")
	maxTokens := flag.Int64("max-model-tokens", 0, "run model token budget (0 disables)")
	maxOutput := flag.Int("max-output-tokens", 0, "maximum model output tokens per request (0 omits)")
	var g grants
	flag.Var(&g, "grant-capability", "grant exact capability name (repeatable)")
	flag.Parse()
	if *maxTokens < 0 || *maxOutput < 0 {
		return 2, errors.New("token limits must be nonnegative")
	}
	if *pack == "" {
		return 2, errors.New("--pack is required")
	}
	provider, e := agenstra.OpenPack(context.Background(), *pack, nil)
	if e != nil {
		return 2, e
	}
	defer provider.Close()
	if *inspect {
		caps := []any{}
		for _, v := range provider.Capabilities() {
			caps = append(caps, v.ModelView())
		}
		skills := []any{}
		for _, v := range provider.Skills() {
			skills = append(skills, v.Description)
		}
		return 0, printJSON(map[string]any{"capabilities": caps, "skills": skills})
	}
	if *instruction == "" {
		return 2, errors.New("--instruction is required unless --inspect is used")
	}
	model, e := agenstra.NewHTTPJSONDecisionModel(os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("AGENT_MODEL_API_KEY"), 30*time.Second, nil)
	if e != nil {
		return 2, e
	}
	defer model.Close()
	model.MaxOutputTokens = *maxOutput
	grantMap := map[string]bool{}
	for _, v := range g {
		grantMap[v] = true
	}
	runtime := &agenstra.AgentRuntime{Provider: provider, Model: model, Grants: grantMap}
	runtime.MaxModelTokens = *maxTokens
	result, e := runtime.Run(context.Background(), *instruction)
	if e != nil {
		return 2, e
	}
	if e = printJSON(result); e != nil {
		return 2, e
	}
	if result.Status != "completed" {
		return 2, nil
	}
	return 0, nil
}
func printJSON(v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	fmt.Println(string(b))
	return nil
}

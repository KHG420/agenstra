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
	contextCharacters := flag.Int("max-context-characters", 80000, "maximum Unicode characters in model input")
	contextWindow := flag.Int64("context-window-tokens", 0, "known model context window (0 unknown)")
	maxInput := flag.Int64("max-input-tokens", 0, "model input token ceiling (0 unspecified)")
	outputReserve := flag.Int("output-reserve-tokens", 0, "tokens reserved for the response")
	protocolReserve := flag.Int64("protocol-reserve-tokens", 0, "context allowance for protocol overhead")
	triggerRatio := flag.Float64("context-trigger-ratio", 0, "soft projection trigger ratio (0 disables)")
	targetRatio := flag.Float64("context-target-ratio", 0, "soft projection target ratio")
	concurrentTools := flag.Int("max-concurrent-tools", 4, "maximum independent tool calls in flight (1-4)")
	var g grants
	flag.Var(&g, "grant-capability", "grant exact capability name (repeatable)")
	flag.Parse()
	if *maxTokens < 0 || *maxOutput < 0 {
		return 2, errors.New("token limits must be nonnegative")
	}
	if *concurrentTools < 1 || *concurrentTools > 4 {
		return 2, errors.New("max-concurrent-tools must be between 1 and 4")
	}
	settings := agenstra.DefaultHostSettings()
	settings.MaxContextCharacters = *contextCharacters
	settings.ModelContextWindowTokens = *contextWindow
	settings.MaxModelInputTokens = *maxInput
	settings.ModelOutputReserveTokens = *outputReserve
	settings.ModelProtocolReserveTokens = *protocolReserve
	settings.ContextPolicy = agenstra.ContextPolicy{TriggerRatio: *triggerRatio, TargetRatio: *targetRatio}
	if err := settings.Validate(); err != nil {
		return 2, err
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
	runtime.MaxContextCharacters = *contextCharacters
	runtime.ModelContextWindowTokens = *contextWindow
	runtime.MaxModelInputTokens = *maxInput
	runtime.ModelOutputReserveTokens = *outputReserve
	runtime.ModelProtocolReserveTokens = *protocolReserve
	runtime.ContextPolicy = settings.ContextPolicy
	runtime.MaxModelOutputTokens = *maxOutput
	runtime.MaxModelTokens = *maxTokens
	runtime.MaxConcurrentTools = *concurrentTools
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

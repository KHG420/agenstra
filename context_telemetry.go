package agenstra

import (
	"time"
	"unicode/utf8"
)

// ContextTelemetry describes the final input projection, outside model context.
// Tokens are added only by a model measurement; character counts are exact.
type ContextTelemetry struct {
	Schema              string                `json:"schema"`
	ProjectionID        string                `json:"projection_id"`
	Round               int                   `json:"round"`
	MeasuredAt          float64               `json:"measured_at"`
	InputCharacters     int                   `json:"input_characters"`
	CharacterLimit      int                   `json:"character_limit"`
	CharactersRemaining int                   `json:"characters_remaining"`
	Utilization         float64               `json:"character_utilization"`
	OverLimit           bool                  `json:"over_limit"`
	CandidateCharacters int                   `json:"candidate_characters"`
	Components          map[string]int        `json:"components"`
	Omissions           ContextOmissionCounts `json:"omissions"`
}

type ContextOmissionCounts struct {
	Observations    int `json:"observations"`
	Arguments       int `json:"arguments"`
	FactPaths       int `json:"fact_paths"`
	Skills          int `json:"skills"`
	Memories        int `json:"memories"`
	DeferredSchemas int `json:"deferred_schemas"`
}

func measureContext(state *RuntimeState, prompt string, candidate, packet ContextPacket, limit int) *ContextTelemetry {
	raw, _ := CanonicalJSON(packet)
	before, _ := CanonicalJSON(candidate)
	promptSize := utf8.RuneCountInString(prompt)
	size := promptSize + utf8.RuneCount(raw)
	c := &ContextTelemetry{Schema: "agenstra.context-telemetry.v1", ProjectionID: NewID(), Round: state.RoundsUsed + 1, MeasuredAt: float64(time.Now().UnixNano()) / 1e9, InputCharacters: size, CharacterLimit: limit, CharactersRemaining: max(0, limit-size), OverLimit: size > limit, CandidateCharacters: promptSize + utf8.RuneCount(before), Components: map[string]int{"system_prompt": promptSize}}
	if limit > 0 {
		c.Utilization = float64(size) / float64(limit)
	}
	obj, _ := objectOf(packet)
	valuesSize := 0
	for key, value := range obj {
		n := contextCharacters(value)
		c.Components[key] = n
		valuesSize += n
	}
	c.Components["json_framing"] = utf8.RuneCount(raw) - valuesSize
	c.Omissions.Observations = max(0, len(state.ModelObservations)-len(packet.Observations))
	for _, o := range packet.Observations {
		if o.ArgumentsOmitted {
			c.Omissions.Arguments++
		}
	}
	for _, f := range packet.Facts {
		c.Omissions.FactPaths += len(f.OmittedPaths)
	}
	c.Omissions.Skills = max(0, len(candidate.LoadedSkills)-len(packet.LoadedSkills))
	c.Omissions.Memories = max(0, len(candidate.Memories)-len(packet.Memories))
	for _, capability := range packet.Capabilities {
		if capability["schema_requires_inspection"] == true {
			c.Omissions.DeferredSchemas++
		}
	}
	return c
}

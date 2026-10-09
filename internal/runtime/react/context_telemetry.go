package react

import (
	"time"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func measureContext(state *agentcontract.RuntimeState, prompt string, candidate, packet agentcontract.ContextPacket, limit int) *agentcontract.ContextTelemetry {
	raw, _ := agentcontract.CanonicalJSON(packet)       //nolint:errcheck // Step validates the packet before projection; projection retains JSON values.
	before, _ := agentcontract.CanonicalJSON(candidate) //nolint:errcheck // Step validates this candidate before projection.
	promptSize := utf8.RuneCountInString(prompt)
	size := promptSize + utf8.RuneCount(raw)
	c := &agentcontract.ContextTelemetry{Schema: "agenstra.context-telemetry.v1", ProjectionID: agentcontract.NewID(), Round: state.RoundsUsed + 1, MeasuredAt: float64(time.Now().UnixNano()) / 1e9, InputCharacters: size, CharacterLimit: limit, CharactersRemaining: max(0, limit-size), OverLimit: size > limit, CandidateCharacters: promptSize + utf8.RuneCount(before), Components: map[string]int{"system_prompt": promptSize}}
	if limit > 0 {
		c.Utilization = float64(size) / float64(limit)
	}
	obj, _ := agentcontract.ObjectOf(packet) //nolint:errcheck // The validated ContextPacket encodes to an object.
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

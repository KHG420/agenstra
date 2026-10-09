package host

import (
	"context"
	"sort"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// ListMemories returns a bounded owner- and pack-scoped page after access checks.
func (h *AgentHost) ListMemories(ctx context.Context, owner, pack string, limit, offset int) ([]agentcontract.Memory, error) {
	if _, err := h.Policy(ctx, owner, pack, false); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 || offset < 0 {
		return nil, agentcontract.NewHostError("invalid_page")
	}
	return h.Store.ListMemories(owner, pack, limit, offset)
}

// GetMemory reads an entry only within the owner's allowed pack scope.
func (h *AgentHost) GetMemory(ctx context.Context, owner, pack, id string) (agentcontract.Memory, error) {
	if _, err := h.Policy(ctx, owner, pack, false); err != nil {
		return agentcontract.Memory{}, err
	}
	return h.Store.GetMemory(owner, pack, id)
}

// SetMemory validates and saves a host-supplied preference under current access and revision.
func (h *AgentHost) SetMemory(ctx context.Context, owner, pack string, update agentcontract.MemoryUpdate) (agentcontract.Memory, error) {
	if _, err := h.Policy(ctx, owner, pack, false); err != nil {
		return agentcontract.Memory{}, err
	}
	if err := agentcontract.ValidateMemory(update.Scope, update.Key, update.Value, update.Kind); err != nil {
		return agentcontract.Memory{}, err
	}
	if update.Revision < 0 {
		return agentcontract.Memory{}, agentcontract.NewHostError("memory_invalid")
	}
	return h.Store.SetMemory(owner, pack, update)
}

// DeleteMemory forgets an entry under current access and expected revision.
func (h *AgentHost) DeleteMemory(ctx context.Context, owner, pack, id string, revision int) (agentcontract.Memory, error) {
	if _, err := h.GetMemory(ctx, owner, pack, id); err != nil {
		return agentcontract.Memory{}, err
	}
	if revision < 1 {
		return agentcontract.Memory{}, agentcontract.NewHostError("memory_invalid")
	}
	return h.Store.ForgetMemory(owner, pack, id, revision)
}

// MemoryHistory reads revisions and evidence after verifying current scope and ownership.
func (h *AgentHost) MemoryHistory(ctx context.Context, owner, pack, id string) (agentcontract.MemoryHistory, error) {
	if _, err := h.GetMemory(ctx, owner, pack, id); err != nil {
		return agentcontract.MemoryHistory{}, err
	}
	return h.Store.MemoryHistory(owner, pack, id)
}

func memoryViews(items []agentcontract.Memory) []agentcontract.MemoryView {
	byKey := map[string]agentcontract.Memory{}
	for _, m := range items {
		if m.Status != "active" {
			continue
		}
		previous, ok := byKey[m.Key]
		if !ok || m.Scope == "pack" && previous.Scope == "user" {
			byKey[m.Key] = m
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	views := []agentcontract.MemoryView{}
	for _, key := range keys {
		m := byKey[key]
		views = append(views, agentcontract.MemoryView{PackID: m.PackID, ID: m.ID, Key: m.Key, Value: m.Value, Kind: m.Kind, Scope: m.Scope, Revision: m.Revision})
	}
	return views
}

func setMemoryInput(envelope map[string]any, id, text string) {
	envelope["memory_inputs"] = []runstore.MemoryInput{{ID: id, Text: text}}
}

func (h *AgentHost) prepareMemories(ctx context.Context, run agentcontract.StoredRun) (agentcontract.StoredRun, error) {
	raw, err := agentcontract.CanonicalJSON(run.State["memory_inputs"])
	if err != nil {
		return run, agentcontract.NewHostError("run_state_invalid")
	}
	inputs := []runstore.MemoryInput{}
	if run.State["memory_inputs"] != nil {
		if err := jsonvalue.DecodeStrict(raw, &inputs); err != nil {
			return run, agentcontract.NewHostError("run_state_invalid")
		}
	}
	_, enabled := h.Model.(agentcontract.MemoryExtractor)
	for _, input := range inputs {
		done, err := h.Store.MemoryInputDone(run.OwnerID, input)
		if err != nil {
			return run, err
		}
		if done || !enabled {
			continue
		}
		existing, err := h.Store.ListMemories(run.OwnerID, run.PackID, -1, 0)
		if err != nil {
			return run, err
		}
		known := []agentcontract.MemoryView{}
		for _, m := range existing {

			// Forgotten defaults are history, not examples for relearning. Filter
			// before limiting so they cannot crowd out active or habit candidates.
			if m.Status == "forgotten" {
				continue
			}
			known = append(known, agentcontract.MemoryView{PackID: m.PackID, ID: m.ID, Key: m.Key, Value: m.Value, Kind: m.Kind, Scope: m.Scope, Revision: m.Revision})
			if len(known) == 32 {
				break
			}
		}
		request := agentcontract.MemoryExtractionRequest{Text: input.Text, Existing: known, MaxCharacters: h.runSettings(run).MaxContextCharacters}
		_, extractErr := agentcontract.MemoryExtractionInput(&request)
		var proposals []agentcontract.MemoryProposal
		if extractErr == nil {
			run, proposals, extractErr = h.extractRunMemories(ctx, run, request, input.ID)
		}
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		if extractErr == nil {
			extractErr = agentcontract.ValidateProposals(input.Text, proposals)
		}
		code := ""
		if extractErr != nil {
			proposals = nil
			code = "memory_extraction_failed"
			if agentcontract.ErrorCode(extractErr) == "memory_extraction_too_large" {
				code = "memory_extraction_too_large"
			}
		}
		if err = h.Store.ApplyMemoryInput(run, input, proposals, code); err != nil {
			return run, err
		}
	}
	errors := []agentcontract.JSON{}
	for _, input := range inputs {
		code, err := h.Store.MemoryInputError(run.OwnerID, input.ID)
		if err != nil {
			return run, err
		}
		if code != "" {
			errors = append(errors, agentcontract.JSON{"source_id": input.ID, "code": code})
		}
	}
	run.State["memory_errors"] = errors
	if _, exists := run.State["memory_snapshot"]; !exists {
		items, err := h.Store.ListMemories(run.OwnerID, run.PackID, -1, 0)
		if err != nil {
			return run, err
		}
		views := memoryViews(items)
		bindings, err := agentcontract.RunBindings(run)
		if err != nil {
			return run, err
		}
		for _, binding := range bindings {
			source, err := h.Store.ListMemories(run.OwnerID, binding.PackID, -1, 0)
			if err != nil {
				return run, err
			}
			private := []agentcontract.Memory{}
			for _, m := range source {
				if m.Scope == "pack" {
					private = append(private, m)
				}
			}
			views = append(views, memoryViews(private)...)
		}
		run.State["memory_snapshot"] = views
		state, err := h.Restore(run)
		if err != nil {
			return run, err
		}
		return h.save(run, state, "", nil, agentcontract.JSON{"kind": "memories_loaded"})
	}
	return run, nil
}

func (h *AgentHost) runMemories(run agentcontract.StoredRun) ([]agentcontract.MemoryView, error) {
	raw, err := agentcontract.CanonicalJSON(run.State["memory_snapshot"])
	if err != nil {
		return nil, err
	}
	views := []agentcontract.MemoryView{}
	if run.State["memory_snapshot"] != nil {
		if err = jsonvalue.DecodeStrict(raw, &views); err != nil {
			return nil, agentcontract.NewHostError("run_state_invalid")
		}
	}
	bindings, err := agentcontract.RunBindings(run)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{run.PackID: true}
	for _, b := range bindings {
		allowed[b.PackID] = true
	}
	out := []agentcontract.MemoryView{}
	for _, v := range views {
		pack := v.PackID
		if pack == "" {
			pack = run.PackID
		}
		if !allowed[pack] || pack != run.PackID && v.Scope != "pack" {
			continue
		}
		visible, err := h.Store.VisibleMemorySnapshot(run.OwnerID, pack, []agentcontract.MemoryView{v})
		if err != nil {
			return nil, err
		}
		out = append(out, visible...)
	}
	return out, nil
}

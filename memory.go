package agenstra

import (
	"context"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Memory is a user-owned default or project convention. Pack scope is the first
// version's project boundary; scope and ownership are assigned by the Host.
type Memory struct {
	ID            string  `json:"id"`
	Scope         string  `json:"scope"`
	PackID        string  `json:"pack_id"`
	Key           string  `json:"key"`
	Value         string  `json:"value"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	Origin        string  `json:"origin"`
	Revision      int     `json:"revision"`
	EvidenceCount int     `json:"evidence_count"`
	SourceID      string  `json:"source_id"`
	Quote         string  `json:"quote"`
	CreatedAt     float64 `json:"created_at"`
	UpdatedAt     float64 `json:"updated_at"`
}
type MemoryView struct {
	PackID   string `json:"pack_id,omitempty"`
	ID       string `json:"id"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	Revision int    `json:"revision"`
}
type MemoryUpdate struct {
	Scope    string `json:"scope"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Kind     string `json:"kind"`
	Revision int    `json:"revision"`
}
type MemoryEvidence struct {
	SourceID  string  `json:"source_id"`
	Quote     string  `json:"quote"`
	Value     string  `json:"value"`
	Mode      string  `json:"mode"`
	CreatedAt float64 `json:"created_at"`
}
type MemoryHistory struct {
	Revisions []Memory         `json:"revisions"`
	Evidence  []MemoryEvidence `json:"evidence"`
}
type MemoryProposal struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
	Value string `json:"value"`
	Kind  string `json:"kind"`
	Mode  string `json:"mode"`
	Quote string `json:"quote"`
}
type MemoryExtractionRequest struct {
	Text     string       `json:"text"`
	Existing []MemoryView `json:"existing"`
	// MaxCharacters includes the extraction system prompt and canonical request.
	// Hosts populate it; zero uses the default Host budget for direct model calls.
	MaxCharacters int `json:"-"`
}

// MemoryExtractor is optional for custom models. HTTPJSONDecisionModel implements
// it; models without it can still use and manage manually saved memories.
type MemoryExtractor interface {
	ExtractMemories(context.Context, MemoryExtractionRequest) ([]MemoryProposal, error)
}
type memoryInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

const memoryHabitEvidence = 3

var memoryKey = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func validateMemory(scope, key, value, kind string) error {
	if (scope != "user" && scope != "pack") || !memoryKey.MatchString(key) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 500 || !slices.Contains([]string{"preference", "constraint", "convention"}, kind) {
		return hostError("memory_invalid")
	}
	return nil
}
func validateProposals(text string, proposals []MemoryProposal) error {
	if len(proposals) > 8 {
		return hostError("memory_extraction_invalid")
	}
	seen := map[string]bool{}
	for _, p := range proposals {
		k := p.Scope + ":" + p.Key
		if seen[k] || !slices.Contains([]string{"habit", "explicit", "temporary", "forget"}, p.Mode) || strings.TrimSpace(p.Quote) == "" || !strings.Contains(text, p.Quote) || utf8.RuneCountInString(p.Quote) > 500 {
			return hostError("memory_extraction_invalid")
		}
		seen[k] = true
		if p.Mode == "forget" {
			if (p.Scope != "user" && p.Scope != "pack") || !memoryKey.MatchString(p.Key) {
				return hostError("memory_extraction_invalid")
			}
		} else if validateMemory(p.Scope, p.Key, p.Value, p.Kind) != nil || p.Mode == "habit" && p.Kind != "preference" {
			return hostError("memory_extraction_invalid")
		}
	}
	return nil
}
func (h *AgentHost) ListMemories(ctx context.Context, owner, pack string, limit, offset int) ([]Memory, error) {
	if _, err := h.policy(ctx, owner, pack, false); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 || offset < 0 {
		return nil, hostError("invalid_page")
	}
	return h.Store.listMemories(owner, pack, limit, offset)
}
func (h *AgentHost) GetMemory(ctx context.Context, owner, pack, id string) (Memory, error) {
	if _, err := h.policy(ctx, owner, pack, false); err != nil {
		return Memory{}, err
	}
	return h.Store.getMemory(owner, pack, id)
}
func (h *AgentHost) SetMemory(ctx context.Context, owner, pack string, update MemoryUpdate) (Memory, error) {
	if _, err := h.policy(ctx, owner, pack, false); err != nil {
		return Memory{}, err
	}
	if err := validateMemory(update.Scope, update.Key, update.Value, update.Kind); err != nil {
		return Memory{}, err
	}
	if update.Revision < 0 {
		return Memory{}, hostError("memory_invalid")
	}
	return h.Store.setMemory(owner, pack, update)
}
func (h *AgentHost) DeleteMemory(ctx context.Context, owner, pack, id string, revision int) (Memory, error) {
	if _, err := h.GetMemory(ctx, owner, pack, id); err != nil {
		return Memory{}, err
	}
	if revision < 1 {
		return Memory{}, hostError("memory_invalid")
	}
	return h.Store.forgetMemory(owner, pack, id, revision)
}
func (h *AgentHost) MemoryHistory(ctx context.Context, owner, pack, id string) (MemoryHistory, error) {
	if _, err := h.GetMemory(ctx, owner, pack, id); err != nil {
		return MemoryHistory{}, err
	}
	return h.Store.memoryHistory(owner, pack, id)
}
func memoryViews(items []Memory) []MemoryView {
	byKey := map[string]Memory{}
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
	views := []MemoryView{}
	for _, key := range keys {
		m := byKey[key]
		views = append(views, MemoryView{PackID: m.PackID, ID: m.ID, Key: m.Key, Value: m.Value, Kind: m.Kind, Scope: m.Scope, Revision: m.Revision})
	}
	return views
}
func setMemoryInput(envelope map[string]any, id, text string) {
	envelope["memory_inputs"] = []memoryInput{{ID: id, Text: text}}
}
func (h *AgentHost) prepareMemories(ctx context.Context, run StoredRun) (StoredRun, error) {
	raw, _ := CanonicalJSON(run.State["memory_inputs"])
	inputs := []memoryInput{}
	if run.State["memory_inputs"] != nil {
		if err := strictUnmarshal(raw, &inputs); err != nil {
			return run, hostError("run_state_invalid")
		}
	}
	extractor, enabled := h.Model.(MemoryExtractor)
	for _, input := range inputs {
		done, err := h.Store.memoryInputDone(run.OwnerID, input)
		if err != nil {
			return run, err
		}
		if done || !enabled {
			continue
		}
		existing, err := h.Store.listMemories(run.OwnerID, run.PackID, 32, 0)
		if err != nil {
			return run, err
		}
		known := []MemoryView{}
		for _, m := range existing {
			known = append(known, MemoryView{PackID: m.PackID, ID: m.ID, Key: m.Key, Value: m.Value, Kind: m.Kind, Scope: m.Scope, Revision: m.Revision})
		}
		request := MemoryExtractionRequest{Text: input.Text, Existing: known, MaxCharacters: h.Settings.MaxContextCharacters}
		_, extractErr := memoryExtractionInput(&request)
		var proposals []MemoryProposal
		if extractErr == nil {
			extractCtx, cancel := context.WithTimeout(ctx, time.Duration(h.Settings.ModelTimeoutSeconds*1e9))
			proposals, extractErr = extractor.ExtractMemories(extractCtx, request)
			cancel()
		}
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		if extractErr == nil {
			extractErr = validateProposals(input.Text, proposals)
		}
		code := ""
		if extractErr != nil {
			proposals = nil
			code = "memory_extraction_failed"
			if ErrorCode(extractErr) == "memory_extraction_too_large" {
				code = "memory_extraction_too_large"
			}
		}
		if err = h.Store.applyMemoryInput(run, input, proposals, code); err != nil {
			return run, err
		}
	}
	errors := []JSON{}
	for _, input := range inputs {
		code, err := h.Store.memoryInputError(run.OwnerID, input.ID)
		if err != nil {
			return run, err
		}
		if code != "" {
			errors = append(errors, JSON{"source_id": input.ID, "code": code})
		}
	}
	run.State["memory_errors"] = errors
	if _, exists := run.State["memory_snapshot"]; !exists {
		items, err := h.Store.listMemories(run.OwnerID, run.PackID, -1, 0)
		if err != nil {
			return run, err
		}
		views := memoryViews(items)
		bindings, err := runBindings(run)
		if err != nil {
			return run, err
		}
		for _, binding := range bindings {
			source, err := h.Store.listMemories(run.OwnerID, binding.PackID, -1, 0)
			if err != nil {
				return run, err
			}
			private := []Memory{}
			for _, m := range source {
				if m.Scope == "pack" {
					private = append(private, m)
				}
			}
			views = append(views, memoryViews(private)...)
		}
		run.State["memory_snapshot"] = views
		state, err := h.restore(run)
		if err != nil {
			return run, err
		}
		return h.save(run, state, "", nil, JSON{"kind": "memories_loaded"})
	}
	return run, nil
}
func (h *AgentHost) runMemories(run StoredRun) ([]MemoryView, error) {
	raw, err := CanonicalJSON(run.State["memory_snapshot"])
	if err != nil {
		return nil, err
	}
	views := []MemoryView{}
	if run.State["memory_snapshot"] != nil {
		if err = strictUnmarshal(raw, &views); err != nil {
			return nil, hostError("run_state_invalid")
		}
	}
	bindings, err := runBindings(run)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{run.PackID: true}
	for _, b := range bindings {
		allowed[b.PackID] = true
	}
	out := []MemoryView{}
	for _, v := range views {
		pack := v.PackID
		if pack == "" {
			pack = run.PackID
		}
		if !allowed[pack] || pack != run.PackID && v.Scope != "pack" {
			continue
		}
		visible, err := h.Store.visibleMemorySnapshot(run.OwnerID, pack, []MemoryView{v})
		if err != nil {
			return nil, err
		}
		out = append(out, visible...)
	}
	return out, nil
}

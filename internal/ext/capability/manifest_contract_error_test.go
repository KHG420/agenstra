package capability

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestManifestContractErrorPreservesValidationCause(t *testing.T) {
	err := validateRawManifest([]byte(`{"schema":`), nil)
	var hostError *agentcontract.HostError
	if !errors.As(err, &hostError) || hostError.Code != "capability_contract_invalid" {
		t.Fatalf("local manifest parse lost safe contract code: %v", err)
	}
	// The strict decoder deliberately redacts malformed JSON to a fixed error;
	// OpenPack's typed header decode supplies an independently checkable cause.
	path := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(path, []byte(`{"schema":13}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = OpenPack(context.Background(), path, nil)
	var typeError *json.UnmarshalTypeError
	if !errors.As(err, &hostError) || hostError.Code != "capability_contract_invalid" || !errors.As(err, &typeError) {
		t.Fatalf("safe code or original typed decode error lost: %v", err)
	}
	err = validateRawManifest([]byte(`{"schema":"not-a-supported-pack-private-marker"}`), nil)
	if !errors.As(err, &hostError) || hostError.Code != "capability_contract_invalid" {
		t.Fatalf("semantic manifest error lost safe code: %v", err)
	}
}

func TestManifestContractErrorDoesNotMisclassifyMissingFiles(t *testing.T) {
	_, err := OpenPack(t.Context(), filepath.Join(t.TempDir(), "missing.json"), nil)
	var hostError *agentcontract.HostError
	if !errors.Is(err, os.ErrNotExist) || errors.As(err, &hostError) {
		t.Fatalf("filesystem availability error changed: %v", err)
	}
}

func TestManifestContractErrorMetadataBeforeEnvironmentResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(path, []byte(`{"schema":"agenstra.rest-pack.v2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenPack(t.Context(), path, nil)
	var hostError *agentcontract.HostError
	if !errors.As(err, &hostError) || hostError.Code != "capability_contract_invalid" {
		t.Fatalf("local metadata failure misclassified as unavailable environment: %v", err)
	}
}

func TestManifestContractErrorLeavesAuthoringValidationDetailed(t *testing.T) {
	// Developer draft validation remains separate from run opening and does not
	// turn an authoring detail into a lifecycle failure.
	err := ValidatePackManifest(agentcontract.JSON{"schema": "not-a-supported-pack"}, nil)
	var hostError *agentcontract.HostError
	if err == nil || errors.As(err, &hostError) {
		t.Fatalf("developer authoring validation changed: %v", err)
	}
}

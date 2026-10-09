package host

import (
	"errors"
	"fmt"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func validateResult(r agentcontract.CapabilityResult) error {
	if (r.Data == nil) == (r.ErrorCode == "") {
		return errors.New("capability result must contain data or an error code")
	}
	if r.ErrorCode != "" && len(r.ErrorCode) > 120 {
		return fmt.Errorf("invalid error code")
	}
	return nil
}

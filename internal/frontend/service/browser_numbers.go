package service

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// browserNumbersRoundTrip checks the decimal value after binary64 JSON parsing.
// It does not replace arguments with the rounded values. Backend providers keep
// their original exact-number support.
func browserNumbersRoundTrip(arguments agentcontract.JSON) bool {
	raw, err := json.Marshal(arguments)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	return browserNumberValueRoundTrips(value)
}

func browserNumberValueRoundTrips(value any) bool {
	switch v := value.(type) {
	case json.Number:
		number, err := v.Float64()
		if err != nil {
			return false
		}
		if number == 0 {
			// Avoid expanding an arbitrarily small exponent into a rational. A
			// nonzero mantissa that underflows binary64 cannot retain its value.
			mantissa, _, _ := strings.Cut(strings.ToLower(string(v)), "e")
			return !strings.ContainsAny(mantissa, "123456789")
		}
		original, originalOK := new(big.Rat).SetString(string(v))
		rounded, roundedOK := new(big.Rat).SetString(strconv.FormatFloat(number, 'g', -1, 64))
		return originalOK && roundedOK && original.Cmp(rounded) == 0
	case map[string]any:
		for _, child := range v {
			if !browserNumberValueRoundTrips(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !browserNumberValueRoundTrips(child) {
				return false
			}
		}
	}
	return true
}

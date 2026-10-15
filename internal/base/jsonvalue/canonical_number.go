package jsonvalue

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

// canonicalDecimalNumber keeps the existing float-token notation without rounding
// a decoded decimal through binary64. Ordinary Go floats still use Float.
func canonicalDecimalNumber(value json.Number) (json.Number, error) {
	// Retain the existing finite-range check; its rounded value is never encoded.
	if _, err := value.Float64(); err != nil {
		return "", err
	}
	raw := string(value)
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	var power big.Int
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		if _, ok := power.SetString(raw[index+1:], 10); !ok {
			return "", errors.New("invalid decimal JSON exponent")
		}
		raw = raw[:index]
	}
	fractionDigits := 0
	if index := strings.IndexByte(raw, '.'); index >= 0 {
		fractionDigits = len(raw) - index - 1
		raw = raw[:index] + raw[index+1:]
	}
	digits := strings.TrimLeft(raw, "0")
	if digits == "" {
		if negative {
			return json.Number("-0.0"), nil
		}
		return json.Number("0.0"), nil
	}
	significant := strings.TrimRight(digits, "0")
	power.Add(&power, big.NewInt(int64(len(digits)-len(significant)-fractionDigits)))
	digits = significant
	var exponent big.Int
	exponent.Add(&power, big.NewInt(int64(len(digits)-1)))
	prefix := ""
	if negative {
		prefix = "-"
	}
	if exponent.Cmp(big.NewInt(-4)) >= 0 && exponent.Cmp(big.NewInt(16)) < 0 {
		point := int(exponent.Int64()) + 1
		switch {
		case point <= 0:
			return json.Number(prefix + "0." + strings.Repeat("0", -point) + digits), nil
		case point < len(digits):
			return json.Number(prefix + digits[:point] + "." + digits[point:]), nil
		default:
			return json.Number(prefix + digits + strings.Repeat("0", point-len(digits)) + ".0"), nil
		}
	}
	coefficient := digits[:1]
	if len(digits) > 1 {
		coefficient += "." + digits[1:]
	}
	sign := "+"
	if exponent.Sign() < 0 {
		sign = "-"
	}
	absolute := new(big.Int).Abs(&exponent).String()
	if len(absolute) < 2 {
		absolute = "0" + absolute
	}
	return json.Number(prefix + coefficient + "e" + sign + absolute), nil
}

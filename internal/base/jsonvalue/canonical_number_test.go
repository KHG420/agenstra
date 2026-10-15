package jsonvalue

import (
	"encoding/json"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

func TestCanonicalDecimalNumbersKeepExactValue(t *testing.T) {
	tests := []struct{ input, want string }{
		{"9007199254740993.0", "9007199254740993.0"},
		{"9223372036854775807.0", "9.223372036854775807e+18"},
		{"0.100000000000000000001", "0.100000000000000000001"},
		{"1e-400", "1e-400"},
		{"-1.250000000000000000001e-10", "-1.250000000000000000001e-10"},
		{"1.230000e+6", "1230000.0"},
		{"10000e-8", "0.0001"},
		{"10e-8", "1e-07"},
		{"10.0e15", "1e+16"},
		{"-0.000e+2", "-0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			raw, err := Canonical(json.Number(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tt.want {
				t.Fatalf("number changed: got %s, want %s", raw, tt.want)
			}
			before, ok := new(big.Rat).SetString(tt.input)
			if !ok {
				t.Fatal("invalid test input")
			}
			after, ok := new(big.Rat).SetString(string(raw))
			if !ok || before.Cmp(after) != 0 {
				t.Fatalf("exact numerical value changed: %s -> %s", tt.input, raw)
			}
		})
	}
}

func TestCanonicalNumbersRetainLegacyFloatEncoding(t *testing.T) {
	values := []float64{0, math.Copysign(0, -1), 1, -1, .1, 1e-4, math.Nextafter(1e-4, 0), 1e16, math.Nextafter(1e16, 0), math.SmallestNonzeroFloat64, math.MaxFloat64}
	random := rand.New(rand.NewSource(15102026))
	for range 2000 {
		values = append(values, math.Float64frombits(random.Uint64()))
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		// Float is the unchanged producer of previously persisted canonical float tokens.
		old, err := Float(value)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := Canonical(json.Number(old))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != old {
			t.Fatalf("legacy persisted float changed: %s -> %s", old, raw)
		}
	}
}

func TestCanonicalDecimalNumbersRetainRangeAndTypeBoundaries(t *testing.T) {
	for _, input := range []string{"1e309", "-1e309"} {
		if _, err := Canonical(json.Number(input)); err == nil {
			t.Fatalf("previously rejected overflow accepted: %s", input)
		}
	}
	for _, tt := range []struct{ input, want string }{{"2", "2"}, {"2.0", "2.0"}, {"2e0", "2.0"}, {"9223372036854775807", "9223372036854775807"}} {
		raw, err := Canonical(json.Number(tt.input))
		if err != nil || string(raw) != tt.want {
			t.Fatalf("existing integer/float distinction changed: %s -> %s, %v", tt.input, raw, err)
		}
	}
}

// Package jsonvalue implements the framework's JSON boundary operations.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// Canonical encodes finite JSON with stable key ordering and numeric formatting.
// It rejects unsupported and cyclic values; the returned bytes belong to the caller.
func Canonical(v any) ([]byte, error) {
	// Validate with encoding/json before walking maps and pointers ourselves.
	// This rejects cyclic or unsupported host values without unbounded recursion.
	if _, err := json.Marshal(v); err != nil {
		return nil, err
	}
	if err := finiteJSON(v); err != nil {
		return nil, err
	}
	prepared, err := canonicalPrepare(v)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(prepared)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	canonical := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	return unescapeJSONSeparators(canonical), nil
}

// Float formats a finite float using the framework's canonical JSON number rules.
func Float(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", errors.New("JSON numbers must be finite")
	}
	if v == 0 {
		if math.Signbit(v) {
			return "-0.0", nil
		}
		return "0.0", nil
	}
	abs := math.Abs(v)
	if abs >= 1e-4 && abs < 1e16 {
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s, nil
	}
	s := strconv.FormatFloat(v, 'e', -1, 64)
	parts := strings.SplitN(s, "e", 2)
	exponent, _ := strconv.Atoi(parts[1]) //nolint:errcheck // FormatFloat emits a bounded numeric exponent.
	return fmt.Sprintf("%se%+03d", parts[0], exponent), nil
}
func canonicalPrepare(v any) (any, error) {
	switch x := v.(type) {
	case float64:
		s, e := Float(x)
		if e != nil {
			return nil, e
		}
		return json.Number(s), nil
	case float32:
		s, e := Float(float64(x))
		if e != nil {
			return nil, e
		}
		return json.Number(s), nil
	case json.Number:
		raw := string(x)
		if strings.ContainsAny(raw, ".eE") {
			n, e := x.Float64()
			if e != nil {
				return nil, e
			}
			s, e := Float(n)
			if e != nil {
				return nil, e
			}
			return json.Number(s), nil
		}
		return x, nil
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, item := range x {
			prepared, e := canonicalPrepare(item)
			if e != nil {
				return nil, e
			}
			m[k] = prepared
		}
		return m, nil
	}
	if v == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		return canonicalPrepare(rv.Elem().Interface())
	}
	if marshaler, ok := v.(json.Marshaler); ok {
		raw, e := marshaler.MarshalJSON()
		if e != nil {
			return nil, e
		}
		var result any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if e := dec.Decode(&result); e != nil {
			return nil, e
		}
		return canonicalPrepare(result)
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		items := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			prepared, e := canonicalPrepare(rv.Index(i).Interface())
			if e != nil {
				return nil, e
			}
			items[i] = prepared
		}
		return items, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return v, nil
		}
		m := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			prepared, e := canonicalPrepare(iter.Value().Interface())
			if e != nil {
				return nil, e
			}
			m[iter.Key().String()] = prepared
		}
		return m, nil
	case reflect.Struct:
		raw, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if e := dec.Decode(&m); e != nil {
			return nil, e
		}
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if field.Anonymous && tag == "" {
				prepared, e := canonicalPrepare(rv.Field(i).Interface())
				if e != nil {
					return nil, e
				}
				if embedded, ok := prepared.(map[string]any); ok {
					for key, value := range embedded {
						if _, exists := m[key]; exists {
							m[key] = value
						}
					}
				}
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			if _, exists := m[tag]; !exists {
				continue
			}
			prepared, e := canonicalPrepare(rv.Field(i).Interface())
			if e != nil {
				return nil, e
			}
			m[tag] = prepared
		}
		return m, nil
	default:
		return v, nil
	}
}
func unescapeJSONSeparators(raw []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			out.WriteByte(raw[i])
			i++
			continue
		}
		start := i
		for i < len(raw) && raw[i] == '\\' {
			i++
		}
		count := i - start
		if count%2 == 1 && i+5 <= len(raw) && raw[i] == 'u' && (bytes.Equal(raw[i:i+5], []byte("u2028")) || bytes.Equal(raw[i:i+5], []byte("u2029"))) {
			out.Write(raw[start : i-1])
			if raw[i+4] == '8' {
				out.Write([]byte("\xe2\x80\xa8"))
			} else {
				out.Write([]byte("\xe2\x80\xa9"))
			}
			i += 5
			continue
		}
		out.Write(raw[start:i])
	}
	return out.Bytes()
}
func finiteJSON(v any) error {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return errors.New("JSON numbers must be finite")
		}
	case float32:
		if math.IsNaN(float64(t)) || math.IsInf(float64(t), 0) {
			return errors.New("JSON numbers must be finite")
		}
	case map[string]any:
		for _, x := range t {
			if err := finiteJSON(x); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range t {
			if err := finiteJSON(x); err != nil {
				return err
			}
		}
	}
	return nil
}

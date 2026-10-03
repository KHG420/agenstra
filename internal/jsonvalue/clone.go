package jsonvalue

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

// Clone copies boundary data without normalizing Go numbers or typed slices.
// JSON validation runs first so recursive copying cannot walk cyclic values.
func Clone[T any](value T) (T, error) {
	var zero T
	original, err := json.Marshal(value)
	if err != nil {
		return zero, err
	}
	type reference struct {
		typeOf  reflect.Type
		pointer uintptr
		length  int
	}
	active := map[reference]bool{}
	var copyValue func(reflect.Value) (reflect.Value, error)
	copyValue = func(v reflect.Value) (reflect.Value, error) {
		if !v.IsValid() {
			return v, nil
		}
		// time.Time is immutable; its location data is shared by the standard library.
		if v.Type() == reflect.TypeFor[time.Time]() {
			return v, nil
		}
		if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && !v.IsNil() {
			ref := reference{typeOf: v.Type(), pointer: uintptr(v.UnsafePointer())}
			if v.Kind() == reflect.Slice {
				ref.length = v.Len()
			}
			if active[ref] {
				return reflect.Value{}, errors.New("cyclic JSON boundary value")
			}
			active[ref] = true
			defer delete(active, ref)
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer:
			if v.IsNil() {
				return reflect.Zero(v.Type()), nil
			}
			elem, err := copyValue(v.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			if v.Kind() == reflect.Pointer {
				out := reflect.New(v.Type()).Elem()
				out.Set(reflect.New(v.Type().Elem()))
				out.Elem().Set(elem)
				return out, nil
			}
			out := reflect.New(v.Type()).Elem()
			out.Set(elem)
			return out, nil
		case reflect.Map:
			if v.IsNil() {
				return reflect.Zero(v.Type()), nil
			}
			out := reflect.MakeMapWithSize(v.Type(), v.Len())
			iter := v.MapRange()
			for iter.Next() {
				elem, err := copyValue(iter.Value())
				if err != nil {
					return reflect.Value{}, err
				}
				out.SetMapIndex(iter.Key(), elem)
			}
			return out, nil
		case reflect.Slice, reflect.Array:
			out := reflect.New(v.Type()).Elem()
			if v.Kind() == reflect.Slice {
				if v.IsNil() {
					return out, nil
				}
				out = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			}
			for i := 0; i < v.Len(); i++ {
				elem, err := copyValue(v.Index(i))
				if err != nil {
					return reflect.Value{}, err
				}
				out.Index(i).Set(elem)
			}
			return out, nil
		case reflect.Struct:
			out := reflect.New(v.Type()).Elem()
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if !field.IsExported() || strings.Split(field.Tag.Get("json"), ",")[0] == "-" {
					// Non-JSON state can contain locks or resources; leave it zero in the copy.
					continue
				}
				elem, err := copyValue(v.Field(i))
				if err != nil {
					return reflect.Value{}, err
				}
				out.Field(i).Set(elem)
			}
			return out, nil
		default:
			return v, nil
		}
	}
	copy, err := copyValue(reflect.ValueOf(value))
	if err != nil {
		return zero, err
	}
	if !copy.IsValid() {
		return zero, nil
	}
	result := copy.Interface().(T)
	encoded, err := json.Marshal(result)
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(original, encoded) {
		return zero, errors.New("JSON boundary depends on opaque Go state")
	}
	return result, nil
}

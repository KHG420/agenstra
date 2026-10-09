package jsonvalue

import (
	"reflect"
	"testing"
	"time"
)

func TestCloneRetainsTypesAndOwnsNestedValues(t *testing.T) {
	type value struct {
		Items map[string][]int `json:"items"`
		Name  *string          `json:"name"`
		Time  time.Time        `json:"time"`
	}
	name := "original"
	input := value{Items: map[string][]int{"ids": {1, 2}}, Name: &name, Time: time.Now()}
	copy, err := Clone(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copy, input) {
		t.Fatalf("cloning changed the host's Go values: %+v", copy)
	}
	copy.Items["ids"][0] = 99
	copy.Items["extra"] = []int{3}
	*copy.Name = "changed"
	if input.Items["ids"][0] != 1 || len(input.Items) != 1 || *input.Name != "original" {
		t.Fatalf("the clone changed caller-owned state: %+v", input)
	}
}

func TestCloneRejectsCycles(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := Clone(cycle); err == nil {
		t.Fatal("cyclic boundary value accepted")
	}
}

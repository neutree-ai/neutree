package util

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestDeepCopy_Slices(t *testing.T) {
	t.Run("string slice", func(t *testing.T) {
		input := []string{"a", "b", "c"}
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}

		// Verify values are equal
		if len(result) != len(input) {
			t.Fatalf("length mismatch: got %d, want %d", len(result), len(input))
		}
		for i := range input {
			if result[i] != input[i] {
				t.Errorf("result[%d] = %v, want %v", i, result[i], input[i])
			}
		}

		// Verify it's a true copy (modifying result shouldn't affect input)
		result[0] = "modified"
		if input[0] == "modified" {
			t.Error("DeepCopyObject() did not create independent copy")
		}
	})

	t.Run("int slice", func(t *testing.T) {
		input := []int{1, 2, 3, 4, 5}
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}

		// Verify it's independent
		result[0] = 999
		if input[0] == 999 {
			t.Error("DeepCopyObject() did not create independent copy")
		}
	})
}

func TestDeepCopy_Maps(t *testing.T) {
	t.Run("simple map", func(t *testing.T) {
		input := map[string]int{
			"one":   1,
			"two":   2,
			"three": 3,
		}
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}

		// Verify values
		for k, v := range input {
			if result[k] != v {
				t.Errorf("result[%s] = %v, want %v", k, result[k], v)
			}
		}

		// Verify independence
		result["one"] = 999
		if input["one"] == 999 {
			t.Error("DeepCopyObject() did not create independent copy")
		}
	})

	t.Run("nested map", func(t *testing.T) {
		input := map[string]map[string]string{
			"level1": {
				"key1": "value1",
				"key2": "value2",
			},
		}
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}

		// Verify independence of nested map
		result["level1"]["key1"] = "modified"
		if input["level1"]["key1"] == "modified" {
			t.Error("DeepCopyObject() did not create independent copy of nested map")
		}
	})
}

func TestDeepCopy_Structs(t *testing.T) {
	t.Run("simple struct", func(t *testing.T) {
		type Person struct {
			Name string
			Age  int
		}
		input := Person{Name: "Alice", Age: 30}
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}

		if result.Name != input.Name || result.Age != input.Age {
			t.Errorf("DeepCopyObject() = %+v, want %+v", result, input)
		}

		// Verify independence
		result.Name = "Bob"
		if input.Name == "Bob" {
			t.Error("DeepCopyObject() did not create independent copy")
		}
	})

	t.Run("struct with slice", func(t *testing.T) {
		type Data struct {
			Items []string
		}
		input := Data{Items: []string{"a", "b", "c"}}
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}

		// Verify independence of slice
		result.Items[0] = "modified"
		if input.Items[0] == "modified" {
			t.Error("DeepCopyObject() did not create independent copy of slice")
		}
	})
}

func TestDeepCopy_NilValues(t *testing.T) {
	t.Run("nil pointer", func(t *testing.T) {
		var input *v1.EngineSpec
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}
		if result != nil {
			t.Errorf("DeepCopyObject() of nil pointer = %v, want nil", result)
		}
	})

	t.Run("struct with nil fields", func(t *testing.T) {
		input := &v1.EngineSpec{
			Versions:       nil,
			SupportedTasks: nil,
		}
		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}
		if result.Versions != nil {
			t.Errorf("DeepCopyObject() Versions = %v, want nil", result.Versions)
		}
		if result.SupportedTasks != nil {
			t.Errorf("DeepCopyObject() SupportedTasks = %v, want nil", result.SupportedTasks)
		}
	})
}

func TestDeepCopy_ComplexNesting(t *testing.T) {
	t.Run("deeply nested structure", func(t *testing.T) {
		type Inner struct {
			Value string
		}
		type Middle struct {
			Inners []*Inner
			Data   map[string][]int
		}
		type Outer struct {
			Middles []*Middle
		}

		input := &Outer{
			Middles: []*Middle{
				{
					Inners: []*Inner{
						{Value: "test1"},
						{Value: "test2"},
					},
					Data: map[string][]int{
						"key1": {1, 2, 3},
					},
				},
			},
		}

		result, err := DeepCopyObject(input)
		if err != nil {
			t.Fatalf("DeepCopyObject() error = %v", err)
		}

		// Verify independence at multiple levels
		result.Middles[0].Inners[0].Value = "modified"
		if input.Middles[0].Inners[0].Value == "modified" {
			t.Error("DeepCopyObject() did not create independent copy of nested slice element")
		}

		result.Middles[0].Data["key1"][0] = 999
		if input.Middles[0].Data["key1"][0] == 999 {
			t.Error("DeepCopyObject() did not create independent copy of nested map slice")
		}
	})
}

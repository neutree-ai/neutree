package util

import (
	"encoding/json"

	jsonpatchv5 "github.com/evanphx/json-patch/v5"
	jd "github.com/josephburnett/jd/lib"
)

func JsonEqual(obj1, obj2 interface{}) (bool, string, error) {
	json1, err := json.Marshal(obj1)
	if err != nil {
		return false, "", err
	}

	json2, err := json.Marshal(obj2)
	if err != nil {
		return false, "", err
	}

	if string(json1) == string(json2) {
		return true, "", nil
	}

	var diff string

	j1, err1 := jd.ReadJsonString(string(json1))
	j2, err2 := jd.ReadJsonString(string(json2))

	if err1 == nil && err2 == nil {
		diff = j1.Diff(j2).Render()
	}

	return false, diff, nil
}

// JsonContains checks if all fields in desired exist in current with matching values.
// For objects, every key-value pair in desired must exist in current (current may have extra keys).
// For arrays, both must have the same length, and each element in desired must be contained
// in the corresponding element of current.
// Both inputs are normalized through JSON round-trip to ensure consistent types (e.g., int → float64).
func JsonContains(current, desired interface{}) (bool, string, error) {
	curJSON, err := json.Marshal(current)
	if err != nil {
		return false, "", err
	}

	desJSON, err := json.Marshal(desired)
	if err != nil {
		return false, "", err
	}

	var curNorm, desNorm interface{}
	if err := json.Unmarshal(curJSON, &curNorm); err != nil {
		return false, "", err
	}

	if err := json.Unmarshal(desJSON, &desNorm); err != nil {
		return false, "", err
	}

	if jsonContainsAll(curNorm, desNorm) {
		return true, "", nil
	}

	var diff string

	j1, err1 := jd.ReadJsonString(string(curJSON))
	j2, err2 := jd.ReadJsonString(string(desJSON))

	if err1 == nil && err2 == nil {
		diff = j1.Diff(j2).Render()
	}

	return false, diff, nil
}

func jsonContainsAll(current, desired interface{}) bool {
	if desired == nil {
		return current == nil
	}

	if current == nil {
		return false
	}

	switch d := desired.(type) {
	case map[string]interface{}:
		c, ok := current.(map[string]interface{})
		if !ok {
			return false
		}

		for key, dVal := range d {
			cVal, exists := c[key]

			if dVal == nil {
				if exists && cVal != nil {
					return false
				}

				continue
			}

			if !exists {
				return false
			}

			if !jsonContainsAll(cVal, dVal) {
				return false
			}
		}

		return true
	case []interface{}:
		c, ok := current.([]interface{})
		if !ok {
			return false
		}

		if len(c) != len(d) {
			return false
		}

		for i := range d {
			if !jsonContainsAll(c[i], d[i]) {
				return false
			}
		}

		return true
	default:
		return current == desired
	}
}

func JsonMerge(obj1, obj2, result interface{}) error {
	json1, err := json.Marshal(obj1)
	if err != nil {
		return err
	}

	json2, err := json.Marshal(obj2)
	if err != nil {
		return err
	}

	merge, err := jsonpatchv5.MergePatch(json1, json2)
	if err != nil {
		return err
	}

	err = json.Unmarshal(merge, result)
	if err != nil {
		return err
	}

	return nil
}

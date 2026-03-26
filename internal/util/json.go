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

// NormalizeJSON normalizes a value for stable comparison by JSON round-tripping
// (to unify Go types like int→float64, map[string]string→map[string]interface{})
// and stripping null values and empty maps. This handles Kong's config normalization
// where null fields are stored explicitly and nil maps become empty objects {}.
func NormalizeJSON(obj interface{}) (interface{}, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	var normalized interface{}
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}

	return stripEmpty(normalized), nil
}

func stripEmpty(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		m := make(map[string]interface{})

		for k, val := range t {
			if val == nil {
				continue
			}

			cleaned := stripEmpty(val)
			if cm, ok := cleaned.(map[string]interface{}); ok && len(cm) == 0 {
				continue
			}

			m[k] = cleaned
		}

		return m
	case []interface{}:
		a := make([]interface{}, len(t))
		for i, val := range t {
			a[i] = stripEmpty(val)
		}

		return a
	default:
		return v
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

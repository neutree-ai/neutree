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

package util

import "encoding/json"

// DeepCopyObject creates a deep copy of the input object using JSON marshal and unmarshal.
// For simple, JSON-serializable types, this is typically
// sufficient. For more complex types that rely on the above features, consider using a more
// specialized deep copy mechanism instead.
func DeepCopyObject[T any](input T) (T, error) {
	var output T

	content, err := json.Marshal(input)
	if err != nil {
		return output, err
	}

	err = json.Unmarshal(content, &output)

	return output, err
}

package bentoml

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ModelInfoMetadataKey is the model.yaml metadata key holding the model info
// fields a user filled in by hand. It lives in the model's own descriptor rather
// than in a database row because it describes the checkpoint, travels with it
// through export/import, and has no cross-model uniqueness to arbitrate.
const ModelInfoMetadataKey = "neutree_model_info"

// ReadManualModelInfo decodes the hand-filled model info out of a model.yaml
// metadata map. Absent or empty metadata yields a nil result and no error: a
// model nobody has annotated is the normal case.
func ReadManualModelInfo(metadata map[string]interface{}) (*v1.ModelInfo, error) {
	raw, ok := metadata[ModelInfoMetadataKey]
	if !ok || raw == nil {
		return nil, nil
	}

	// The value arrives as whatever the YAML decoder produced, so it round-trips
	// through JSON to land in the typed shape.
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.Wrapf(err, "re-encode %s metadata", ModelInfoMetadataKey)
	}

	var info v1.ModelInfo
	if err := json.Unmarshal(encoded, &info); err != nil {
		return nil, errors.Wrapf(err, "decode %s metadata", ModelInfoMetadataKey)
	}

	return &info, nil
}

// WriteManualModelInfo replaces the hand-filled model info recorded for a model
// version. A nil or empty info removes the block entirely.
//
// It rewrites only the metadata key: name, version and the on-disk location are
// left exactly as they are, because they are what every deployment path resolves
// against.
func WriteManualModelInfo(homePath, modelName, version string, info *v1.ModelInfo) error {
	model, err := GetModelDetail(homePath, modelName, version)
	if err != nil {
		return err
	}

	yamlPath := filepath.Join(ModelDir(homePath, model.Name, model.Version), ModelYAMLFileName)

	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		return errors.Wrap(err, "read model.yaml")
	}

	// Edited as a generic document rather than through ModelYAML: whatever the
	// tool that wrote this descriptor put in it that ModelYAML does not model
	// would otherwise be dropped on the way back out.
	var document map[string]interface{}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return errors.Wrap(err, "unmarshal model.yaml")
	}

	if document == nil {
		document = map[string]interface{}{}
	}

	metadata, ok := document["metadata"].(map[string]interface{})
	if !ok || metadata == nil {
		metadata = map[string]interface{}{}
	}

	if info == nil || isEmptyModelInfo(info) {
		delete(metadata, ModelInfoMetadataKey)
	} else {
		generic, err := genericModelInfo(info)
		if err != nil {
			return err
		}

		metadata[ModelInfoMetadataKey] = generic
	}

	document["metadata"] = metadata

	out, err := yaml.Marshal(document)
	if err != nil {
		return errors.Wrap(err, "marshal model.yaml")
	}

	return writeFileAtomically(yamlPath, out)
}

// genericModelInfo renders the typed info as plain maps, via its JSON tags, so
// the block reads the same in model.yaml as it does over the API.
func genericModelInfo(info *v1.ModelInfo) (map[string]interface{}, error) {
	encoded, err := json.Marshal(info)
	if err != nil {
		return nil, errors.Wrapf(err, "encode %s metadata", ModelInfoMetadataKey)
	}

	var generic map[string]interface{}
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return nil, errors.Wrapf(err, "encode %s metadata", ModelInfoMetadataKey)
	}

	return generic, nil
}

// writeFileAtomically replaces path in one rename, so a reader of model.yaml
// never observes a half-written descriptor.
func writeFileAtomically(path string, content []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return errors.Wrap(err, "create temp descriptor")
	}

	tmpName := tmp.Name()

	defer func() {
		// Harmless once the rename succeeded: the name no longer exists.
		_ = os.Remove(tmpName)
	}()

	if _, err = tmp.Write(content); err != nil {
		tmp.Close()

		return errors.Wrap(err, "write temp descriptor")
	}

	if err = tmp.Sync(); err != nil {
		tmp.Close()

		return errors.Wrap(err, "sync temp descriptor")
	}

	if err = tmp.Close(); err != nil {
		return errors.Wrap(err, "close temp descriptor")
	}

	if err = os.Chmod(tmpName, 0o644); err != nil {
		return errors.Wrap(err, "chmod temp descriptor")
	}

	return errors.Wrap(os.Rename(tmpName, path), "replace descriptor")
}

func isEmptyModelInfo(info *v1.ModelInfo) bool {
	encoded, err := json.Marshal(info)
	if err != nil {
		return false
	}

	return string(encoded) == "{}"
}

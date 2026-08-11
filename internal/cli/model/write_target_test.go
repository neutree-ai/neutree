package model

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func huggingFaceRegistry(name string) *v1.ModelRegistry {
	return &v1.ModelRegistry{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  "https://huggingface.co",
		},
	}
}

// The refusal has to name the registry and say what about it makes the write
// impossible; "operation not supported" alone leaves the reader guessing which
// of their registries they picked and whether it is a fault or a property.
func TestValidateWritableRegistryNamesTheRegistryAndItsKind(t *testing.T) {
	err := ValidateWritableRegistry(huggingFaceRegistry("public-hugging-face"), "public-hugging-face", ModelWritePush)
	require.EqualError(t, err,
		`cannot push models to model registry "public-hugging-face": `+
			`it is a hugging-face registry, which neutree reads from but never writes to`)

	err = ValidateWritableRegistry(huggingFaceRegistry("public-hugging-face"), "public-hugging-face", ModelWriteDelete)
	require.EqualError(t, err,
		`cannot delete models from model registry "public-hugging-face": `+
			`it is a hugging-face registry, which neutree reads from but never writes to`)
}

func TestValidateWritableRegistryAllowsRegistriesWeStore(t *testing.T) {
	bentoml := &v1.ModelRegistry{
		Metadata: &v1.Metadata{Name: "private-nfs"},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://nfs-server:/srv/models",
		},
	}

	require.NoError(t, ValidateWritableRegistry(bentoml, "private-nfs", ModelWritePush))
	require.NoError(t, ValidateWritableRegistry(bentoml, "private-nfs", ModelWriteDelete))
}

// The check reads a registry the command fetched, so an unusable one means the
// fetch is what went wrong. Refusing here would report the wrong problem, and a
// registry kind this build does not know about is the server's to judge.
func TestValidateWritableRegistryDefersWhenItCannotTell(t *testing.T) {
	require.NoError(t, ValidateWritableRegistry(nil, "whatever", ModelWritePush))
	require.NoError(t, ValidateWritableRegistry(&v1.ModelRegistry{}, "whatever", ModelWritePush))
	require.NoError(t, ValidateWritableRegistry(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{Type: "some-future-kind"},
	}, "whatever", ModelWritePush))
}

// The name comes off the fetched object, which is not guaranteed to carry
// metadata. Falling back to the name the user typed is what keeps the sentence
// readable: empty quotes in the middle of a refusal read as a broken tool, and
// the reader is left without the one word that says which registry was meant.
func TestValidateWritableRegistryFallsBackToTheRequestedName(t *testing.T) {
	err := ValidateWritableRegistry(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
	}, "public-hugging-face", ModelWriteDelete)

	require.EqualError(t, err,
		`cannot delete models from model registry "public-hugging-face": `+
			`it is a hugging-face registry, which neutree reads from but never writes to`)

	err = ValidateWritableRegistry(&v1.ModelRegistry{
		Metadata: &v1.Metadata{},
		Spec:     &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
	}, "public-hugging-face", ModelWritePush)

	require.EqualError(t, err,
		`cannot push models to model registry "public-hugging-face": `+
			`it is a hugging-face registry, which neutree reads from but never writes to`)
}

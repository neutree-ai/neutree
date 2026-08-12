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
// registry kind the visibility mapping does not know is private by that
// mapping's own rule — so it is the server's to judge, not this function's.
func TestValidateWritableRegistryDefersWhenItCannotTell(t *testing.T) {
	require.NoError(t, ValidateWritableRegistry(nil, "whatever", ModelWritePush))
	require.NoError(t, ValidateWritableRegistry(&v1.ModelRegistry{}, "whatever", ModelWritePush))
	require.NoError(t, ValidateWritableRegistry(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{Type: "some-future-kind"},
	}, "whatever", ModelWritePush))
}

// What is refused is "public", not "hugging-face". Asserting it against the
// mapping rather than against the one kind that is public today is what makes
// the next read-only provider covered by this test the moment the mapping
// learns about it — which is the whole reason the judgement was moved there.
func TestValidateWritableRegistryRefusesWhateverTheMappingCallsPublic(t *testing.T) {
	for _, registryType := range []v1.ModelRegistryType{
		v1.HuggingFaceModelRegistryType,
		v1.ModelScopeModelRegistryType,
		v1.BentoMLModelRegistryType,
		"some-future-kind",
	} {
		err := ValidateWritableRegistry(&v1.ModelRegistry{
			Metadata: &v1.Metadata{Name: "a-registry"},
			Spec:     &v1.ModelRegistrySpec{Type: registryType},
		}, "a-registry", ModelWritePush)

		public := v1.VisibilityForModelRegistryType(registryType) == v1.ModelRegistryVisibilityPublic
		if public {
			require.Error(t, err, "kind %q is public and must be refused", registryType)
			continue
		}

		require.NoError(t, err, "kind %q is private and must be left to the server", registryType)
	}
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

// NEU-627's whole shape rests on this being true: ModelScope became writable-
// refusable in the CLI without a line of CLI code, because NEU-625 moved the
// judgement to v1.VisibilityForModelRegistryType and NEU-627 taught that one
// mapping about the kind.
//
// Asserted on the exact sentence rather than on "an error happened", because
// the claim is that the CLI names the new kind correctly without knowing it
// exists — a refusal reading "hugging-face" would mean something is still
// hard-coded, and a bare require.Error would not catch that.
func TestValidateWritableRegistryRefusesModelScopeWithNoCLIChange(t *testing.T) {
	modelScope := &v1.ModelRegistry{
		Metadata: &v1.Metadata{Name: "public-model-scope"},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.ModelScopeModelRegistryType,
			Url:  "https://www.modelscope.cn",
		},
	}

	require.EqualError(t,
		ValidateWritableRegistry(modelScope, "public-model-scope", ModelWritePush),
		`cannot push models to model registry "public-model-scope": `+
			`it is a model-scope registry, which neutree reads from but never writes to`)

	require.EqualError(t,
		ValidateWritableRegistry(modelScope, "public-model-scope", ModelWriteDelete),
		`cannot delete models from model registry "public-model-scope": `+
			`it is a model-scope registry, which neutree reads from but never writes to`)
}

package model_registry

import (
	"io"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var (
	// ErrNotSupported is returned by a registry that cannot perform an operation
	// at all, as opposed to one that tried and failed. Callers should test for it
	// with errors.Is and translate it into a "this registry kind cannot do that"
	// response rather than a server error.
	ErrNotSupported = errors.New("operation not supported by this model registry")
	// ErrNotFound is returned when the registry is reachable but the requested
	// object is not there.
	ErrNotFound = errors.New("not found in model registry")
)

type ListOption struct {
	Search string
	// Offset and Limit page the result. Offset past the end yields an empty page,
	// not an error. Paging is only coherent because the underlying listing is
	// ordered — see the sort in convertBentoMLModelsToGeneralModels.
	Offset int
	Limit  int
}

// ModelPage is one page of a listing plus the number of models that matched the
// search before Offset and Limit were applied, so a caller can render paging
// without fetching everything.
//
// Total is nil when the registry cannot state how many matched. That is a real
// answer, not a placeholder: reporting the page size instead would tell a client
// it had reached the end of a listing it had barely started.
type ModelPage struct {
	Models []v1.GeneralModel
	Total  *int
}

// KnownTotal builds a Total for a registry that can count its own contents.
func KnownTotal(total int) *int {
	return &total
}

// MaxReadmeBytes caps how much of a model's README is read.
//
// A README is arbitrary content from outside this system: a file in a directory
// anybody with push rights can write, or a document on a public hub. It is
// displayed, not processed, and no display needs a megabyte, so the limit is
// applied while reading rather than after — an unbounded read is the failure
// mode, not an unbounded response.
const MaxReadmeBytes = 1 << 20

// Readme is a model's README as the registry holds it: markdown exactly as
// stored, never rendered, because rendering someone else's markup into HTML on
// the server would make this an injection vector for every client that displays
// it.
type Readme struct {
	// Content is the markdown, truncated to MaxReadmeBytes.
	Content string
	// Truncated says the file was longer than MaxReadmeBytes and Content is its
	// beginning. A reader that cannot tell a truncated document from a complete
	// one has no way to say so to the person looking at it.
	Truncated bool
}

// RegistryUsage is a raw observation of what a registry's backing storage holds.
// It is what the registry can see; turning it into a status is the aggregator's
// job.
type RegistryUsage struct {
	// ModelCount is the number of distinct model names.
	ModelCount int
	// StorageBytes is the on-disk size of the model tree, obtained by walking it.
	StorageBytes int64
}

// ModelRegistry defines the interface for model registry operations
type ModelRegistry interface {
	// Basic registry operations
	ListModels(option ListOption) (*ModelPage, error)
	Connect() error
	Disconnect() error
	HealthyCheck() error

	// Model operations
	GetModelVersion(name, version string) (*v1.ModelVersion, error)
	// GetModelDetail returns everything known about one model version: what the
	// registry records about it plus whatever its checkpoint files state. It is
	// the read path for a model's detail view and is the expensive one — it opens
	// the checkpoint. GetModelVersion stays the cheap call for code that only
	// needs to resolve a version or read its recorded metadata.
	GetModelDetail(name, version string) (*v1.ModelVersion, error)
	// GetReadme returns the model's README.md verbatim, capped at MaxReadmeBytes.
	// It wraps ErrNotFound when the model has no README, and ErrNotSupported on a
	// registry that does not serve one. Those two are kept apart deliberately: "no
	// README was written" and "this registry cannot tell you" are different
	// answers, and collapsing them would leave a caller unable to say which it got.
	GetReadme(name, version string) (*Readme, error)
	DeleteModel(name, version string) error
	ImportModel(reader io.Reader, name, version string, progress io.Writer) error
	ExportModel(name, version, outputPath string) error
	GetModelPath(name, version string) (string, error)
	// SetManualModelInfo records the model info fields a user filled in by hand,
	// replacing any previously recorded set. Registries that do not carry
	// user-supplied metadata wrap ErrNotSupported.
	SetManualModelInfo(name, version string, info *v1.ModelInfo) error

	// CollectUsage walks the registry's backing storage and reports what it
	// currently holds. Registries whose storage is not ours to measure wrap
	// ErrNotSupported.
	CollectUsage() (*RegistryUsage, error)

	// GetNFSVersion returns the NFS protocol version (e.g. "3", "4", "4.1")
	// detected from the control-plane mount. Returns empty string for non-NFS registries.
	GetNFSVersion() (string, error)
}

type NewModelRegistryFunc func(registry *v1.ModelRegistry) (ModelRegistry, error)

var (
	NewModelRegistry NewModelRegistryFunc = new
)

func new(registry *v1.ModelRegistry) (ModelRegistry, error) {
	switch registry.Spec.Type {
	case v1.HuggingFaceModelRegistryType:
		return newHuggingFace(registry)
	case v1.BentoMLModelRegistryType:
		return newFileBased(registry)
	default:
		return nil, errors.New("unsupported model registry type: " + string(registry.Spec.Type))
	}
}

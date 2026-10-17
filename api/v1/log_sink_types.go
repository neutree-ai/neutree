package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type LogSinkPhase string

const (
	LogSinkPhasePENDING  LogSinkPhase = "Pending"
	LogSinkPhaseRUNNING  LogSinkPhase = "Running"
	LogSinkPhaseDEGRADED LogSinkPhase = "Degraded"
	LogSinkPhaseFAILED   LogSinkPhase = "Failed"
	LogSinkPhaseDISABLED LogSinkPhase = "Disabled"
	LogSinkPhaseDELETED  LogSinkPhase = "Deleted"
)

const (
	LogSinkTypeVictoriaLogs = "victorialogs"

	LogSinkAuthTypeNone   = "none"
	LogSinkAuthTypeBasic  = "basic"
	LogSinkAuthTypeBearer = "bearer"

	// LogSinkBufferDefaultMaxSize is the disk buffer size used when spec.buffer.max_size is unset.
	LogSinkBufferDefaultMaxSize int64 = 1 << 30
	// LogSinkBufferMinMaxSize is the smallest disk buffer Vector accepts.
	LogSinkBufferMinMaxSize int64 = 268435488
)

// LogSink is an external log store that AI traces are replicated to. It is a
// global resource: metadata.workspace is always empty.
type LogSink struct {
	APIVersion string         `json:"api_version,omitempty"`
	ID         int            `json:"id,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   *Metadata      `json:"metadata,omitempty"`
	Spec       *LogSinkSpec   `json:"spec,omitempty"`
	Status     *LogSinkStatus `json:"status,omitempty"`
}

type LogSinkSpec struct {
	Type         string                `json:"type"`
	Enabled      *bool                 `json:"enabled,omitempty"`
	VictoriaLogs *VictoriaLogsSinkSpec `json:"victorialogs,omitempty"`
	Buffer       *LogSinkBuffer        `json:"buffer,omitempty"`
}

// IsEnabled reports whether the sink should receive traces; an unset value means enabled.
func (in *LogSinkSpec) IsEnabled() bool {
	if in == nil || in.Enabled == nil {
		return true
	}

	return *in.Enabled
}

type VictoriaLogsSinkSpec struct {
	URL                string            `json:"url"`
	CACert             string            `json:"ca_cert,omitempty"`
	InsecureSkipVerify bool              `json:"insecure_skip_verify,omitempty"`
	TimeoutSeconds     int               `json:"timeout_seconds,omitempty"`
	Auth               *LogSinkAuth      `json:"auth,omitempty"`
	ExtraFields        map[string]string `json:"extra_fields,omitempty"`
}

type LogSinkAuth struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty" api:"-"`
	Token    string `json:"token,omitempty" api:"-"`
}

type LogSinkBuffer struct {
	// MaxSize is the disk buffer size in bytes.
	MaxSize int64 `json:"max_size,omitempty"`
}

// GetMaxSize returns the configured disk buffer size, or the default when unset.
func (in *LogSinkBuffer) GetMaxSize() int64 {
	if in == nil || in.MaxSize == 0 {
		return LogSinkBufferDefaultMaxSize
	}

	return in.MaxSize
}

type LogSinkStatus struct {
	Phase              LogSinkPhase `json:"phase,omitempty"`
	LastTransitionTime string       `json:"last_transition_time,omitempty"`
	ErrorMessage       string       `json:"error_message,omitempty"`
	// ObservedHash fingerprints the spec and secrets the controller last acted on.
	ObservedHash       string                 `json:"observed_hash,omitempty"`
	SentEvents         int64                  `json:"sent_events,omitempty"`
	DroppedEvents      int64                  `json:"dropped_events,omitempty"`
	LastSentTime       string                 `json:"last_sent_time,omitempty"`
	LastConnectionTest *LogSinkConnectionTest `json:"last_connection_test,omitempty"`
}

type LogSinkConnectionTest struct {
	Time    string `json:"time"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

func (obj *LogSink) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *LogSink) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *LogSink) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *LogSink) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *LogSink) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *LogSink) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *LogSink) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *LogSink) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *LogSink) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *LogSink) GetSpec() interface{} {
	return obj.Spec
}

func (obj *LogSink) GetStatus() interface{} {
	return obj.Status
}

func (obj *LogSink) GetKind() string {
	return obj.Kind
}

func (obj *LogSink) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *LogSink) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *LogSink) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *LogSink) GetMetadata() interface{} {
	return obj.Metadata
}

// LogSinkList is a list of LogSink resources
type LogSinkList struct {
	Kind  string    `json:"kind"`
	Items []LogSink `json:"items"`
}

func (in *LogSinkList) GetKind() string {
	return in.Kind
}

func (in *LogSinkList) SetKind(kind string) {
	in.Kind = kind
}

func (in *LogSinkList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *LogSinkList) SetItems(objs []scheme.Object) {
	items := make([]LogSink, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*LogSink) //nolint:errcheck
	}

	in.Items = items
}

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
	LogSinkTypeS3           = "s3"

	LogSinkAuthTypeNone   = "none"
	LogSinkAuthTypeBasic  = "basic"
	LogSinkAuthTypeBearer = "bearer"

	// LogSinkBufferDefaultMaxSize is the disk buffer size used when spec.buffer.max_size is unset.
	LogSinkBufferDefaultMaxSize int64 = 1 << 30
	// LogSinkBufferMinMaxSize is the smallest disk buffer Vector accepts.
	LogSinkBufferMinMaxSize int64 = 268435488

	LogSinkS3CompressionGzip = "gzip"
	LogSinkS3CompressionZstd = "zstd"
	LogSinkS3CompressionNone = "none"

	// LogSinkS3DefaultKeyPrefix is used when spec.s3.key_prefix is unset.
	LogSinkS3DefaultKeyPrefix = "neutree/%Y/%m/%d/"
	// LogSinkS3DefaultFlushIntervalSeconds is used when spec.s3.flush_interval_seconds is unset.
	LogSinkS3DefaultFlushIntervalSeconds = 300
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
	S3           *S3SinkSpec           `json:"s3,omitempty"`
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

// S3SinkSpec writes traces as compressed NDJSON objects to an S3 bucket or an
// S3-compatible store.
type S3SinkSpec struct {
	// Endpoint is the store's URL; empty means the AWS endpoint for Region.
	Endpoint string `json:"endpoint,omitempty"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	// KeyPrefix may hold strftime placeholders, evaluated in UTC when a batch
	// is created. Empty means LogSinkS3DefaultKeyPrefix.
	KeyPrefix string `json:"key_prefix,omitempty"`
	// ForcePathStyle addresses the bucket as <endpoint>/<bucket>; unset means true.
	ForcePathStyle *bool `json:"force_path_style,omitempty"`
	// Compression is gzip, zstd or none; empty means gzip.
	Compression string `json:"compression,omitempty"`
	// FlushIntervalSeconds is the longest an event waits in a batch; 0 means
	// LogSinkS3DefaultFlushIntervalSeconds.
	FlushIntervalSeconds int         `json:"flush_interval_seconds,omitempty"`
	Auth                 *S3SinkAuth `json:"auth,omitempty"`
}

type S3SinkAuth struct {
	AccessKeyID     string `json:"access_key_id,omitempty" api:"-"`
	SecretAccessKey string `json:"secret_access_key,omitempty" api:"-"`
}

// GetKeyPrefix returns the key prefix, or the default when unset.
func (in *S3SinkSpec) GetKeyPrefix() string {
	if in.KeyPrefix == "" {
		return LogSinkS3DefaultKeyPrefix
	}

	return in.KeyPrefix
}

// UsePathStyle reports whether the bucket is addressed by path; unset means true.
func (in *S3SinkSpec) UsePathStyle() bool {
	return in.ForcePathStyle == nil || *in.ForcePathStyle
}

// GetCompression returns the compression, or gzip when unset.
func (in *S3SinkSpec) GetCompression() string {
	if in.Compression == "" {
		return LogSinkS3CompressionGzip
	}

	return in.Compression
}

// GetFlushIntervalSeconds returns the flush interval, or the default when unset.
func (in *S3SinkSpec) GetFlushIntervalSeconds() int {
	if in.FlushIntervalSeconds <= 0 {
		return LogSinkS3DefaultFlushIntervalSeconds
	}

	return in.FlushIntervalSeconds
}

type LogSinkBuffer struct {
	// MaxSize is the usable disk buffer size in bytes. Vector reserves one more
	// 128MiB data file on top of it, so disk usage can reach MaxSize+128MiB.
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
	// ComponentID is the Vector sink ID of the current activation. The controller
	// assigns a new one each time the sink starts being delivered and clears it
	// when the sink stops being delivered (e.g. disabled).
	ComponentID string `json:"component_id,omitempty"`
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

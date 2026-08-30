package neutreemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	metricsnormalizer "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/runtimeusage"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const (
	defaultHTTPTimeout = 5 * time.Second
)

type Config struct {
	ListenAddress        string
	Labels               adapter.CanonicalLabels
	ScrapeTargetProvider ScrapeTargetProvider
	// ClusterType identifies the topology used to select an optional adapter
	// capability. It remains internal host configuration, not adapter state.
	ClusterType string
	// AcceleratorType selects the accelerator adapter from the registry. An
	// empty type emits only generic node and runtime metrics.
	AcceleratorType string
	// AcceleratorExporterPort and AcceleratorExporterMetricsPath are the
	// profile-derived endpoint. When a profile does not project a target, the
	// topology-specific default target remains in effect.
	AcceleratorExporterPort        int
	AcceleratorExporterMetricsPath string
	// VirtualizationMetricsTarget is the profile-owned Kubernetes monitor endpoint.
	// The generic host uses it only to obtain raw exposition; the selected
	// accelerator adapter interprets that exposition when building metrics.
	VirtualizationMetricsTarget *v1.MetricsTargetProfile
	// Accelerators is the registered accelerator adapter registry used to
	// resolve AcceleratorType to an adapter.
	Accelerators map[string]adapter.Accelerator
	// AcceleratorMetricDescriptors are adapter-owned descriptors accepted by
	// the shared Prometheus collector for this NodeAgent image.
	AcceleratorMetricDescriptors          []adapter.MetricDescriptor
	RuntimeUsageProvider                  runtimeusage.Provider
	KubernetesAcceleratorEvidenceProvider KubernetesAcceleratorEvidenceProvider
	StaticAcceleratorEvidenceProvider     StaticAcceleratorEvidenceProvider
	AllocationTimeout                     time.Duration
	KubernetesWriter                      *metricskubernetes.AnnotationWriter
	AnnotationSyncInterval                time.Duration
	HTTPClient                            *http.Client
}

// WithAccelerators returns a copy of the config carrying the host-owned,
// immutable accelerator lookup table.
func (c Config) WithAccelerators(accelerators map[string]adapter.Accelerator) Config {
	c.Accelerators = make(map[string]adapter.Accelerator, len(accelerators))
	for typ, accel := range accelerators {
		c.Accelerators[typ] = accel
	}

	return c
}

// WithAcceleratorMetricDescriptors returns a copy of the adapter descriptors
// that have already been validated and frozen by the public NodeAgent host.
func (c Config) WithAcceleratorMetricDescriptors(descriptors []adapter.MetricDescriptor) Config {
	c.AcceleratorMetricDescriptors = adapter.CloneMetricDescriptors(descriptors)

	return c
}

type Server struct {
	config     Config
	httpClient *http.Client
	normalizer *metricsnormalizer.Normalizer
}

type KubernetesAcceleratorEvidenceProvider interface {
	// KubernetesAcceleratorEvidence provides raw Kubernetes facts for an
	// accelerator adapter; the server never assigns vendor semantics to them.
	KubernetesAcceleratorEvidence(ctx context.Context) (adapter.KubernetesEvidence, error)
}

type StaticAcceleratorEvidenceProvider interface {
	// StaticAcceleratorEvidence provides raw Ray and process facts for an
	// accelerator adapter; the server never assigns vendor semantics to them.
	StaticAcceleratorEvidence(ctx context.Context) (adapter.StaticEvidence, error)
}

func NewServer(config Config) (*Server, error) {
	if err := validateAdapterMetricDescriptors(config.AcceleratorMetricDescriptors); err != nil {
		return nil, err
	}

	if config.ClusterType == "" {
		config.ClusterType = config.Labels.ClusterType
	}

	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	if config.ListenAddress == "" {
		config.ListenAddress = ":9101"
	}

	config.VirtualizationMetricsTarget = cloneMetricsTargetProfile(config.VirtualizationMetricsTarget)

	return &Server{
		config:     config,
		httpClient: config.HTTPClient,
		normalizer: &metricsnormalizer.Normalizer{},
	}, nil
}

func cloneMetricsTargetProfile(profile *v1.MetricsTargetProfile) *v1.MetricsTargetProfile {
	if profile == nil {
		return nil
	}

	copy := *profile
	if len(profile.PodSelector) > 0 {
		copy.PodSelector = make(map[string]string, len(profile.PodSelector))
		for key, value := range profile.PodSelector {
			copy.PodSelector[key] = value
		}
	}

	return &copy
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/node/device-snapshot", s.handleNodeDeviceSnapshot)

	return mux
}

func (s *Server) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.config.ListenAddress,
		Handler:           s.Handler(),
		ReadHeaderTimeout: defaultHTTPTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}

		errCh <- nil
	}()

	if s.config.KubernetesWriter != nil {
		go s.runKubernetesAnnotationWriter(ctx)
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}

		return <-errCh
	case err := <-errCh:
		return err
	}
}

func (s *Server) runKubernetesAnnotationWriter(ctx context.Context) {
	interval := s.config.AnnotationSyncInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}

	s.writeKubernetesAnnotations(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.writeKubernetesAnnotations(ctx)
		}
	}
}

func (s *Server) writeKubernetesAnnotations(ctx context.Context) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/v1/node/device-snapshot", nil)
	if err != nil {
		return
	}

	snapshot := s.nodeDeviceSnapshot(request)

	if isEmptyCPUDeviceSnapshot(snapshot) {
		return
	}

	_ = s.config.KubernetesWriter.Write(ctx, snapshot)
}

func isEmptyCPUDeviceSnapshot(snapshot *v1.NodeDeviceSnapshot) bool {
	if snapshot == nil {
		return false
	}

	return snapshot.Accelerator.Type == v1.StaticNodeAcceleratorTypeCPU &&
		len(snapshot.Accelerator.Devices) == 0
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	samples := s.normalizer.Samples(s.normalizeRequest(r.Context()))
	registry := prometheus.NewRegistry()
	registry.MustRegister(newMetricsCollector(samples, s.config.AcceleratorMetricDescriptors))

	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

// normalizeRequest is the host-side boundary between collection and metric
// semantics. A selected adapter receives only raw evidence, so provider-specific
// future accelerators can add allocation rules without adding vendor branches
// to the shared normalizer.
func (s *Server) normalizeRequest(ctx context.Context) metricsnormalizer.NormalizeRequest {
	normalizeReq := metricsnormalizer.NormalizeRequest{
		Labels:                       s.config.Labels,
		NodeExporter:                 s.scrapeFirstTarget(ctx, metricsnormalizer.TargetNodeExporter),
		EndpointReplicaRuntimeUsages: s.endpointReplicaRuntimeUsages(ctx),
	}

	accel := s.selectedAccelerator()
	if accel == nil {
		return normalizeReq
	}

	acceleratorExporter := s.scrapeAcceleratorExporters(ctx)

	normalizeReq.AcceleratorExporter = acceleratorExporter
	normalizeReq.AcceleratorSamples = s.acceleratorSamples(
		ctx,
		accel,
		acceleratorExporter,
	)

	return normalizeReq
}

// selectedAccelerator resolves the management-plane-selected adapter without
// making collection depend on a vendor-specific implementation. The selected
// adapter owns the resource-name and device-ID interpretation.
func (s *Server) selectedAccelerator() adapter.Accelerator {
	if s.config.AcceleratorType == "" {
		return nil
	}

	return s.config.Accelerators[s.config.AcceleratorType]
}

// acceleratorSamples drives the generic adapter lifecycle: discover hardware,
// collect topology evidence, build adapter metrics, validate them, then return
// normalizer-owned samples. It deliberately fails closed instead of parsing an
// adapter-owned exporter body in the generic host.
func (s *Server) acceleratorSamples(
	ctx context.Context,
	accel adapter.Accelerator,
	acceleratorExporter *model.ScrapeResult,
) []metricsnormalizer.Sample {
	hardware, err := s.discoverAdapterHardware(ctx, accel)
	if err != nil {
		klog.V(2).InfoS("Accelerator adapter failed to discover hardware", "accelerator_type", s.config.AcceleratorType, "error", err)

		return []metricsnormalizer.Sample{}
	}

	result, err := s.adapterMetricResult(
		ctx,
		accel,
		hardware,
		acceleratorExporter,
	)
	if err != nil {
		klog.V(2).InfoS("Accelerator adapter failed to build metrics", "accelerator_type", s.config.AcceleratorType, "error", err)
		// A configured adapter owns its failure mode. The host emits no vendor
		// metrics until that adapter can produce them.
		return []metricsnormalizer.Sample{}
	}

	if err := validateAdapterSamples(result.Samples, s.config.AcceleratorMetricDescriptors); err != nil {
		klog.V(2).InfoS("Accelerator adapter returned invalid metrics", "accelerator_type", s.config.AcceleratorType, "error", err)

		return []metricsnormalizer.Sample{}
	}

	// An empty, non-nil slice retains the explicit adapter result when no
	// samples were produced.
	if result.Samples == nil {
		return []metricsnormalizer.Sample{}
	}

	return normalizerSamplesFromAdapter(result.Samples)
}

// discoverAdapterHardware applies the host collection deadline to an adapter's
// physical inventory discovery and isolates its returned snapshot per cycle.
// This is the extension point for vendor SDK-backed discovery; the host only
// consumes the canonical snapshot.
func (s *Server) discoverAdapterHardware(
	ctx context.Context,
	accel adapter.Accelerator,
) (adapter.HardwareSnapshot, error) {
	hardwareCtx, cancel := context.WithTimeout(ctx, s.allocationTimeout())
	defer cancel()

	return accel.DiscoverHardware(hardwareCtx)
}

// adapterMetricResult converts common host facts into the public adapter
// evidence model, then selects the Kubernetes or static capability by cluster
// type. It deliberately passes PodResources, HAMi monitor text, Ray actors,
// and processes through unchanged: vendor-specific allocation and metric
// semantics remain wholly inside the adapter.
func (s *Server) adapterMetricResult(
	ctx context.Context,
	accel adapter.Accelerator,
	hardware adapter.HardwareSnapshot,
	acceleratorExporter *model.ScrapeResult,
) (adapter.MetricResult, error) {
	common := adapter.CommonEvidence{
		Labels: s.config.Labels,
	}
	if acceleratorExporter != nil {
		common.ExporterText = acceleratorExporter.Body
		common.ExporterUp = acceleratorExporter.Up
	}

	buildCtx, cancel := context.WithTimeout(ctx, s.allocationTimeout())
	defer cancel()

	switch s.config.ClusterType {
	case v1.KubernetesClusterType:
		kubernetesAccelerator, ok := accel.(adapter.KubernetesAccelerator)
		if !ok {
			return adapter.MetricResult{}, fmt.Errorf("accelerator adapter does not implement Kubernetes capability")
		}

		evidence := s.kubernetesAcceleratorEvidence(buildCtx, common)
		result, err := kubernetesAccelerator.BuildKubernetesMetrics(
			buildCtx,
			hardware.Clone(),
			evidence.Clone(),
		)

		return result.Clone(), err
	case v1.SSHClusterType:
		staticAccelerator, ok := accel.(adapter.StaticAccelerator)
		if !ok {
			return adapter.MetricResult{}, fmt.Errorf("accelerator adapter does not implement static capability")
		}

		evidence := s.staticAcceleratorEvidence(buildCtx, common)
		result, err := staticAccelerator.BuildStaticMetrics(
			buildCtx,
			hardware.Clone(),
			evidence.Clone(),
		)

		return result.Clone(), err
	default:
		return adapter.MetricResult{}, fmt.Errorf("unsupported cluster type %q", s.config.ClusterType)
	}
}

// kubernetesAcceleratorEvidence merges best-effort raw Kubernetes topology
// into the per-request common evidence passed to an adapter. It does not
// decode HAMi annotations or resource names, which lets each adapter use
// their own virtualization conventions over the same evidence shape.
func (s *Server) kubernetesAcceleratorEvidence(
	ctx context.Context,
	common adapter.CommonEvidence,
) adapter.KubernetesEvidence {
	evidence := adapter.KubernetesEvidence{Common: common}
	if s.config.KubernetesAcceleratorEvidenceProvider == nil {
		return evidence
	}

	raw, err := s.config.KubernetesAcceleratorEvidenceProvider.KubernetesAcceleratorEvidence(ctx)
	if err != nil {
		klog.V(2).InfoS("Kubernetes accelerator evidence collection failed", "accelerator_type", s.config.AcceleratorType, "error", err)
		return evidence
	}

	raw.Common = common

	if s.config.VirtualizationMetricsTarget != nil {
		monitorText, monitorUp := s.virtualizationMonitorEvidence(ctx)
		raw.VirtualizationMonitor = adapter.VirtualizationMonitorEvidence{
			Text: monitorText,
			Up:   monitorUp,
		}
	}

	return raw.Clone()
}

func (s *Server) virtualizationMonitorEvidence(ctx context.Context) (string, bool) {
	writer := s.config.KubernetesWriter
	if writer == nil || s.config.VirtualizationMetricsTarget == nil {
		return "", false
	}

	collector := KubernetesVirtualizationMonitorCollector{
		Client:     writer.Client,
		NodeName:   writer.NodeName,
		Target:     s.config.VirtualizationMetricsTarget,
		HTTPClient: s.httpClient,
	}
	text, up, err := collector.Collect(ctx)

	if err != nil {
		klog.V(2).InfoS("Virtualization monitor collection failed", "error", err)
	}

	return text, up
}

// staticAcceleratorEvidence merges best-effort raw Ray/process topology into
// the per-request common evidence passed to an adapter. The adapter decides
// how Ray RequiredResources and local process observations map to its devices.
func (s *Server) staticAcceleratorEvidence(
	ctx context.Context,
	common adapter.CommonEvidence,
) adapter.StaticEvidence {
	evidence := adapter.StaticEvidence{Common: common}
	if s.config.StaticAcceleratorEvidenceProvider == nil {
		return evidence
	}

	raw, err := s.config.StaticAcceleratorEvidenceProvider.StaticAcceleratorEvidence(ctx)
	if err != nil {
		klog.V(2).InfoS("Static accelerator evidence collection failed", "accelerator_type", s.config.AcceleratorType, "error", err)
		return evidence
	}

	raw.Common = common

	return raw.Clone()
}

// normalizerSamplesFromAdapter is the final host-side conversion after the
// adapter has produced validated canonical metric samples. Keeping this small
// conversion outside adapters lets all vendors share the existing Prometheus
// exposition path without importing internal normalizer types.
func normalizerSamplesFromAdapter(samples []adapter.Sample) []metricsnormalizer.Sample {
	result := make([]metricsnormalizer.Sample, 0, len(samples))

	for _, sample := range samples {
		labels := make(map[string]string, len(sample.Labels))
		for key, value := range sample.Labels {
			labels[key] = value
		}

		result = append(result, metricsnormalizer.Sample{
			Name:   sample.Name,
			Labels: labels,
			Value:  sample.Value,
		})
	}

	return result
}

func (s *Server) endpointReplicaRuntimeUsages(ctx context.Context) []model.EndpointReplicaRuntimeUsage {
	if s.config.RuntimeUsageProvider == nil {
		return nil
	}

	usageCtx, cancel := context.WithTimeout(ctx, s.allocationTimeout())
	defer cancel()

	usages, err := s.config.RuntimeUsageProvider.Usages(usageCtx)
	if err != nil {
		return nil
	}

	return usages
}

func (s *Server) handleNodeDeviceSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot := s.nodeDeviceSnapshot(r)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (s *Server) nodeDeviceSnapshot(r *http.Request) *v1.NodeDeviceSnapshot {
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}

	if accel := s.selectedAccelerator(); accel != nil {
		hardware, err := s.discoverAdapterHardware(ctx, accel)
		if err != nil {
			klog.V(2).InfoS("Accelerator adapter failed to discover hardware for device snapshot", "accelerator_type", s.config.AcceleratorType, "error", err)

			return &v1.NodeDeviceSnapshot{Accelerator: v1.CPUStaticNodeAcceleratorStatus()}
		}

		result, err := s.adapterMetricResult(ctx, accel, hardware, s.scrapeAcceleratorExporters(ctx))
		if err != nil {
			klog.V(2).InfoS("Accelerator adapter failed to build device snapshot allocations", "accelerator_type", s.config.AcceleratorType, "error", err)
		}

		return &v1.NodeDeviceSnapshot{
			Accelerator: hardware.Clone().Accelerator,
			Allocations: result.Allocations,
		}
	}

	return &v1.NodeDeviceSnapshot{Accelerator: v1.CPUStaticNodeAcceleratorStatus()}
}

func (s *Server) allocationTimeout() time.Duration {
	if s.config.AllocationTimeout > 0 {
		return s.config.AllocationTimeout
	}

	return defaultHTTPTimeout
}

func (s *Server) scrapeAcceleratorExporters(ctx context.Context) *model.ScrapeResult {
	if s.config.ScrapeTargetProvider == nil {
		return nil
	}

	targets, err := s.scrapeTargets(ctx, metricsnormalizer.TargetAcceleratorExporter)
	if err != nil {
		klog.V(2).InfoS("Failed to discover scrape targets", "target", metricsnormalizer.TargetAcceleratorExporter, "error", err)

		return &model.ScrapeResult{
			Target: metricsnormalizer.TargetAcceleratorExporter,
			Error:  err.Error(),
		}
	}

	if len(targets) == 0 {
		klog.V(2).InfoS("No scrape targets discovered", "target", metricsnormalizer.TargetAcceleratorExporter)

		return &model.ScrapeResult{Target: metricsnormalizer.TargetAcceleratorExporter}
	}

	klog.V(2).InfoS("Discovered scrape targets", "target", metricsnormalizer.TargetAcceleratorExporter, "count", len(targets))

	var body strings.Builder
	errors := make([]string, 0)
	succeeded := 0
	successfulFallbacks := map[string]struct{}{}

	for _, target := range targets {
		fallbackKey := scrapeTargetFallbackKey(target.URL)
		if _, ok := successfulFallbacks[fallbackKey]; ok && isHTTPSURL(target.URL) {
			klog.V(2).InfoS("Skipping HTTPS scrape fallback after successful HTTP scrape", "target", metricsnormalizer.TargetAcceleratorExporter, "url", target.URL)
			continue
		}

		result := s.scrape(ctx, metricsnormalizer.TargetAcceleratorExporter, target.URL)
		if !result.Up {
			klog.V(2).InfoS("Scrape target failed", "target", metricsnormalizer.TargetAcceleratorExporter, "url", target.URL, "error", result.Error)

			if result.Error != "" {
				errors = append(errors, result.Error)
			}

			continue
		}

		klog.V(2).InfoS("Scrape target succeeded", "target", metricsnormalizer.TargetAcceleratorExporter, "url", target.URL, "body_bytes", len(result.Body))

		succeeded++

		successfulFallbacks[fallbackKey] = struct{}{}

		body.WriteString(result.Body)

		if !strings.HasSuffix(result.Body, "\n") {
			body.WriteByte('\n')
		}
	}

	result := &model.ScrapeResult{
		Target: metricsnormalizer.TargetAcceleratorExporter,
		Up:     succeeded > 0,
		Body:   body.String(),
		Error:  strings.Join(errors, "; "),
	}

	klog.V(2).InfoS("Scraped accelerator exporters", "target", metricsnormalizer.TargetAcceleratorExporter, "discovered", len(targets), "succeeded", succeeded)

	return result
}

func (s *Server) scrapeFirstTarget(ctx context.Context, targetType string) model.ScrapeResult {
	targets, err := s.scrapeTargets(ctx, targetType)
	if err != nil {
		klog.V(2).InfoS("Failed to discover scrape targets", "target", targetType, "error", err)

		return model.ScrapeResult{Target: targetType, Error: err.Error()}
	}

	if len(targets) == 0 {
		klog.V(2).InfoS("No scrape targets discovered", "target", targetType)

		return model.ScrapeResult{Target: targetType}
	}

	klog.V(2).InfoS("Discovered scrape targets", "target", targetType, "count", len(targets))

	errors := make([]string, 0)

	for _, target := range targets {
		result := s.scrape(ctx, targetType, target.URL)
		if result.Up {
			klog.V(2).InfoS("Scrape target succeeded", "target", targetType, "url", target.URL, "body_bytes", len(result.Body))
			return result
		}

		klog.V(2).InfoS("Scrape target failed", "target", targetType, "url", target.URL, "error", result.Error)

		if result.Error != "" {
			errors = append(errors, result.Error)
		}
	}

	return model.ScrapeResult{Target: targetType, Error: strings.Join(errors, "; ")}
}

func (s *Server) scrapeTargets(ctx context.Context, targetType string) ([]ScrapeTarget, error) {
	provider := s.config.ScrapeTargetProvider
	if provider == nil {
		return nil, nil
	}

	return provider.Targets(ctx, targetType)
}

func scrapeTargetFallbackKey(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	return parsed.Host + parsed.EscapedPath()
}

func isHTTPSURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "https"
}

func (s *Server) scrape(ctx context.Context, target string, url string) model.ScrapeResult {
	if strings.TrimSpace(url) == "" {
		return model.ScrapeResult{Target: target}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return model.ScrapeResult{Target: target, Error: err.Error()}
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return model.ScrapeResult{Target: target, Error: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return model.ScrapeResult{Target: target, Error: err.Error()}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return model.ScrapeResult{
			Target: target,
			Error:  fmt.Sprintf("unexpected status code %d", resp.StatusCode),
			Body:   string(body),
		}
	}

	return model.ScrapeResult{
		Target: target,
		Up:     true,
		Body:   string(body),
	}
}

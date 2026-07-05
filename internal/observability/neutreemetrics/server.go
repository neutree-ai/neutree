package neutreemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/allocation"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/devicesnapshot"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	metricsnormalizer "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/runtimeusage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

const (
	defaultHTTPTimeout = 5 * time.Second
)

type Config struct {
	ListenAddress        string
	Labels               model.CanonicalLabels
	ScrapeTargetProvider ScrapeTargetProvider
	// TODO: Introduce accelerator exporter adapters here. The first built-in
	// adapter is NVIDIA DCGM-compatible metrics; future external adapters should
	// map vendor exporter output into Neutree's canonical accelerator samples
	// before normalizing to neutree_* metrics and device snapshots.
	DeviceSnapshotProvider   model.DeviceSnapshotProvider
	AllocationProvider       allocation.Provider
	RuntimeUsageProvider     runtimeusage.Provider
	EndpointGPUUsageProvider EndpointGPUUsageProvider
	GPUHardwareProvider      hardware.GPUHardwareInfoProvider
	AllocationTimeout        time.Duration
	KubernetesWriter         *metricskubernetes.AnnotationWriter
	AnnotationSyncInterval   time.Duration
	HTTPClient               *http.Client
}

type Server struct {
	config     Config
	httpClient *http.Client
	normalizer *metricsnormalizer.Normalizer
}

type EndpointGPUUsageProvider interface {
	Usages(ctx context.Context) ([]model.EndpointReplicaGPUUsage, error)
}

func NewServer(config Config) (*Server, error) {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	if config.ListenAddress == "" {
		config.ListenAddress = ":9101"
	}

	return &Server{
		config:     config,
		httpClient: config.HTTPClient,
		normalizer: &metricsnormalizer.Normalizer{},
	}, nil
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

	snapshot, err := s.nodeDeviceSnapshot(request)
	if err != nil {
		return
	}

	_ = s.config.KubernetesWriter.Write(ctx, snapshot)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	samples := s.normalizer.Samples(s.normalizeRequest(r.Context()))
	registry := prometheus.NewRegistry()
	registry.MustRegister(newMetricsCollector(samples))

	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (s *Server) normalizeRequest(ctx context.Context) metricsnormalizer.NormalizeRequest {
	normalizeReq := metricsnormalizer.NormalizeRequest{
		Labels:                       s.config.Labels,
		NodeExporter:                 s.scrapeFirstTarget(ctx, metricsnormalizer.TargetNodeExporter),
		EndpointReplicaRuntimeUsages: s.endpointReplicaRuntimeUsages(ctx),
		EndpointReplicaGPUUsages:     s.endpointReplicaGPUUsages(ctx),
	}

	if acceleratorExporter := s.scrapeAcceleratorExporters(ctx); acceleratorExporter != nil {
		gpuHardwareInfos := s.gpuHardwareInfosFromScrape(ctx, acceleratorExporter)
		normalizeReq.AcceleratorExporter = acceleratorExporter
		normalizeReq.EndpointAllocations = s.endpointAllocationsFromScrape(ctx, acceleratorExporter, gpuHardwareInfos)
		normalizeReq.GPUHardwareInfos = gpuHardwareInfos
	}

	return normalizeReq
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

func (s *Server) endpointReplicaGPUUsages(ctx context.Context) []model.EndpointReplicaGPUUsage {
	if s.config.EndpointGPUUsageProvider == nil {
		return nil
	}

	usageCtx, cancel := context.WithTimeout(ctx, s.allocationTimeout())
	defer cancel()

	usages, err := s.config.EndpointGPUUsageProvider.Usages(usageCtx)
	if err != nil {
		return nil
	}

	return usages
}

func (s *Server) handleNodeDeviceSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.nodeDeviceSnapshot(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (s *Server) nodeDeviceSnapshot(r *http.Request) (*model.NodeDeviceSnapshot, error) {
	if s.config.DeviceSnapshotProvider != nil {
		return s.config.DeviceSnapshotProvider.DeviceSnapshot(r)
	}

	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}

	acceleratorExporter := s.scrapeAcceleratorExporters(ctx)
	if acceleratorExporter == nil || !acceleratorExporter.Up {
		return s.withAllocations(ctx, devicesnapshot.FromAcceleratorMetrics(""))
	}

	snapshot := devicesnapshot.FromAcceleratorMetrics(acceleratorExporter.Body)
	applyGPUHardwareInfoToSnapshot(snapshot, s.gpuHardwareInfosFromScrape(ctx, acceleratorExporter))

	return s.withAllocations(ctx, snapshot)
}

func applyGPUHardwareInfoToSnapshot(snapshot *model.NodeDeviceSnapshot, infos []model.GPUHardwareInfo) {
	if snapshot == nil || len(infos) == 0 {
		return
	}

	infosByUUID := gpuHardwareInfoByUUID(infos)
	for i := range snapshot.Accelerator.Devices {
		info, ok := infosByUUID[snapshot.Accelerator.Devices[i].UUID]
		if !ok {
			continue
		}

		if info.MinorNumber != "" {
			minorNumber, err := strconv.Atoi(info.MinorNumber)
			if err == nil {
				snapshot.Accelerator.Devices[i].MinorNumber = minorNumber
			}
		}
		if snapshot.Accelerator.Devices[i].ProductName == "" {
			snapshot.Accelerator.Devices[i].ProductName = info.Product
		}
		if snapshot.Accelerator.Devices[i].ProductModel == "" {
			snapshot.Accelerator.Devices[i].ProductModel = info.Product
		}
		if snapshot.Accelerator.Devices[i].MemoryMiB == 0 {
			memoryMiB, err := strconv.ParseInt(info.MemoryTotalMiB, 10, 64)
			if err == nil {
				snapshot.Accelerator.Devices[i].MemoryMiB = memoryMiB
			}
		}
	}
}

func gpuHardwareInfoByUUID(infos []model.GPUHardwareInfo) map[string]model.GPUHardwareInfo {
	result := map[string]model.GPUHardwareInfo{}
	for _, info := range infos {
		if info.UUID == "" {
			continue
		}

		result[info.UUID] = info
	}

	return result
}

func (s *Server) withAllocations(ctx context.Context, snapshot *model.NodeDeviceSnapshot) (*model.NodeDeviceSnapshot, error) {
	if s.config.AllocationProvider == nil || snapshot == nil {
		return snapshot, nil
	}

	allocationCtx, cancel := context.WithTimeout(ctx, s.allocationTimeout())
	defer cancel()

	allocations, err := s.config.AllocationProvider.Allocations(allocationCtx, snapshot)
	if err != nil {
		return nil, err
	}

	snapshot.Allocations = allocations

	return snapshot, nil
}

func (s *Server) endpointAllocationsFromScrape(
	ctx context.Context,
	acceleratorExporter *model.ScrapeResult,
	gpuHardwareInfos []model.GPUHardwareInfo,
) []model.EndpointAllocation {
	if s.config.AllocationProvider == nil || acceleratorExporter == nil || !acceleratorExporter.Up {
		return nil
	}

	allocationCtx, cancel := context.WithTimeout(ctx, s.allocationTimeout())
	defer cancel()

	snapshot := devicesnapshot.FromAcceleratorMetrics(acceleratorExporter.Body)
	applyGPUHardwareInfoToSnapshot(snapshot, gpuHardwareInfos)

	snapshot, err := s.withAllocations(allocationCtx, snapshot)
	if err != nil || snapshot == nil {
		return nil
	}

	return allocation.EndpointAllocationsFromStaticNodeAllocations(s.config.Labels, snapshot.Allocations)
}

func (s *Server) gpuHardwareInfosFromScrape(
	ctx context.Context,
	acceleratorExporter *model.ScrapeResult,
) []model.GPUHardwareInfo {
	if acceleratorExporter == nil || !acceleratorExporter.Up {
		return nil
	}

	infos := hardware.FromAcceleratorMetrics(acceleratorExporter.Body)

	provider := s.config.GPUHardwareProvider
	if provider == nil {
		provider = hardware.NvidiaSMIGPUHardwareInfoProvider{}
	}

	hardwareCtx, cancel := context.WithTimeout(ctx, s.allocationTimeout())
	defer cancel()

	providerInfos, err := provider.GPUHardwareInfos(hardwareCtx)
	if err != nil {
		return infos
	}

	return hardware.Merge(infos, providerInfos)
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

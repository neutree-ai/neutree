package neutreemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout = 5 * time.Second
)

type Config struct {
	ListenAddress           string
	Labels                  CanonicalLabels
	NodeExporterURL         string
	AcceleratorExporterURL  string
	AcceleratorExporterURLs []string
	SnapshotToken           string
	SnapshotProvider        SnapshotProvider
	AllocationProvider      AllocationProvider
	KubernetesWriter        *KubernetesAnnotationWriter
	AnnotationSyncInterval  time.Duration
	HTTPClient              *http.Client
}

type Server struct {
	config     Config
	httpClient *http.Client
	normalizer *Normalizer
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
		normalizer: &Normalizer{},
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/node/snapshot", s.handleNodeSnapshot)

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
	snapshot, err := s.nodeSnapshot(nil)
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
	normalizeReq := NormalizeRequest{
		Labels:       s.config.Labels,
		NodeExporter: s.scrape(r.Context(), TargetNodeExporter, s.config.NodeExporterURL),
	}

	if acceleratorExporter := s.scrapeAcceleratorExporters(r.Context()); acceleratorExporter != nil {
		normalizeReq.AcceleratorExporter = acceleratorExporter
		normalizeReq.EndpointAllocations = s.endpointAllocationsFromScrape(r.Context(), acceleratorExporter)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.normalizer.Normalize(normalizeReq)))
}

func (s *Server) handleNodeSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeSnapshotRequest(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	snapshot, err := s.nodeSnapshot(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (s *Server) authorizeSnapshotRequest(r *http.Request) bool {
	if s.config.SnapshotToken == "" {
		return true
	}

	return r.Header.Get("Authorization") == "Bearer "+s.config.SnapshotToken
}

func (s *Server) nodeSnapshot(r *http.Request) (*NodeSnapshot, error) {
	if s.config.SnapshotProvider != nil {
		return s.config.SnapshotProvider.Snapshot(r)
	}

	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}

	acceleratorExporter := s.scrapeAcceleratorExporters(ctx)
	if acceleratorExporter == nil || !acceleratorExporter.Up {
		return s.withAllocations(ctx, snapshotFromAcceleratorMetrics(""))
	}

	return s.withAllocations(ctx, snapshotFromAcceleratorMetrics(acceleratorExporter.Body))
}

func (s *Server) withAllocations(ctx context.Context, snapshot *NodeSnapshot) (*NodeSnapshot, error) {
	if s.config.AllocationProvider == nil || snapshot == nil {
		return snapshot, nil
	}

	allocations, err := s.config.AllocationProvider.Allocations(ctx, snapshot)
	if err != nil {
		return nil, err
	}

	snapshot.Allocations = allocations

	return snapshot, nil
}

func (s *Server) endpointAllocationsFromScrape(
	ctx context.Context,
	acceleratorExporter *ScrapeResult,
) []EndpointAllocation {
	if s.config.AllocationProvider == nil || acceleratorExporter == nil || !acceleratorExporter.Up {
		return nil
	}

	snapshot, err := s.withAllocations(ctx, snapshotFromAcceleratorMetrics(acceleratorExporter.Body))
	if err != nil || snapshot == nil {
		return nil
	}

	return endpointAllocationsFromStaticNodeAllocations(s.config.Labels, snapshot.Allocations)
}

func (s *Server) scrapeAcceleratorExporters(ctx context.Context) *ScrapeResult {
	urls := append([]string{}, s.config.AcceleratorExporterURLs...)
	if s.config.AcceleratorExporterURL != "" {
		urls = append(urls, s.config.AcceleratorExporterURL)
	}

	if len(urls) == 0 {
		return nil
	}

	var body strings.Builder
	errors := make([]string, 0)
	up := true

	for _, url := range urls {
		result := s.scrape(ctx, TargetAcceleratorExporter, url)
		if !result.Up {
			up = false

			if result.Error != "" {
				errors = append(errors, result.Error)
			}

			continue
		}

		body.WriteString(result.Body)

		if !strings.HasSuffix(result.Body, "\n") {
			body.WriteByte('\n')
		}
	}

	return &ScrapeResult{
		Target: TargetAcceleratorExporter,
		Up:     up,
		Body:   body.String(),
		Error:  strings.Join(errors, "; "),
	}
}

func (s *Server) scrape(ctx context.Context, target string, url string) ScrapeResult {
	if strings.TrimSpace(url) == "" {
		return ScrapeResult{Target: target}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ScrapeResult{Target: target, Error: err.Error()}
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ScrapeResult{Target: target, Error: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ScrapeResult{Target: target, Error: err.Error()}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ScrapeResult{
			Target: target,
			Error:  fmt.Sprintf("unexpected status code %d", resp.StatusCode),
			Body:   string(body),
		}
	}

	return ScrapeResult{
		Target: target,
		Up:     true,
		Body:   string(body),
	}
}

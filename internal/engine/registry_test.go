package engine

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestRegistryRegister(t *testing.T) {
	tests := []struct {
		name              string
		setup             func(*registry)
		engine            *v1.Engine
		expectedErr       bool
		expectEngineMatch func(t *testing.T, e *v1.Engine)
	}{
		{
			name: "register new engine successfully",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "engine1",
				},
				Spec: &v1.EngineSpec{},
			},
			expectedErr: false,
			expectEngineMatch: func(t *testing.T, e *v1.Engine) {
				if e.Metadata.Name != "engine1" {
					t.Errorf("expected engine name 'engine1', got '%s'", e.Metadata.Name)
				}
			},
		},
		{
			name: "register engine with missing name",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "",
				},
				Spec: &v1.EngineSpec{},
			},
			expectedErr: true,
		},
		{
			name: "merge existing engine",
			setup: func(r *registry) {
				r.engines["engine2"] = &v1.Engine{
					Metadata: &v1.Metadata{
						Name: "engine2",
					},
					Spec: &v1.EngineSpec{
						Versions: []*v1.EngineVersion{
							{
								Version: "v1",
							},
						},
					},
				}
			},
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "engine2",
				},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v2",
						},
					},
				},
			},
			expectedErr: false,
			expectEngineMatch: func(t *testing.T, e *v1.Engine) {
				if len(e.Spec.Versions) != 2 {
					t.Errorf("expected 2 versions after merge, got %d", len(e.Spec.Versions))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &registry{
				engines: make(map[string]*v1.Engine),
			}

			if tt.setup != nil {
				tt.setup(r)
			}

			err := r.Register(tt.engine)
			if (err != nil) != tt.expectedErr {
				t.Fatalf("expected error: %v, got: %v", tt.expectedErr, err)
			}
			if tt.expectEngineMatch != nil {
				tt.expectEngineMatch(t, r.engines[tt.engine.Metadata.Name])
			}
		})
	}

}
func TestRegistryCleanup(t *testing.T) {
	r := &registry{
		engines: make(map[string]*v1.Engine),
	}

	// Pre-populate registry
	r.engines["engine1"] = &v1.Engine{
		Metadata: &v1.Metadata{Name: "engine1"},
	}
	r.engines["engine2"] = &v1.Engine{
		Metadata: &v1.Metadata{Name: "engine2"},
	}

	err := r.Cleanup()
	if err != nil {
		t.Fatalf("unexpected error during cleanup: %v", err)
	}

	if len(r.engines) != 0 {
		t.Errorf("expected registry to be empty after cleanup, got %d engines", len(r.engines))
	}
}

func TestRegistryListAll(t *testing.T) {
	r := &registry{
		engines: make(map[string]*v1.Engine),
	}

	// Pre-populate registry
	r.engines["engine1"] = &v1.Engine{
		Metadata: &v1.Metadata{Name: "engine1"},
	}
	r.engines["engine2"] = &v1.Engine{
		Metadata: &v1.Metadata{Name: "engine2"},
	}

	engines, err := r.ListAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error during ListAll: %v", err)
	}

	if len(engines) != 2 {
		t.Errorf("expected 2 engines from ListAll, got %d", len(engines))
	}
	engineNames := map[string]bool{}
	for _, e := range engines {
		engineNames[e.Metadata.Name] = true
	}
	if !engineNames["engine1"] || !engineNames["engine2"] {
		t.Errorf("ListAll returned unexpected engine names: %v", engineNames)
	}
}

// TestRegistryRegisterCapabilities makes registration the enforcement point for
// the capability protocol: a declaration no consumer could act on is rejected
// here rather than silently ignored downstream.
func TestRegistryRegisterCapabilities(t *testing.T) {
	tests := []struct {
		name         string
		capabilities *v1.EngineCapabilities
		expectedErr  bool
	}{
		{
			// Engines registered before the protocol existed carry no
			// declaration and must still register.
			name:         "no declaration",
			capabilities: nil,
		},
		{
			name: "valid declaration",
			capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true, Port: 9100, Path: "/metrics"},
				Playground:    &v1.PlaygroundCapability{Enabled: true, Modes: []string{v1.PlaygroundModeChat}},
			},
		},
		{
			name: "unknown playground mode",
			capabilities: &v1.EngineCapabilities{
				Playground: &v1.PlaygroundCapability{Enabled: true, Modes: []string{"vision"}},
			},
			expectedErr: true,
		},
		{
			name: "metrics port out of range",
			capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true, Port: 70000},
			},
			expectedErr: true,
		},
		{
			name: "metrics path without a leading slash",
			capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true, Path: "metrics"},
			},
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &registry{engines: make(map[string]*v1.Engine)}

			err := r.Register(&v1.Engine{
				Metadata: &v1.Metadata{Name: "engine1"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{Version: "v1", Capabilities: tt.capabilities},
					},
				},
			})

			if (err != nil) != tt.expectedErr {
				t.Fatalf("expected error: %v, got: %v", tt.expectedErr, err)
			}

			// A rejected declaration must not be half-applied.
			if tt.expectedErr {
				if _, registered := r.engines["engine1"]; registered {
					t.Errorf("engine must not be registered when its capabilities are invalid")
				}
			}
		})
	}
}

// TestBuiltinEnginesDeclareCapabilities guards the built-ins against silently
// losing their declarations, which would leave them relying on the
// undeclared-means-legacy fallback.
func TestBuiltinEnginesDeclareCapabilities(t *testing.T) {
	engines, err := GetBuiltinEngines()
	if err != nil {
		t.Fatalf("failed to load built-in engines: %v", err)
	}

	if len(engines) == 0 {
		t.Fatal("expected at least one built-in engine")
	}

	for _, e := range engines {
		for _, version := range e.Spec.Versions {
			name := e.Metadata.Name + "/" + version.Version

			if err := version.Capabilities.Validate(); err != nil {
				t.Errorf("%s declares invalid capabilities: %v", name, err)
				continue
			}

			if version.Capabilities == nil {
				t.Errorf("%s declares no capabilities", name)
				continue
			}

			// The built-ins are all scraped on the port their Kubernetes deploy
			// template exposes, and all back the Playground.
			metrics := version.ResolveMetricsExport()
			if !metrics.Enabled || metrics.Port != v1.DefaultMetricsExportPort {
				t.Errorf("%s: unexpected metrics export %+v", name, metrics)
			}

			playground := version.ResolvePlayground()
			if !playground.Enabled || len(playground.Modes) == 0 {
				t.Errorf("%s: unexpected playground %+v", name, playground)
			}
		}
	}
}

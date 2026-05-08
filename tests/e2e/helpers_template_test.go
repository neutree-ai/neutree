package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempTemplate(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "tmpl.yaml")

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write tmpl: %v", err)
	}

	return path
}

func TestRenderTemplate_PlainSubstitution(t *testing.T) {
	path := writeTempTemplate(t, "name: {{ .NAME }}\nworkspace: {{ .WORKSPACE }}\n")

	got, err := renderTemplate(path, map[string]any{
		"NAME":      "alpha",
		"WORKSPACE": "ws-1",
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	want := "name: alpha\nworkspace: ws-1\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestRenderTemplate_MissingKeyErrors(t *testing.T) {
	path := writeTempTemplate(t, "name: {{ .NAME }}\nmissing: {{ .NOPE }}\n")

	_, err := renderTemplate(path, map[string]any{"NAME": "alpha"})
	if err == nil {
		t.Fatalf("expected error for missing key, got nil")
	}

	if !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("error should mention missing key NOPE, got: %v", err)
	}
}

func TestRenderTemplate_RangeWorkerIPs(t *testing.T) {
	path := writeTempTemplate(t, `worker_ips:
{{- range .IPS }}
  - "{{ . }}"
{{- end }}
`)

	got, err := renderTemplate(path, map[string]any{
		"IPS": []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"},
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	want := "worker_ips:\n  - \"10.0.0.1\"\n  - \"10.0.0.2\"\n  - \"10.0.0.3\"\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestRenderTemplate_IfSkipsEmpty(t *testing.T) {
	path := writeTempTemplate(t, `config:
{{- if .ACCEL }}
  accelerator_type: "{{ .ACCEL }}"
{{- end }}
  next: line
`)

	t.Run("empty skipped", func(t *testing.T) {
		got, err := renderTemplate(path, map[string]any{"ACCEL": ""})
		if err != nil {
			t.Fatalf("renderTemplate: %v", err)
		}

		want := "config:\n  next: line\n"
		if got != want {
			t.Fatalf("got=%q want=%q", got, want)
		}
	})

	t.Run("non-empty rendered", func(t *testing.T) {
		got, err := renderTemplate(path, map[string]any{"ACCEL": "nvidia_gpu"})
		if err != nil {
			t.Fatalf("renderTemplate: %v", err)
		}

		want := "config:\n  accelerator_type: \"nvidia_gpu\"\n  next: line\n"
		if got != want {
			t.Fatalf("got=%q want=%q", got, want)
		}
	})
}

func TestMergeData_PriorityCallerOverProfileOverEnv(t *testing.T) {
	caller := map[string]any{"X": "from_caller"}
	profile := map[string]string{"X": "from_profile", "Y": "from_profile_only"}
	env := map[string]string{"X": "from_env", "Z": "from_env_only"}

	got := mergeData(caller, profile, env)

	if got["X"] != "from_caller" {
		t.Errorf("X: want from_caller, got %v", got["X"])
	}

	if got["Y"] != "from_profile_only" {
		t.Errorf("Y: want from_profile_only, got %v", got["Y"])
	}

	if got["Z"] != "from_env_only" {
		t.Errorf("Z: want from_env_only, got %v", got["Z"])
	}
}

func TestMergeData_CallerEmptyStringOverridesProfile(t *testing.T) {
	caller := map[string]any{"X": ""}
	profile := map[string]string{"X": "profile_val"}

	got := mergeData(caller, profile, nil)

	if got["X"] != "" {
		t.Errorf("caller empty string should override profile, got %v", got["X"])
	}
}

func TestMergeData_ProfileEmptyValuesSkipped(t *testing.T) {
	profile := map[string]string{"EMPTY": "", "FILLED": "value"}

	got := mergeData(nil, profile, nil)

	if _, ok := got["EMPTY"]; ok {
		t.Errorf("empty profile value should be skipped, got entry: %v", got["EMPTY"])
	}

	if got["FILLED"] != "value" {
		t.Errorf("FILLED: want value, got %v", got["FILLED"])
	}
}

// --- Rev 1: ModelCache structurization ---

func TestRenderTemplate_ModelCachesByMode(t *testing.T) {
	tmpl := `{{- if .CACHES -}}
model_caches:
{{- range .CACHES }}
  - name: {{ .Name }}
{{- if eq .Mode "host_path" }}
    host_path:
      path: "{{ .HostPath }}"
{{- end }}
{{- if eq .Mode "nfs" }}
    nfs:
      server: "{{ .NFSServer }}"
      path: "{{ .NFSPath }}"
{{- end }}
{{- if eq .Mode "pvc" }}
    pvc:
      storageClassName: "{{ .PVCStorageClass }}"
      resources:
        requests:
          storage: {{ .PVCStorage }}
{{- end }}
{{- end }}
{{- end }}
`
	path := writeTempTemplate(t, tmpl)

	cases := []struct {
		name   string
		caches []ModelCache
		want   string
	}{
		{
			name: "host_path",
			caches: []ModelCache{
				{Name: "hp-cache", Mode: "host_path", HostPath: "/data/models"},
			},
			want: "model_caches:\n  - name: hp-cache\n    host_path:\n      path: \"/data/models\"\n",
		},
		{
			name: "nfs",
			caches: []ModelCache{
				{Name: "nfs-cache", Mode: "nfs", NFSServer: "10.0.0.1", NFSPath: "/exports/m"},
			},
			want: "model_caches:\n  - name: nfs-cache\n    nfs:\n      server: \"10.0.0.1\"\n      path: \"/exports/m\"\n",
		},
		{
			name: "pvc",
			caches: []ModelCache{
				{Name: "pvc-cache", Mode: "pvc", PVCStorageClass: "fast-ssd", PVCStorage: "10Gi"},
			},
			want: "model_caches:\n  - name: pvc-cache\n    pvc:\n      storageClassName: \"fast-ssd\"\n      resources:\n        requests:\n          storage: 10Gi\n",
		},
		{
			name: "multiple mixed",
			caches: []ModelCache{
				{Name: "a", Mode: "host_path", HostPath: "/x"},
				{Name: "b", Mode: "nfs", NFSServer: "n", NFSPath: "/p"},
			},
			want: "model_caches:\n  - name: a\n    host_path:\n      path: \"/x\"\n  - name: b\n    nfs:\n      server: \"n\"\n      path: \"/p\"\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderTemplate(path, map[string]any{"CACHES": tc.caches})
			if err != nil {
				t.Fatalf("renderTemplate: %v", err)
			}

			if got != tc.want {
				t.Fatalf("got=%q\nwant=%q", got, tc.want)
			}
		})
	}
}

func TestRenderTemplate_ModelCachesEmptyOmitsBlock(t *testing.T) {
	tmpl := `before
{{- if .CACHES }}
model_caches: yes
{{- end }}
after
`
	path := writeTempTemplate(t, tmpl)

	got, err := renderTemplate(path, map[string]any{"CACHES": []ModelCache{}})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	want := "before\nafter\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

// --- Rev 2: EngineArgs and Role permissions ---

func TestRenderTemplate_EngineArgsRange(t *testing.T) {
	tmpl := `engine_args:
{{- range .ARGS }}
  {{ .Key }}: {{ .Value }}
{{- end }}
`
	path := writeTempTemplate(t, tmpl)

	got, err := renderTemplate(path, map[string]any{
		"ARGS": []EngineArg{
			{Key: "dtype", Value: "half"},
			{Key: "max_model_len", Value: "4096"},
		},
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	want := "engine_args:\n  dtype: half\n  max_model_len: 4096\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestRenderTemplate_RolePermissionsRange(t *testing.T) {
	tmpl := `permissions:
{{- range .PERMS }}
  - "{{ . }}"
{{- end }}
`
	path := writeTempTemplate(t, tmpl)

	got, err := renderTemplate(path, map[string]any{
		"PERMS": []string{"cluster:read", "endpoint:write"},
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	want := "permissions:\n  - \"cluster:read\"\n  - \"endpoint:write\"\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

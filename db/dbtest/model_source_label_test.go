package dbtest

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// TestModelSource covers the NEU-782 model source.
//
// It is stored PER MODEL, in spec.model_sources keyed by the client-facing model
// name, and only on external endpoints. One external endpoint routinely fronts
// models of different origin (one upstream pointing at an internal endpoint,
// another at a public API), and a model may even have targets on several
// upstreams, so neither the endpoint nor the upstream resolves to one source
// per model.
//
// An internal endpoint's source is derived as 'self-hosted'; 'self-hosted' is
// rejected on an external endpoint so it stays one-to-one with IE, which is what
// keeps the IE row and the EE row for the same model name distinguishable in the
// allowed_models picker once the UI drops the internal/external badge.
func TestModelSource(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const (
		ws          = "model-source-ws"
		ieName      = "ms-internal-ep"
		ieModel     = "ms-shared-model"
		eeMixed     = "ms-external-mixed"
		eeUnset     = "ms-external-unset"
		eeCustom    = "ms-external-custom"
		eeViaRef    = "ms-external-via-ref"
		eeViaRefSet = "ms-external-via-ref-set"

		refModel    = "ms-ref-model"
		refSetModel = "ms-ref-set-model"

		groupModel   = "ms-group-model"
		vendorModel  = "ms-vendor-model"
		groupSource  = "internal-shared"
		vendorSource = "third-party-public"
		customSource = "acme-research-lab"
	)

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.external_endpoints WHERE (metadata).workspace = $1", ws)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.endpoints WHERE (metadata).workspace = $1", ws)
	})

	if _, err := db.ExecContext(ctx, `
		INSERT INTO api.endpoints (api_version, kind, spec, metadata)
		VALUES (
			'v1',
			'Endpoint',
			ROW(
				'ms-cluster',
				ROW('ms-registry', $3::text, '', 'v1', '', NULL)::api.model_spec,
				ROW('vllm', 'v0.11.2')::api.endpoint_engine_spec,
				ROW('4', '2', NULL, '16')::api.resource_spec,
				ROW(1)::api.replica_spec,
				NULL, NULL, NULL
			)::api.endpoint_spec,
			ROW($1::text, NULL, $2::text, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata
		)`, ieName, ws, ieModel); err != nil {
		t.Fatalf("insert internal endpoint: %v", err)
	}

	// insertEE registers an external endpoint exposing the given client-facing
	// models through one upstream, with the given spec.model_sources JSON.
	// endpointRef non-empty makes the upstream point at an internal endpoint
	// instead of a URL, which is what makes its models internal by construction.
	insertEEVia := func(name string, models []string, modelSources, endpointRef string) error {
		mapping := make([]string, 0, len(models))
		for _, m := range models {
			mapping = append(mapping, "'"+m+"', 'upstream-model'")
		}

		upstream := "ROW('https://upstream.example.com')::api.external_endpoint_upstream_spec, " +
			"ROW('bearer', 'cred')::api.external_endpoint_auth_spec"
		ref := "NULL"

		if endpointRef != "" {
			upstream = "NULL, NULL"
			ref = "'" + endpointRef + "'"
		}

		_, err := db.ExecContext(ctx, `
			INSERT INTO api.external_endpoints (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ExternalEndpoint',
				ROW(
					ARRAY[
						ROW(
							`+upstream+`,
							jsonb_build_object(`+strings.Join(mapping, ", ")+`),
							`+ref+`,
							'up-1'
						)::api.external_endpoint_upstream_entry
					],
					30,
					NULL,
					$3::jsonb
				)::api.external_endpoint_spec,
				ROW($1::text, NULL, $2::text, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata
			)`, name, ws, modelSources)

		return err
	}

	insertEE := func(name string, models []string, modelSources string) error {
		return insertEEVia(name, models, modelSources, "")
	}

	// The headline case: ONE endpoint, TWO models, TWO different sources.
	if err := insertEE(eeMixed, []string{groupModel, vendorModel},
		`{"`+groupModel+`":"`+groupSource+`","`+vendorModel+`":"`+vendorSource+`"}`); err != nil {
		t.Fatalf("insert mixed-source external endpoint: %v", err)
	}

	if err := insertEE(eeUnset, []string{ieModel}, `{}`); err != nil {
		t.Fatalf("insert external endpoint without sources: %v", err)
	}

	// An upstream pointing at an internal endpoint: its models are internal by
	// construction, so the source is derived without the admin saying anything.
	if err := insertEEVia(eeViaRef, []string{refModel}, `{}`, ieName); err != nil {
		t.Fatalf("insert endpoint_ref-backed external endpoint: %v", err)
	}

	// ...and an explicit source still wins over that derivation.
	if err := insertEEVia(eeViaRefSet, []string{refSetModel},
		`{"`+refSetModel+`":"`+vendorSource+`"}`, ieName); err != nil {
		t.Fatalf("insert endpoint_ref-backed external endpoint with a source: %v", err)
	}

	// Extensibility: an unknown value must be accepted, since the enum is open
	// and a new source must not need a migration or a code change.
	if err := insertEE(eeCustom, []string{"ms-custom-model"},
		`{"ms-custom-model":"`+customSource+`"}`); err != nil {
		t.Fatalf("insert external endpoint with an unknown source: %v", err)
	}

	t.Run("self-hosted is rejected on an external endpoint", func(t *testing.T) {
		err := insertEE("ms-external-selfhosted", []string{"m"}, `{"m":"self-hosted"}`)
		if err == nil {
			t.Fatal("expected self-hosted to be rejected on an external endpoint")
		}

		if !strings.Contains(err.Error(), "self-hosted") {
			t.Fatalf("expected the error to name self-hosted, got %v", err)
		}
	})

	t.Run("self-hosted is rejected even when other models are fine", func(t *testing.T) {
		// Keying by model must not let a bad entry through just because it sits
		// beside good ones.
		err := insertEE("ms-external-partial", []string{"a", "b"},
			`{"a":"third-party-public","b":"self-hosted"}`)
		if err == nil {
			t.Fatal("expected a self-hosted entry to be rejected among valid ones")
		}
	})

	t.Run("self-hosted is rejected on update too", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `
			UPDATE api.external_endpoints
			SET spec.model_sources = $2::jsonb
			WHERE (metadata).workspace = $1 AND (metadata).name = $3`,
			ws, `{"`+groupModel+`":"self-hosted"}`, eeMixed)
		if err == nil {
			t.Fatal("expected self-hosted to be rejected on update")
		}
	})

	t.Run("per-model sources round-trip through the API (list + detail)", func(t *testing.T) {
		// spec.model_sources is a new composite attribute, so this asserts that
		// PostgREST and the Go client actually carry it end to end.
		s := NewTestStorage(t)

		ees, err := s.ListExternalEndpoint(storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>workspace", Operator: "eq", Value: ws},
				{Column: "metadata->>name", Operator: "eq", Value: eeMixed},
			},
		})
		if err != nil {
			t.Fatalf("list external endpoints: %v", err)
		}

		if len(ees) != 1 {
			t.Fatalf("expected exactly one external endpoint, got %d", len(ees))
		}

		for _, tc := range []struct{ model, want string }{
			{groupModel, groupSource},
			{vendorModel, vendorSource},
		} {
			if got := v1.ModelSourceOfExternalEndpoint(&ees[0], tc.model); got != tc.want {
				t.Fatalf("list: model %s expected %q, got %q", tc.model, tc.want, got)
			}
		}

		detail, err := s.GetExternalEndpoint(strconv.Itoa(ees[0].ID))
		if err != nil {
			t.Fatalf("get external endpoint: %v", err)
		}

		if got := v1.ModelSourceOfExternalEndpoint(detail, vendorModel); got != vendorSource {
			t.Fatalf("detail: expected %q, got %q", vendorSource, got)
		}
	})

	t.Run("get_workspace_models resolves the source per model", func(t *testing.T) {
		rows, err := db.QueryContext(ctx, `
			SELECT model, source, endpoint_name, source_label
			FROM api.get_workspace_models($1)`, ws)
		if err != nil {
			t.Fatalf("get_workspace_models: %v", err)
		}
		defer rows.Close()

		type row struct {
			source string
			label  sql.NullString
		}

		// Keyed by endpoint+model, because one endpoint now yields several rows
		// that must not collapse into each other.
		got := map[string]row{}

		for rows.Next() {
			var model, source, endpoint string

			var label sql.NullString
			if err := rows.Scan(&model, &source, &endpoint, &label); err != nil {
				t.Fatalf("scan: %v", err)
			}

			got[endpoint+"|"+model] = row{source: source, label: label}
		}

		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}

		for _, tc := range []struct {
			key        string
			wantSource string
			wantLabel  sql.NullString
		}{
			// Derived, nothing stored.
			{ieName + "|" + ieModel, "endpoint", sql.NullString{String: "self-hosted", Valid: true}},
			// The headline case: two models of ONE endpoint resolving differently.
			{eeMixed + "|" + groupModel, "external_endpoint", sql.NullString{String: groupSource, Valid: true}},
			{eeMixed + "|" + vendorModel, "external_endpoint", sql.NullString{String: vendorSource, Valid: true}},
			// Unset reads as NULL, which the UI shows as its own group.
			{eeUnset + "|" + ieModel, "external_endpoint", sql.NullString{}},
			// An unknown value comes back verbatim.
			{eeCustom + "|ms-custom-model", "external_endpoint", sql.NullString{String: customSource, Valid: true}},
			// Fronting an internal endpoint derives internal-shared with nothing
			// stored -- and NOT self-hosted, which has to stay IE-only.
			{eeViaRef + "|" + refModel, "external_endpoint", sql.NullString{String: "internal-shared", Valid: true}},
			// An explicit source still wins over that derivation.
			{eeViaRefSet + "|" + refSetModel, "external_endpoint", sql.NullString{String: vendorSource, Valid: true}},
		} {
			r, ok := got[tc.key]
			if !ok {
				t.Fatalf("row %s missing, got %v", tc.key, got)
			}

			if r.source != tc.wantSource {
				t.Fatalf("row %s: expected source %q, got %q", tc.key, tc.wantSource, r.source)
			}

			if r.label != tc.wantLabel {
				t.Fatalf("row %s: expected label %v, got %v", tc.key, tc.wantLabel, r.label)
			}
		}
	})
}

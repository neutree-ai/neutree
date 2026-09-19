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

// TestModelSourceLabel covers the NEU-782 model source label.
//
// The label rides on the generic metadata.labels map under the fixed key
// neutree.ai/model-source and is stored ONLY on external endpoints. An internal
// endpoint's source is derived as 'self-hosted'; 'self-hosted' is rejected on an
// external endpoint so that it stays one-to-one with IE, which is what keeps the
// IE row and the EE row for the same model name distinguishable in the
// allowed_models picker once the UI drops the internal/external badge.
func TestModelSourceLabel(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const (
		ws            = "model-source-ws"
		sourceLabel   = "neutree.ai/model-source"
		ieName        = "ms-internal-ep"
		ieModel       = "ms-shared-model"
		eeLabeled     = "ms-external-labeled"
		eeUnlabeled   = "ms-external-unlabeled"
		eeCustom      = "ms-external-custom"
		customSource  = "some-future-source"
		labeledSource = "third-party-public"
	)

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.endpoints WHERE (metadata).workspace = $1", ws)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.external_endpoints WHERE (metadata).workspace = $1", ws)
	})

	// An internal endpoint serving ieModel. Nothing about the source is stored.
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

	// insertEE registers an external endpoint exposing ieModel, with the given
	// metadata.labels JSON.
	insertEE := func(name, labels string) error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO api.external_endpoints (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ExternalEndpoint',
				ROW(
					ARRAY[
						ROW(
							ROW('https://upstream.example.com')::api.external_endpoint_upstream_spec,
							ROW('bearer', 'cred')::api.external_endpoint_auth_spec,
							jsonb_build_object($4::text, 'upstream-model'),
							NULL,
							'up-1'
						)::api.external_endpoint_upstream_entry
					],
					30,
					NULL
				)::api.external_endpoint_spec,
				ROW($1::text, NULL, $2::text, NULL, now(), now(), $3::json, '{}'::json)::api.metadata
			)`, name, ws, labels, ieModel)

		return err
	}

	if err := insertEE(eeLabeled, `{"`+sourceLabel+`":"`+labeledSource+`"}`); err != nil {
		t.Fatalf("insert labeled external endpoint: %v", err)
	}

	if err := insertEE(eeUnlabeled, `{}`); err != nil {
		t.Fatalf("insert unlabeled external endpoint: %v", err)
	}

	// Extensibility: an unknown value must be accepted, since the enum is open
	// and a new source must not need a migration or a code change.
	if err := insertEE(eeCustom, `{"`+sourceLabel+`":"`+customSource+`"}`); err != nil {
		t.Fatalf("insert external endpoint with an unknown source: %v", err)
	}

	t.Run("self-hosted is rejected on an external endpoint", func(t *testing.T) {
		err := insertEE("ms-external-selfhosted", `{"`+sourceLabel+`":"self-hosted"}`)
		if err == nil {
			t.Fatal("expected self-hosted to be rejected on an external endpoint")
		}

		if !strings.Contains(err.Error(), "self-hosted") {
			t.Fatalf("unexpected rejection error: %v", err)
		}
	})

	t.Run("self-hosted is rejected on update too", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `
			UPDATE api.external_endpoints
			SET metadata = ROW(
				(metadata).name, (metadata).display_name, (metadata).workspace,
				(metadata).deletion_timestamp, (metadata).creation_timestamp, (metadata).update_timestamp,
				$2::json, (metadata).annotations
			)::api.metadata
			WHERE (metadata).workspace = $1 AND (metadata).name = $3`,
			ws, `{"`+sourceLabel+`":"self-hosted"}`, eeLabeled)
		if err == nil {
			t.Fatal("expected self-hosted to be rejected when patched onto an external endpoint")
		}

		if !strings.Contains(err.Error(), "self-hosted") {
			t.Fatalf("unexpected rejection error: %v", err)
		}
	})

	t.Run("the source label round-trips on the external endpoint resource", func(t *testing.T) {
		var got sql.NullString
		if err := db.QueryRowContext(ctx, `
			SELECT (metadata).labels::jsonb ->> $3
			FROM api.external_endpoints
			WHERE (metadata).workspace = $1 AND (metadata).name = $2`,
			ws, eeLabeled, sourceLabel).Scan(&got); err != nil {
			t.Fatalf("read source label off the external endpoint: %v", err)
		}

		if !got.Valid || got.String != labeledSource {
			t.Fatalf("expected %q on the resource itself, got %v", labeledSource, got)
		}
	})

	// The label has to be readable on the ExternalEndpoint resource itself, not
	// only through get_workspace_models -- the list and detail pages render it.
	// It rides along in metadata.labels, so this asserts that PostgREST and the
	// Go client actually carry it end to end rather than assuming they do.
	t.Run("the source label is readable through the API (list + detail)", func(t *testing.T) {
		s := NewTestStorage(t)

		ees, err := s.ListExternalEndpoint(storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>workspace", Operator: "eq", Value: ws},
				{Column: "metadata->>name", Operator: "eq", Value: eeLabeled},
			},
		})
		if err != nil {
			t.Fatalf("list external endpoints: %v", err)
		}

		if len(ees) != 1 {
			t.Fatalf("expected exactly one external endpoint, got %d", len(ees))
		}

		if got := v1.ModelSourceOfExternalEndpoint(&ees[0]); got != labeledSource {
			t.Fatalf("list: expected source label %q, got %q", labeledSource, got)
		}

		detail, err := s.GetExternalEndpoint(strconv.Itoa(ees[0].ID))
		if err != nil {
			t.Fatalf("get external endpoint: %v", err)
		}

		if got := v1.ModelSourceOfExternalEndpoint(detail); got != labeledSource {
			t.Fatalf("detail: expected source label %q, got %q", labeledSource, got)
		}
	})

	t.Run("get_workspace_models resolves the source label", func(t *testing.T) {
		rows, err := db.QueryContext(ctx, `
			SELECT model, source, endpoint_name, source_label
			FROM api.get_workspace_models($1)
			ORDER BY source, endpoint_name`, ws)
		if err != nil {
			t.Fatalf("get_workspace_models: %v", err)
		}
		defer rows.Close()

		type row struct {
			model    string
			source   string
			label    sql.NullString
			endpoint string
		}

		got := map[string]row{}

		for rows.Next() {
			var r row
			if err := rows.Scan(&r.model, &r.source, &r.endpoint, &r.label); err != nil {
				t.Fatalf("scan: %v", err)
			}

			got[r.endpoint] = r
		}

		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}

		// The internal endpoint row is derived, not stored.
		ie, ok := got[ieName]
		if !ok {
			t.Fatalf("internal endpoint row missing, got %v", got)
		}

		if ie.source != "endpoint" || !ie.label.Valid || ie.label.String != "self-hosted" {
			t.Fatalf("expected the internal endpoint to resolve to self-hosted, got source=%s label=%v", ie.source, ie.label)
		}

		// The labeled external endpoint returns what was configured.
		ee, ok := got[eeLabeled]
		if !ok {
			t.Fatalf("labeled external endpoint row missing, got %v", got)
		}

		if ee.source != "external_endpoint" || !ee.label.Valid || ee.label.String != labeledSource {
			t.Fatalf("expected %q on the labeled external endpoint, got source=%s label=%v", labeledSource, ee.source, ee.label)
		}

		// An external endpoint with no label yields NULL, not a derived value.
		unlabeled, ok := got[eeUnlabeled]
		if !ok {
			t.Fatalf("unlabeled external endpoint row missing, got %v", got)
		}

		if unlabeled.label.Valid {
			t.Fatalf("expected NULL source_label on an unlabeled external endpoint, got %v", unlabeled.label)
		}

		// An unknown value survives unchanged -- the enum is open.
		custom, ok := got[eeCustom]
		if !ok {
			t.Fatalf("custom-source external endpoint row missing, got %v", got)
		}

		if !custom.label.Valid || custom.label.String != customSource {
			t.Fatalf("expected the unknown source %q to be returned verbatim, got %v", customSource, custom.label)
		}

		// The IE row and the EE row for the SAME model name are still two
		// distinguishable rows -- that is the property NEU-783's per-model quota
		// depends on once the UI drops the internal/external badge.
		if ie.model != custom.model || ie.model != ieModel {
			t.Fatalf("expected both rows to be for %q, got ie=%q ee=%q", ieModel, ie.model, custom.model)
		}

		if ie.label.String == custom.label.String {
			t.Fatalf("IE and EE rows for the same model must not share a source label")
		}
	})
}

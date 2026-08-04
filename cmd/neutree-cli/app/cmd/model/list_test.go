package model

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/client"
)

// Column names are the upper-cased JSON names of the fields they show, which is
// the only thing that makes a column checkable against the API payload.
func TestListHeaderNamesAPIFields(t *testing.T) {
	require.Equal(t, []string{"NAME", "VERSIONS", "ALIAS", "SIZE", "CREATION_TIME"}, listHeader())
}

// The header and the rows are generated from one column list, so they cannot
// drift apart the way two hand-synchronised literals could. This asserts the
// property rather than the current column count: whatever listColumns holds,
// every row is as wide as the header.
func TestListRowsAreAsWideAsTheHeader(t *testing.T) {
	models := []v1.GeneralModel{
		{Name: "a", Versions: []v1.ModelVersion{{Name: "v1", Size: "64 B", CreationTime: "t", Alias: "pet"}}},
		{Name: "b"},
	}

	for _, model := range models {
		require.Len(t, listRow(model), len(listHeader()), "model %q", model.Name)
	}
}

func TestListRowRendersAliasAndPlaceholders(t *testing.T) {
	aliased := v1.GeneralModel{Name: "aliased", Versions: []v1.ModelVersion{
		{Name: "v1", Alias: "pet", Size: "64 B", CreationTime: "2026-06-25T00:00:00Z"},
	}}
	require.Equal(t,
		[]string{"aliased", "v1", "pet", "64 B", "2026-06-25T00:00:00Z"},
		listRow(aliased))

	// Nobody named this version and the registry reported no size: two different
	// absences, rendered differently on purpose.
	bare := v1.GeneralModel{Name: "bare", Versions: []v1.ModelVersion{
		{Name: "v1", CreationTime: "2026-06-25T00:00:00Z"},
		{Name: "v2"},
	}}
	require.Equal(t,
		[]string{"bare", "v1 (+1 more)", unsetValue, unknownValue, "2026-06-25T00:00:00Z"},
		listRow(bare))

	require.Equal(t,
		[]string{"empty", unsetValue, unsetValue, unknownValue, unknownValue},
		listRow(v1.GeneralModel{Name: "empty"}))
}

func TestRenderModelTableWritesOneLinePerModel(t *testing.T) {
	var out bytes.Buffer

	models := []v1.GeneralModel{
		{Name: "a", Versions: []v1.ModelVersion{{Name: "v1"}}},
		{Name: "b", Versions: []v1.ModelVersion{{Name: "v1"}}},
	}

	require.NoError(t, renderModelTable(&out, models))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, len(models)+1)
	require.True(t, strings.HasPrefix(lines[0], "NAME"))
	require.True(t, strings.HasPrefix(lines[1], "a"))
	require.True(t, strings.HasPrefix(lines[2], "b"))
}

func TestSummarisePageReportsTheServersTotal(t *testing.T) {
	models := []v1.GeneralModel{{Name: "a"}, {Name: "b"}}
	total := 42

	require.Equal(t, "Showing models 21-22 (total: 42)",
		summarisePage(&client.ModelList{Models: models, Offset: 20, Total: &total}))

	// A registry that cannot count what matched leaves the total unknown; the
	// summary says so rather than reporting the page size as the total.
	require.Equal(t, "Showing models 1-2 (total: unknown)",
		summarisePage(&client.ModelList{Models: models}))

	require.Equal(t, "No models shown (total: 0)",
		summarisePage(&client.ModelList{Total: new(int)}))
}

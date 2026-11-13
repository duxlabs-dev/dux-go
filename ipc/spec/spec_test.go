package spec_test

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/duxlabs-dev/dux-go/ipc/spec"
)

var methods = spec.Methods{
	{Name: "ui.open", Group: "ui", Params: []spec.Param{spec.Req("files", "files"), spec.OptList("paths", "more"), spec.Opt("line", "")}},
	{Name: "describe", Group: "core"},
}

func TestNamesAndFind(t *testing.T) {
	assert.Equal(t, []string{"describe", "ui.open"}, methods.Names())
	got, ok := methods.Find("ui.open")
	require.True(t, ok)
	assert.Equal(t, "ui", got.Group)
	_, ok = methods.Find("nope")
	assert.False(t, ok)
}

func TestCompleteOffersParamsNotGivenYet(t *testing.T) {
	comps, _ := methods.Complete(nil, nil, "")
	assert.Equal(t, []string{"describe", "ui.open"}, comps)

	comps, directive := methods.Complete(nil, []string{"ui.open"}, "")
	assert.Equal(t, []string{"files=\trequired files", "paths=\tmore", "line=\t"}, comps)
	assert.Equal(t, cobra.ShellCompDirectiveNoSpace|cobra.ShellCompDirectiveNoFileComp, directive)

	comps, _ = methods.Complete(nil, []string{"ui.open", "files=/a", "paths=/b"}, "")
	assert.Equal(t, []string{"paths=\tmore", "line=\t"}, comps, "a list param stays on offer")

	comps, _ = methods.Complete(nil, []string{"nope"}, "")
	assert.Empty(t, comps)
}

func TestParseParams(t *testing.T) {
	params, err := spec.Method{}.ParseParams([]string{"path=/a", "paths=/x", "paths=/y", `select=["/z"]`, "name=[draft", "extra=/one", `extra=["/two"]`, "n=3"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"path":   "/a",
		"paths":  []any{"/x", "/y"},
		"select": []any{"/z"},
		"name":   "[draft",
		"extra":  []any{"/one", "/two"},
		"n":      "3",
	}, params)

	for _, bad := range []string{"novalue", "=x"} {
		_, err = spec.Method{}.ParseParams([]string{bad})
		assert.Error(t, err, bad)
	}
}

func TestParseJSONParamsSendsValuesTypedButListItemsAsStrings(t *testing.T) {
	open, _ := methods.Find("ui.open")
	params, err := open.ParseJSONParams([]string{"paths=2024", "line=1", "line=2", "simple=true", `opts={"a":1}`, "files=/etc/hostname"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"paths":  []any{"2024"},
		"line":   []any{float64(1), float64(2)},
		"simple": true,
		"opts":   map[string]any{"a": float64(1)},
		"files":  "/etc/hostname",
	}, params)

	params, err = open.ParseJSONParams([]string{`paths= ["/a","/b"]`, `line= [1]`, `line= [2]`})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"paths": []any{"/a", "/b"}, "line": []any{float64(1), float64(2)}}, params,
		"JSON whitespace before an array does not turn it into one string or a nested list")

	params, err = open.ParseParams([]string{"paths=/one"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"paths": []any{"/one"}}, params, "a single value of a list param is wrapped")
}

func TestParamWireShape(t *testing.T) {
	raw, err := json.Marshal(spec.Method{Name: "m", Group: "g", Params: []spec.Param{spec.Req("a", ""), spec.OptList("b", "d")}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"m","group":"g","params":[{"name":"a","required":true},{"name":"b","required":false,"list":true,"desc":"d"}]}`, string(raw))
}

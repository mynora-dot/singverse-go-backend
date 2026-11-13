package app

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The migration baseline is the original inventory. The current OpenAPI
// contract retires removed features; every retained operation needs a handler.
func TestPublicRouteContractCoverage(t *testing.T) {
	a := application(t)
	b, e := os.ReadFile("../../contracts/openapi/singverse.openapi.json")
	if e != nil {
		t.Fatal(e)
	}
	var contract struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if e = json.Unmarshal(b, &contract); e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile("../../contracts/go-migration-baseline.json")
	if e != nil {
		t.Fatal(e)
	}
	var baseline struct {
		PublicOperations []struct{ Method, Path string } `json:"publicOperations"`
	}
	if e = json.Unmarshal(b, &baseline); e != nil {
		t.Fatal(e)
	}
	if len(baseline.PublicOperations) < 250 || len(contract.Paths) < 200 {
		t.Fatal("contract unexpectedly empty or truncated")
	}
	normalize := func(p string) string {
		p = regexp.MustCompile(`\{[^}]+\}|:[^/]+|\*[^/]+`).ReplaceAllString(p, "{}")
		return strings.TrimSuffix(p, "/")
	}
	actual := map[string]bool{}
	for _, r := range a.Router.Routes() {
		actual[r.Method+" "+normalize(r.Path)] = true
	}
	missing := []string{}
	retired := 0
	for _, op := range baseline.PublicOperations {
		if _, present := contract.Paths[op.Path][strings.ToLower(op.Method)]; !present {
			retired++
			continue
		}
		if !actual[op.Method+" "+normalize(op.Path)] {
			missing = append(missing, op.Method+" "+op.Path)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d public operations missing from actual Gin route table:\n%s", len(missing), strings.Join(missing, "\n"))
	}
	t.Logf("matched %d retained baseline operations against %d actual registrations; %d retired in OpenAPI", len(baseline.PublicOperations)-retired, len(actual), retired)
}

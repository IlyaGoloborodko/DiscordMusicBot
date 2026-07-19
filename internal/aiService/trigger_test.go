package aiService

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestTriggerSerialisation(t *testing.T) {
	req := AgentRequest{
		Session: AgentSession{GuildID: "g", ChannelID: "c"},
		Message: "[autoplay] ...",
		Trigger: TriggerAutoplay,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"trigger":"autoplay"`) {
		t.Errorf("trigger missing from the request body: %s", body)
	}
}

// The field is optional by agreement: with no trigger the service falls back to
// its own heuristic, so it must be absent rather than sent as "".
func TestEmptyTriggerIsOmitted(t *testing.T) {
	body, err := json.Marshal(AgentRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "trigger") {
		t.Errorf(`an unset trigger was serialised: %s`, body)
	}
}

// TestEveryAgentRequestSetsATrigger scans the callers for AgentRequest literals
// and insists each one names a trigger.
//
// The point of the field is to tell a machine-initiated turn from a human one so
// autoplay cannot re-serve the last hour of music. That protection is invisible
// when it lapses — a new call site that forgets the field just silently looks
// like a user request — so the check is mechanical rather than a matter of
// remembering.
func TestEveryAgentRequestSetsATrigger(t *testing.T) {
	files, err := filepath.Glob("../*/*.go")
	if err != nil {
		t.Fatalf("globbing callers: %v", err)
	}

	found := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "AgentRequest" {
				return true
			}
			found++

			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Trigger" {
					return true
				}
			}
			t.Errorf("%s:%d: AgentRequest without a Trigger — the service would read this "+
				"machine turn as a user request and let it repeat music that just played",
				path, fset.Position(lit.Pos()).Line)
			return true
		})
	}

	// Guard against the scan quietly matching nothing (moved packages, changed
	// layout) and reporting success for that reason.
	if found == 0 {
		t.Fatal("no AgentRequest literals found in sibling packages; the scan is not looking where the callers are")
	}
	t.Logf("checked %d AgentRequest call sites", found)
}

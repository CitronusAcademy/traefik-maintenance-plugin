package traefik_maintenance_plugin_test

import (
	"os"
	"strings"
	"testing"
)

func TestWorkflowPins(t *testing.T) {
	const codeql = "24c54180a607b1449ed407dd24f251e4e9147c8d"
	const oldCodeql = "dd903d2e4f5405488e5ef1422510ee31c8b32357"
	codeqlBody := readPinFile(t, ".github/workflows/codeql.yml")
	scorecardBody := readPinFile(t, ".github/workflows/scorecard.yml")
	if strings.Contains(codeqlBody, oldCodeql) || strings.Contains(scorecardBody, oldCodeql) {
		t.Fatalf("workflows still pin CodeQL %s", oldCodeql)
	}
	if got := strings.Count(codeqlBody, codeql); got != 3 {
		t.Fatalf("codeql.yml must pin %s three times, got %d", codeql, got)
	}
	if got := strings.Count(scorecardBody, codeql); got != 1 {
		t.Fatalf("scorecard.yml must pin %s once, got %d", codeql, got)
	}

	ci := readPinFile(t, ".github/workflows/ci.yml")
	const lintAction = "ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a"
	const oldLintAction = "82606bf257cbaff209d206a39f5134f0cfbfd2ee"
	if strings.Contains(ci, oldLintAction) {
		t.Fatalf("ci.yml still pins golangci-lint-action %s", oldLintAction)
	}
	if !strings.Contains(ci, lintAction) {
		t.Fatalf("ci.yml missing golangci-lint-action %s", lintAction)
	}
	if !strings.Contains(ci, "version: v2.12.2") {
		t.Fatal("ci.yml lost golangci-lint v2.12.2")
	}

	compose := readPinFile(t, "e2e/docker-compose.yml")
	if strings.Contains(compose, "traefik:v3.1.5") {
		t.Fatal("e2e still uses traefik:v3.1.5")
	}
	if !strings.Contains(compose, "image: traefik:v3.7.14") {
		t.Fatal("e2e missing image: traefik:v3.7.14")
	}
	if !strings.Contains(compose, "image: traefik/whoami") || !strings.Contains(compose, "image: nginx:alpine") {
		t.Fatal("e2e stub images changed")
	}

	mod := readPinFile(t, "go.mod")
	if !strings.Contains(mod, "go 1.26.0\n") {
		t.Fatal("go.mod lost go 1.26.0")
	}
	if !strings.Contains(mod, "toolchain go1.26.9") {
		t.Fatal("go.mod lost toolchain go1.26.9")
	}
	if strings.Contains(mod, "go 1.25") || strings.Contains(mod, "go1.25") {
		t.Fatal("go.mod still selects go 1.25")
	}
}

func readPinFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

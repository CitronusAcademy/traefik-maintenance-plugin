package traefik_maintenance_plugin_test

import (
	"os"
	"strings"
	"testing"
)

func TestReadmeOmitsRetiredGoReportCard(t *testing.T) {
	body, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(strings.ToLower(text), "goreportcard") {
		t.Fatal("README.md still links to goreportcard.com")
	}
	for _, needle := range []string{
		"actions/workflows/ci.yml/badge.svg",
		"actions/workflows/e2e.yml/badge.svg",
		"codecov.io/gh/CitronusAcademy/traefik-maintenance-plugin/branch/main/graph/badge.svg",
		"img.shields.io/github/go-mod/go-version/CitronusAcademy/traefik-maintenance-plugin",
		"api.securityscorecards.dev/projects/github.com/CitronusAcademy/traefik-maintenance-plugin/badge",
		"img.shields.io/badge/License-MIT-yellow.svg",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("README.md missing badge %s", needle)
		}
	}
	const pin = `version: "v1.0.1"`
	if got := strings.Count(text, pin); got != 2 {
		t.Fatalf("README.md must pin %s twice, got %d", pin, got)
	}
}

func TestGolangciCommentOmitsGoReportCard(t *testing.T) {
	body, err := os.ReadFile(".golangci.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(strings.ToLower(text), "go report card") {
		t.Fatal(".golangci.yml still mentions Go Report Card")
	}
	if !strings.Contains(text, "min-complexity: 15") {
		t.Fatal(".golangci.yml lost the gocyclo threshold")
	}
}

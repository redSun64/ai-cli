// Package employee demonstrates a business application built with the SDK.
// Importing this package does not start the CLI or contact the service.
package employee

import (
	"embed"
	aicli "github.com/redSun64/ai-cli"
	"io/fs"
	"time"
)

//go:embed skills/employee
var skillFiles embed.FS

type Options struct {
	BaseURL         string
	LookupEnv       func(string) string
	CredentialStore aicli.CredentialStore
}
type Employee struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Account struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
}

// New composes SDK capabilities and the business module without startup I/O.
func New(options Options) (*aicli.Runtime, error) {
	base := options.BaseURL
	if base == "" {
		base = "http://localhost:18080"
	}
	auth, err := aicli.NewAuth(aicli.AuthConfig{DomainID: "employee", Type: "bearer", CredentialKey: "EMPLOYEE_API_TOKEN", Required: true, Store: options.CredentialStore, LookupEnv: options.LookupEnv})
	if err != nil {
		return nil, err
	}
	files, err := fs.Sub(skillFiles, "skills/employee")
	if err != nil {
		return nil, err
	}
	return aicli.New(aicli.Config{
		Name: "employee", Description: "Employee business capabilities.", Version: "0.1.0", Timeout: 30 * time.Second,
		Auth: auth, HTTP: &aicli.HTTPConfig{BaseURL: base, BaseURLEnv: "EMPLOYEE_BASE_URL", LookupEnv: options.LookupEnv},
		Skill: &aicli.SkillBundle{ID: "employee", Files: files}, Commands: commands(),
	})
}

package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ScaffoldResult records which scaffold files were written and which were
// left untouched because they already existed.
type ScaffoldResult struct {
	Generated []string
	Skipped   []string
}

// ScaffoldConfig configures language-agnostic scaffold generation. Scaffold
// files are generated once and never overwritten: services own them after
// the first run.
type ScaffoldConfig[T any] struct {
	// ScaffoldsDir is the root directory scaffolds are written to.
	ScaffoldsDir string

	// ReadmeData is the template data for scaffold-readme.tmpl.
	ReadmeData any

	// Namespaces lists namespace directories to create under ScaffoldsDir.
	Namespaces []string

	// Endpoints lists all endpoints to scaffold.
	Endpoints []T

	// EndpointNamespace returns the namespace an endpoint belongs to.
	EndpointNamespace func(T) string

	// NamespaceData returns the template data for scaffold-struct.tmpl.
	NamespaceData func(namespace string) any

	// EndpointData returns the template data for scaffold-impl.tmpl.
	EndpointData func(namespace string, endpoint T) any

	// EndpointFileName returns the file name for an endpoint scaffold.
	EndpointFileName func(endpoint T) string

	// ImplFileName is the per-namespace implementation struct file name
	// (e.g. "implementation.go").
	ImplFileName string

	// GenerateFile renders templateName to outputPath with data.
	GenerateFile func(templateName, outputPath string, data any) error
}

// WriteScaffolds writes scaffold files, skipping any that already exist.
func WriteScaffolds[T any](cfg ScaffoldConfig[T]) (*ScaffoldResult, error) {
	result := &ScaffoldResult{
		Generated: []string{},
		Skipped:   []string{},
	}

	if err := os.MkdirAll(cfg.ScaffoldsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create scaffolds dir %s: %w", cfg.ScaffoldsDir, err)
	}

	implFileName := strings.TrimSpace(cfg.ImplFileName)
	if implFileName == "" {
		return nil, fmt.Errorf("scaffold config requires ImplFileName")
	}

	record := func(generated bool, path string) {
		if generated {
			result.Generated = append(result.Generated, path)
		} else {
			result.Skipped = append(result.Skipped, path)
		}
	}

	readmePath := filepath.Join(cfg.ScaffoldsDir, "README.md")
	generated, err := GenerateFileIfNotExists(cfg.GenerateFile, "scaffold-readme.tmpl", readmePath, cfg.ReadmeData)
	if err != nil {
		return nil, fmt.Errorf("generate scaffold README: %w", err)
	}
	record(generated, readmePath)

	endpointsByNamespace := make(map[string][]T)
	for _, endpoint := range cfg.Endpoints {
		namespace := cfg.EndpointNamespace(endpoint)
		endpointsByNamespace[namespace] = append(endpointsByNamespace[namespace], endpoint)
	}

	for _, namespace := range cfg.Namespaces {
		namespaceDir := filepath.Join(cfg.ScaffoldsDir, namespace)
		if err := os.MkdirAll(namespaceDir, 0o755); err != nil {
			return nil, fmt.Errorf("create namespace dir %s: %w", namespaceDir, err)
		}

		implPath := filepath.Join(namespaceDir, implFileName)
		generated, err = GenerateFileIfNotExists(cfg.GenerateFile, "scaffold-struct.tmpl", implPath, cfg.NamespaceData(namespace))
		if err != nil {
			return nil, fmt.Errorf("generate namespace scaffold %s: %w", implPath, err)
		}
		record(generated, implPath)

		for _, endpoint := range endpointsByNamespace[namespace] {
			endpointPath := filepath.Join(namespaceDir, cfg.EndpointFileName(endpoint))
			generated, err = GenerateFileIfNotExists(cfg.GenerateFile, "scaffold-impl.tmpl", endpointPath, cfg.EndpointData(namespace, endpoint))
			if err != nil {
				return nil, fmt.Errorf("generate endpoint scaffold %s: %w", endpointPath, err)
			}
			record(generated, endpointPath)
		}
	}

	return result, nil
}

// GenerateFileIfNotExists generates a file only when it does not already exist.
func GenerateFileIfNotExists(
	generateFile func(templateName, outputPath string, data any) error,
	templateName string,
	outputPath string,
	data any,
) (bool, error) {
	if _, err := os.Stat(outputPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("check file exists %s: %w", outputPath, err)
	}

	if err := generateFile(templateName, outputPath, data); err != nil {
		return false, err
	}
	return true, nil
}

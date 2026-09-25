package openapi

import (
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
)

// File is one schema file of a rendered target representation.
//
// A target may be described by more than one file, and one of them is marked
// primary: the file a client that understands only one document starts from.
type File struct {
	// Name is the file's name, such as openapi.json.
	Name string
	// Content is the file's bytes.
	Content []byte
	// MediaType is the media type of the content.
	MediaType string
	// Primary marks the file a client should start from.
	Primary bool
}

// Rendered is a target representation of a standard description.
type Rendered struct {
	// Files are the target's schema files, in a defined order.
	Files []File
	// MediaType is the media type of the primary file.
	MediaType string
	// FileExtension is the extension of the primary file.
	FileExtension string
	// Warnings are the non-fatal findings of the translation. A value this
	// package derived rather than read from the description is always reported
	// here, so a derived value is never presented as a declared one.
	Warnings []string
}

// RenderOptions configures a translation.
type RenderOptions struct {
	// MediaType selects the serialization of the primary file: application/json
	// by default, or application/yaml.
	MediaType string
	// Options are target-specific switches, as key/value pairs. A switch this
	// package does not know is ignored, because the framework does not know every
	// target's vocabulary.
	Options map[string]string
}

// Render writes an OpenAPI document for a standard description.
//
// It reads only the standard description. Which format that description was
// parsed from is not this function's concern, and that is the point: a protobuf
// contract parsed into the standard model and published as an OpenAPI document is
// the same operation as the reverse.
func Render(described api.API, options RenderOptions) (Rendered, error) {
	document, warnings, err := renderOpenAPI(described, renderOptions{
		mediaType: strings.TrimSpace(options.MediaType),
		options:   options.Options,
	})
	if err != nil {
		return Rendered{}, err
	}
	name, mediaType, extension := schemaFileFor(options.MediaType)
	return Rendered{
		Files: []File{{
			Name:      name,
			Content:   document,
			MediaType: mediaType,
			Primary:   true,
		}},
		MediaType:     mediaType,
		FileExtension: extension,
		Warnings:      warnings,
	}, nil
}

// schemaFileFor names the schema file for a serialization. The name follows the
// content, so a caller that downloads the file is never handed YAML named .json.
func schemaFileFor(mediaType string) (name, resolved, extension string) {
	switch api.NormalizeIdentifier(mediaType) {
	case "application/yaml", "text/yaml", "application/x-yaml", "yaml":
		return schemaFileYAML, "application/yaml", "yaml"
	default:
		return schemaFileJSON, "application/json", "json"
	}
}

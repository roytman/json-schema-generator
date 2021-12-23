// Copyright 2021 IBM Corp.
// SPDX-License-Identifier: Apache-2.0

package schemas

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"log"
	"os"
	"path/filepath"
	"strings"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-tools/pkg/crd"
	crdmarkers "sigs.k8s.io/controller-tools/pkg/crd/markers"
	"sigs.k8s.io/controller-tools/pkg/genall"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
)

var (
	externalDocumentName = "external.json"
	schemaMarker         = markers.Must(markers.MakeDefinition("fybrik:validation:schema", markers.DescribesPackage, struct{}{}))
	objectMarker         = markers.Must(markers.MakeDefinition("fybrik:validation:object", markers.DescribesType, struct{}{}))
)

// Generator generates JSON schema objects.
type Generator struct {
	OutputDir string

	// AllowDangerousTypes allows types which are usually omitted from CRD generation
	// because they are not recommended.
	//
	// Currently the following additional types are allowed when this is true:
	// float32
	// float64
	//
	// Left unspecified, the default is false
	AllowDangerousTypes *bool `marker:",optional"`
}

type GeneratorContext struct {
	ctx        *genall.GenerationContext
	parser     *crd.Parser
	pkgMarkers map[*loader.Package]markers.MarkerValues
}

func (Generator) CheckFilter() loader.NodeFilter {
	return func(node ast.Node) bool {
		// ignore interfaces
		// _, isIface := node.(*ast.InterfaceType)
		return true
	}
}

func (Generator) RegisterMarkers(into *markers.Registry) error {
	// TODO: only register validation markers
	if err := crdmarkers.Register(into); err != nil {
		return err
	}

	if err := markers.RegisterAll(into, schemaMarker, objectMarker); err != nil {
		return err
	}
	into.AddHelp(schemaMarker,
		markers.SimpleHelp("object", "enable generation of JSON schema definition for the go structure"))
	into.AddHelp(objectMarker,
		markers.SimpleHelp("object", "enable generation of JSON schema object for the go structure"))
	return nil
}

func (g Generator) Generate(ctx *genall.GenerationContext) error {
	parser := &crd.Parser{
		Collector:           ctx.Collector,
		Checker:             ctx.Checker,
		AllowDangerousTypes: g.AllowDangerousTypes != nil && *g.AllowDangerousTypes,
	}
	crd.AddKnownTypes(parser)
	for _, root := range ctx.Roots {
		parser.NeedPackage(root)
	}

	context := &GeneratorContext{
		ctx:    ctx,
		parser: parser,
		// documents:  make(map[string]*apiext.JSONSchemaProps),
		pkgMarkers: make(map[*loader.Package]markers.MarkerValues),
	}

	for typeIdent := range parser.Types {
		context.NeedSchemaFor(typeIdent)
	}

	documents := make(map[string]*apiext.JSONSchemaProps)
	for typeIdent, typeSchema := range parser.Schemata {
		documentName := context.documentNameFor(typeIdent.Package)
		document, exists := documents[documentName]
		if !exists {
			document = &apiext.JSONSchemaProps{
				Title:       documentName,
				Definitions: make(apiext.JSONSchemaDefinitions),
			}
			documents[documentName] = document
		}
		document.Definitions[context.definitionNameFor(documentName, typeIdent)] = typeSchema
	}

	return g.output(documents)
}

func (g Generator) output(documents map[string]*apiext.JSONSchemaProps) error {
	// create out dir if needed
	err := os.MkdirAll(g.OutputDir, os.ModePerm)
	if err != nil {
		return err
	}

	for docName, doc := range documents {
		outputFilepath := filepath.Clean(filepath.Join(g.OutputDir, docName))

		// create the file
		f, err := os.Create(outputFilepath)
		if err != nil {
			return err
		}

		defer func() {
			if err := f.Close(); err != nil {
				log.Printf("Error closing file: %s\n", err)
			}
		}()

		bytes, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		_, err = f.Write(bytes)
		if err != nil {
			return err
		}
	}

	return nil
}

func (context *GeneratorContext) documentNameFor(pkg *loader.Package) string {
	isManaged := context.pkgMarkers[pkg].Get(schemaMarker.Name) != nil
	if isManaged {
		return fmt.Sprintf("%s.json", pkg.Name)
	}
	return externalDocumentName
}

func (context *GeneratorContext) definitionNameFor(documentName string, typeIdent crd.TypeIdent) string {
	if documentName == externalDocumentName {
		return qualifiedName(loader.NonVendorPath(typeIdent.Package.PkgPath), typeIdent.Name)
	}
	return typeIdent.Name
}

// qualifiedName constructs a JSONSchema-safe qualified name for a type
// (`<typeName>` or `<safePkgPath>~0<typeName>`, where `<safePkgPath>`
// is the package path with `/` replaced by `~1`, according to JSONPointer
// escapes).
func qualifiedName(pkgName, typeName string) string {
	if pkgName != "" {
		return strings.Replace(pkgName, "/", "~1", -1) + "~0" + typeName
	}
	return typeName
}

func (context *GeneratorContext) TypeRefLink(from *loader.Package, to crd.TypeIdent) string {
	fromDocument := context.documentNameFor(from)
	toDocument := context.documentNameFor(to.Package)

	prefix := "#/definitions/"
	if fromDocument != toDocument {
		prefix = toDocument + prefix
	}

	return prefix + context.definitionNameFor(toDocument, to)
}

func (context *GeneratorContext) NeedSchemaFor(typ crd.TypeIdent) {
	p := context.parser

	context.parser.NeedPackage(typ.Package)
	if _, knownSchema := context.parser.Schemata[typ]; knownSchema {
		return
	}

	info, knownInfo := p.Types[typ]
	if !knownInfo {
		typ.Package.AddError(fmt.Errorf("unknown type %s", typ))
		return
	}

	// avoid tripping recursive schemata, like ManagedFields, by adding an empty WIP schema
	p.Schemata[typ] = apiext.JSONSchemaProps{}

	schemaCtx := newSchemaContext(typ.Package, context, p.AllowDangerousTypes)
	ctxForInfo := schemaCtx.ForInfo(info)

	pkgMarkers, err := markers.PackageMarkers(p.Collector, typ.Package)
	if err != nil {
		typ.Package.AddError(err)
	}
	ctxForInfo.PackageMarkers = pkgMarkers
	context.pkgMarkers[typ.Package] = pkgMarkers

	schema := infoToSchema(ctxForInfo)

	p.Schemata[typ] = *schema
}

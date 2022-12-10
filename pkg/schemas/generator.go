// Copyright 2021 IBM Corp.
// SPDX-License-Identifier: Apache-2.0

package schemas

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/types"
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
	objectMarker         = markers.Must(markers.MakeDefinition("fybrik:validation:object", markers.DescribesType, ObjName(Empty)))
)

type ObjName string

func (n ObjName) ApplyToSchema(schema *apiext.JSONSchemaProps) error {
	schema.Title = string(n)
	return nil
}

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
	ctx    *genall.GenerationContext
	parser *crd.Parser
	// Array of packages that have a type with object marker
	objectPkgs []string
	pkgMarkers map[*loader.Package]markers.MarkerValues
}

func (Generator) CheckFilter() loader.NodeFilter {
	return func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.InterfaceType:
			// skip interfaces, we never care about references in them
			return false
		case *ast.Field:
			_, hasTag := loader.ParseAstTag(node.Tag).Lookup("json")
			// fields without JSON tags mean we have custom serialization,
			// so only visit fields with tags.
			return hasTag
		default:
			return true
		}
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

	context := &GeneratorContext{
		ctx:        ctx,
		parser:     parser,
		objectPkgs: []string{},
		pkgMarkers: make(map[*loader.Package]markers.MarkerValues),
	}

	// Load input packages
	for _, root := range ctx.Roots {
		parser.NeedPackage(root)
		// Load package markers
		pkgMarkers, err := markers.PackageMarkers(parser.Collector, root)
		if err != nil {
			root.AddError(err)
		}
		context.pkgMarkers[root] = pkgMarkers
	}

	// Scan loaded types
	for typeIdent := range parser.Types {
		info, knownInfo := parser.Types[typeIdent]
		if knownInfo {
			if info.Markers.Get(objectMarker.Name) != nil {
				context.objectPkgs = append(context.objectPkgs, typeIdent.Package.PkgPath)
				context.NeedSchemaFor(typeIdent)
			}
		}
		if pkgMarkers, hasMarkers := context.pkgMarkers[typeIdent.Package]; hasMarkers {
			if pkgMarkers.Get(schemaMarker.Name) != nil {
				// Loaded type is in a package with fybrik:validation:schema marker
				// Get a JSON schema from that type (recursive)
				context.NeedSchemaFor(typeIdent)
			}
		}
	}

	documents := make(map[string]*apiext.JSONSchemaProps)
	//nolint:gocritic
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

		// Generate a schema for types with "fybrik:validation:object" marker
		info, knownInfo := parser.Types[typeIdent]
		if knownInfo {
			if info.Markers.Get(objectMarker.Name) != nil {
				listFields, _ := context.getFields(typeIdent)
				schemaPtr := parser.Schemata[typeIdent]
				documentName := fmt.Sprintf("%s.json", schemaPtr.Title)
				document, exists := documents[documentName]
				context.removeExtraProps(typeIdent, &schemaPtr, &listFields)
				if !exists {
					document = schemaPtr.DeepCopy()
					document.Title = documentName
					document.Definitions = make(apiext.JSONSchemaDefinitions)
					documents[documentName] = document
				}

				for _, fieldType := range listFields {
					typeSchemaField := parser.Schemata[fieldType]
					context.removeExtraProps(fieldType, &typeSchemaField, &listFields)
					document.Definitions[context.definitionNameFor(documentName, fieldType)] = typeSchemaField
				}
			}
		}
	}

	return g.output(documents)
}

// Get the fields that related to taxonomy (has a taxonomy child)
// It returns true iff the type has a taxonomy child
func (context *GeneratorContext) getFields(typ crd.TypeIdent) ([]crd.TypeIdent, bool) {
	ListFields := []crd.TypeIdent{}
	isTaxonomy := false
	info, knownInfo := context.parser.Types[typ]
	if knownInfo {
		fields := info.Fields
		for _, field := range fields {
			fieldTypeName := field.RawField.Type
			// Create a crd.typeIdent for the field
			typeIdentField := typeToTypeIdent(fieldTypeName, typ.Package)

			_, fieldKnownInfo := context.parser.Types[typeIdentField]
			if !fieldKnownInfo {
				continue
			}
			// Check if the field is from a package with the `schema` marker
			if context.pkgMarkers[typeIdentField.Package].Get(schemaMarker.Name) != nil {
				isTaxonomy = true
				continue
			}
			// Get the fields of the current field (child)
			childListFields, childTaxonomy := context.getFields(typeIdentField)
			// If the child is related to taxonomy, then add the child and his fields list to the parent fields list
			if childTaxonomy {
				ListFields = append(ListFields, typeIdentField)
				ListFields = append(ListFields, childListFields...)
				isTaxonomy = true
			}
		}
	}
	return ListFields, isTaxonomy
}

// Create a crd.TypeIdent for a given AST type
func typeToTypeIdent(fieldTypeName ast.Expr, pkg *loader.Package) crd.TypeIdent {
	typeIdentField := crd.TypeIdent{Package: nil, Name: Empty}
	switch expr := fieldTypeName.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.StructType:
		typeInfo := pkg.TypesInfo.TypeOf(expr)
		if namedInfo, isNamed := typeInfo.(*types.Named); isNamed {
			pkgPath := loader.NonVendorPath(namedInfo.Obj().Pkg().Path())
			typeIdentField = typeIdentFor(pkgPath, namedInfo.Obj().Name(), pkg)
		}
	case *ast.ArrayType:
		typeIdentField = typeToTypeIdent(expr.Elt, pkg)
	case *ast.MapType:
		typeIdentField = typeToTypeIdent(expr.Value, pkg)
	case *ast.StarExpr:
		typeIdentField = typeToTypeIdent(expr.X, pkg)
	}
	return typeIdentField
}

// Create a crt.TypeIdent for a type with a given package path
func typeIdentFor(pkgPath, typeName string, pkg *loader.Package) crd.TypeIdent {
	if pkgPath == pkg.PkgPath {
		return crd.TypeIdent{
			Package: pkg,
			Name:    typeName,
		}
	}
	if pkgPath != "" {
		pkg = pkg.Imports()[pkgPath]
	}
	return crd.TypeIdent{
		Package: pkg,
		Name:    typeName,
	}
}

func indexOf(a string, list []string) int {
	for i, b := range list {
		if b == a {
			return i
		}
	}
	return -1
}

// Remove fields that is not related to taxonomy
func (context *GeneratorContext) removeExtraProps(typeIdent crd.TypeIdent, v *apiext.JSONSchemaProps, listFields *[]crd.TypeIdent) {
	info, knownInfo := context.parser.Types[typeIdent]
	if knownInfo {
		fieldTypes := []string{}
		for _, typ := range *listFields {
			fieldTypes = append(fieldTypes, typ.Name)
		}
		typeFields := info.Fields
		for _, field := range typeFields {
			fieldTypeName := field.RawField.Type
			// Get the crd.TypeIdent of the current field
			typeIdentField := typeToTypeIdent(fieldTypeName, typeIdent.Package)
			// If the field has a type from a package with the `schema` marker then keep it
			if context.pkgMarkers[typeIdentField.Package].Get(schemaMarker.Name) != nil {
				continue
			}
			// If the field is not in the list of the needed fields then remove it from the schema
			_, fieldKnownInfo := context.parser.Types[typeIdentField]
			if indexOf(typeIdentField.Name, fieldTypes) == -1 || !fieldKnownInfo {
				jsonTag, hasTag := field.Tag.Lookup("json")
				if !hasTag {
					continue
				}
				jsonOpts := strings.Split(jsonTag, ",")
				delete(v.Properties, jsonOpts[0])
				index := indexOf(jsonOpts[0], v.Required)
				if index != -1 {
					length := len(v.Required)
					v.Required[index] = v.Required[length-1]
					v.Required = v.Required[:length-1]
				}
			}
		}
	}
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
		//nolint:gocritic
		defer func() {
			if err = f.Close(); err != nil {
				log.Printf("Error closing file: %s\n", err)
			}
		}()

		bytes, err := json.MarshalIndent(doc, Empty, "  ")
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
	if pkgName != Empty {
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
	// Build the suffix string as a <typeName> if the type is in a package with
	// the `schema` marker or in a package with a type that has the `object` marker
	// Otherwise, the suffix will be build using qualifiedName function
	suffix := to.Name
	if indexOf(to.Package.PkgPath, context.objectPkgs) == -1 {
		suffix = context.definitionNameFor(toDocument, to)
	}
	return prefix + suffix
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

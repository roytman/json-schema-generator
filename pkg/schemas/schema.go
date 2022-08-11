/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This file is modified from https://github.com/kubernetes-sigs/controller-tools/blob/v0.7.0/pkg/crd/schema.go
// Modifications are listed in // Note comments

package schemas

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-tools/pkg/crd"
	crdmarkers "sigs.k8s.io/controller-tools/pkg/crd/markers"

	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
)

// Schema flattening is done in a recursive mapping method.
// Start reading at infoToSchema.

// byteType is the types.Type for byte (see the types documention
// for why we need to look this up in the Universe), saved
// for quick comparison.
var byteType = types.Universe.Lookup("byte").Type()

// SchemaMarker is any marker that needs to modify the schema of the underlying type or field.
type SchemaMarker interface {
	// ApplyToSchema is called after the rest of the schema for a given type
	// or field is generated, to modify the schema appropriately.
	ApplyToSchema(*apiext.JSONSchemaProps) error
}

// applyFirstMarker is applied before any other markers.  It's a bit of a hack.
type applyFirstMarker interface {
	ApplyFirst()
}

// schemaRequester knows how to marker that another schema (e.g. via an external reference) is necessary.
type schemaRequester interface {
	NeedSchemaFor(typ crd.TypeIdent)
	// Note(roee88): TypeRefLink extracted to be controlled by caller
	TypeRefLink(from *loader.Package, to crd.TypeIdent) string
}

// schemaContext stores and provides information across a hierarchy of schema generation.
type schemaContext struct {
	pkg  *loader.Package
	info *markers.TypeInfo

	schemaRequester schemaRequester
	PackageMarkers  markers.MarkerValues

	allowDangerousTypes bool
}

// newSchemaContext constructs a new schemaContext for the given package and schema requester.
// It must have type info added before use via ForInfo.
func newSchemaContext(pkg *loader.Package, req schemaRequester, allowDangerousTypes bool) *schemaContext {
	pkg.NeedTypesInfo()
	return &schemaContext{
		pkg:                 pkg,
		schemaRequester:     req,
		allowDangerousTypes: allowDangerousTypes,
	}
}

// ForInfo produces a new schemaContext with containing the same information
// as this one, except with the given type information.
func (c *schemaContext) ForInfo(info *markers.TypeInfo) *schemaContext {
	return &schemaContext{
		pkg:                 c.pkg,
		info:                info,
		schemaRequester:     c.schemaRequester,
		allowDangerousTypes: c.allowDangerousTypes,
	}
}

// requestSchema asks for the schema for a type in the package with the
// given import path.
func (c *schemaContext) typeIdentFor(pkgPath, typeName string) crd.TypeIdent {
	pkg := c.pkg
	if pkgPath != "" {
		pkg = c.pkg.Imports()[pkgPath]
	}
	return crd.TypeIdent{
		Package: pkg,
		Name:    typeName,
	}
}

// requestSchema asks for the schema for a type in the package with the
// given import path.
func (c *schemaContext) requestSchema(typeIdent crd.TypeIdent) {
	c.schemaRequester.NeedSchemaFor(typeIdent)
}

// infoToSchema creates a schema for the type in the given set of type information.
func infoToSchema(ctx *schemaContext) *apiext.JSONSchemaProps {
	// If the obj implements a JSON marshaler and has a marker, use the markers value and do not traverse as
	// the marshaler could be doing anything. If there is no marker, fall back to traversing.
	if obj := ctx.pkg.Types.Scope().Lookup(ctx.info.Name); obj != nil && implementsJSONMarshaler(obj.Type()) {
		schema := &apiext.JSONSchemaProps{}
		applyMarkers(ctx, ctx.info.Markers, schema, ctx.info.RawSpec.Type)
		if schema.Type != "" {
			return schema
		}
	}
	return typeToSchema(ctx, ctx.info.RawSpec.Type)
}

// applyMarkers applies schema markers to the given schema, respecting "apply first" markers.
func applyMarkers(ctx *schemaContext, markerSet markers.MarkerValues, props *apiext.JSONSchemaProps, node ast.Node) {
	// apply "apply first" markers first...
	for _, markerValues := range markerSet {
		for _, markerValue := range markerValues {
			if _, isApplyFirst := markerValue.(applyFirstMarker); !isApplyFirst {
				continue
			}

			schemaMarker, isSchemaMarker := markerValue.(SchemaMarker)
			if !isSchemaMarker {
				continue
			}

			if err := schemaMarker.ApplyToSchema(props); err != nil {
				ctx.pkg.AddError(loader.ErrFromNode(err /* an okay guess */, node))
			}
		}
	}

	// ...then the rest of the markers
	for _, markerValues := range markerSet {
		for _, markerValue := range markerValues {
			if _, isApplyFirst := markerValue.(applyFirstMarker); isApplyFirst {
				// skip apply-first markers, which were already applied
				continue
			}

			schemaMarker, isSchemaMarker := markerValue.(SchemaMarker)
			if !isSchemaMarker {
				continue
			}
			if err := schemaMarker.ApplyToSchema(props); err != nil {
				ctx.pkg.AddError(loader.ErrFromNode(err /* an okay guess */, node))
			}
		}
	}

	// Note(roee88): exchange x-kubernetes-preserve-unknown-fields with additionalProperties: true
	if props.XPreserveUnknownFields != nil && *props.XPreserveUnknownFields {
		if props.AdditionalProperties == nil {
			props.AdditionalProperties = &apiext.JSONSchemaPropsOrBool{}
		}
		props.AdditionalProperties.Allows = true
		props.XPreserveUnknownFields = nil
	}
}

// typeToSchema creates a schema for the given AST type.
func typeToSchema(ctx *schemaContext, rawType ast.Expr) *apiext.JSONSchemaProps {
	var props *apiext.JSONSchemaProps
	switch expr := rawType.(type) {
	case *ast.Ident:
		props = localNamedToSchema(ctx, expr)
	case *ast.SelectorExpr:
		props = namedToSchema(ctx, expr)
	case *ast.ArrayType:
		props = arrayToSchema(ctx, expr)
	case *ast.MapType:
		props = mapToSchema(ctx, expr)
	case *ast.StarExpr:
		props = typeToSchema(ctx, expr.X)
	case *ast.StructType:
		props = structToSchema(ctx, expr)
	default:
		ctx.pkg.AddError(loader.ErrFromNode(fmt.Errorf("unsupported AST kind %T", expr), rawType))
		// NB(directxman12): we explicitly don't handle interfaces
		return &apiext.JSONSchemaProps{}
	}

	props.Description = ctx.info.Doc

	applyMarkers(ctx, ctx.info.Markers, props, rawType)

	return props
}

// localNamedToSchema creates a schema (ref) for a *potentially* local type reference
// (could be external from a dot-import).
func localNamedToSchema(ctx *schemaContext, ident *ast.Ident) *apiext.JSONSchemaProps {
	typeInfo := ctx.pkg.TypesInfo.TypeOf(ident)
	if typeInfo == types.Typ[types.Invalid] {
		ctx.pkg.AddError(loader.ErrFromNode(fmt.Errorf("unknown type %s", ident.Name), ident))
		return &apiext.JSONSchemaProps{}
	}
	if basicInfo, isBasic := typeInfo.(*types.Basic); isBasic {
		typ, format, err := builtinToType(basicInfo, ctx.allowDangerousTypes)
		if err != nil {
			ctx.pkg.AddError(loader.ErrFromNode(err, ident))
		}
		return &apiext.JSONSchemaProps{
			Type:   typ,
			Format: format,
		}
	}
	// NB(directxman12): if there are dot imports, this might be an external reference,
	// so use typechecking info to get the actual object
	typeNameInfo := typeInfo.(*types.Named).Obj()
	pkg := typeNameInfo.Pkg()
	pkgPath := loader.NonVendorPath(pkg.Path())
	if pkg == ctx.pkg.Types {
		pkgPath = ""
	}

	typeIdent := ctx.typeIdentFor(pkgPath, typeNameInfo.Name())
	ctx.requestSchema(typeIdent)
	link := ctx.schemaRequester.TypeRefLink(ctx.pkg, typeIdent)
	return &apiext.JSONSchemaProps{
		Ref: &link,
	}
}

// namedSchema creates a schema (ref) for an explicitly external type reference.
func namedToSchema(ctx *schemaContext, named *ast.SelectorExpr) *apiext.JSONSchemaProps {
	typeInfoRaw := ctx.pkg.TypesInfo.TypeOf(named)
	if typeInfoRaw == types.Typ[types.Invalid] {
		ctx.pkg.AddError(loader.ErrFromNode(fmt.Errorf("unknown type %v.%s", named.X, named.Sel.Name), named))
		return &apiext.JSONSchemaProps{}
	}
	typeInfo := typeInfoRaw.(*types.Named)
	typeNameInfo := typeInfo.Obj()
	nonVendorPath := loader.NonVendorPath(typeNameInfo.Pkg().Path())
	typeIdent := ctx.typeIdentFor(nonVendorPath, typeNameInfo.Name())
	ctx.requestSchema(typeIdent)
	link := ctx.schemaRequester.TypeRefLink(ctx.pkg, typeIdent)
	return &apiext.JSONSchemaProps{
		Ref: &link,
	}
	// NB(directxman12): we special-case things like resource.Quantity during the "collapse" phase.
}

// arrayToSchema creates a schema for the items of the given array, dealing appropriately
// with the special `[]byte` type (according to OpenAPI standards).
func arrayToSchema(ctx *schemaContext, array *ast.ArrayType) *apiext.JSONSchemaProps {
	eltType := ctx.pkg.TypesInfo.TypeOf(array.Elt)
	if eltType == byteType && array.Len == nil {
		// byte slices are represented as base64-encoded strings
		// (the format is defined in OpenAPI v3, but not JSON Schema)
		return &apiext.JSONSchemaProps{
			Type:   "string",
			Format: "byte",
		}
	}
	// TODO(directxman12): backwards-compat would require access to markers from base info
	items := typeToSchema(ctx.ForInfo(&markers.TypeInfo{}), array.Elt)

	return &apiext.JSONSchemaProps{
		Type:  "array",
		Items: &apiext.JSONSchemaPropsOrArray{Schema: items},
	}
}

// mapToSchema creates a schema for items of the given map.  Key types must eventually resolve
// to string (other types aren't allowed by JSON, and thus the kubernetes API standards).
func mapToSchema(ctx *schemaContext, mapType *ast.MapType) *apiext.JSONSchemaProps {
	keyInfo := ctx.pkg.TypesInfo.TypeOf(mapType.Key)
	// check that we've got a type that actually corresponds to a string
	for keyInfo != nil {
		switch typedKey := keyInfo.(type) {
		case *types.Basic:
			if typedKey.Info()&types.IsString == 0 {
				ctx.pkg.AddError(loader.ErrFromNode(fmt.Errorf("map keys must be strings, not %s", keyInfo.String()), mapType.Key))
				return &apiext.JSONSchemaProps{}
			}
			keyInfo = nil // stop iterating
		case *types.Named:
			keyInfo = typedKey.Underlying()
		default:
			ctx.pkg.AddError(loader.ErrFromNode(fmt.Errorf("map keys must be strings, not %s", keyInfo.String()), mapType.Key))
			return &apiext.JSONSchemaProps{}
		}
	}

	// TODO(directxman12): backwards-compat would require access to markers from base info
	var valSchema *apiext.JSONSchemaProps
	switch val := mapType.Value.(type) {
	case *ast.Ident:
		valSchema = localNamedToSchema(ctx.ForInfo(&markers.TypeInfo{}), val)
	case *ast.SelectorExpr:
		valSchema = namedToSchema(ctx.ForInfo(&markers.TypeInfo{}), val)
	case *ast.ArrayType:
		valSchema = arrayToSchema(ctx.ForInfo(&markers.TypeInfo{}), val)
	case *ast.StarExpr:
		valSchema = typeToSchema(ctx.ForInfo(&markers.TypeInfo{}), val)
	case *ast.MapType:
		valSchema = typeToSchema(ctx.ForInfo(&markers.TypeInfo{}), val)
	default:
		ctx.pkg.AddError(loader.ErrFromNode(fmt.Errorf("not a supported map value type: %T", mapType.Value), mapType.Value))
		return &apiext.JSONSchemaProps{}
	}

	return &apiext.JSONSchemaProps{
		Type: "object",
		AdditionalProperties: &apiext.JSONSchemaPropsOrBool{
			Schema: valSchema,
			Allows: true, /* set automatically by serialization, but useful for testing */
		},
	}
}

// structToSchema creates a schema for the given struct.  Embedded fields are placed in AllOf,
// and can be flattened later with a Flattener.
//
//nolint:gocyclo
func structToSchema(ctx *schemaContext, structType *ast.StructType) *apiext.JSONSchemaProps {
	props := &apiext.JSONSchemaProps{
		Type:       "object",
		Properties: make(map[string]apiext.JSONSchemaProps),
	}

	if ctx.info.RawSpec.Type != structType {
		ctx.pkg.AddError(loader.ErrFromNode(fmt.Errorf("encountered non-top-level struct (possibly embedded), those aren't allowed"), structType))
		return props
	}

	for _, field := range ctx.info.Fields {
		jsonTag, hasTag := field.Tag.Lookup("json")
		if !hasTag {
			// if the field doesn't have a JSON tag, it doesn't belong in output (and shouldn't exist in a serialized type)
			ctx.pkg.AddError(loader.ErrFromNode(
				fmt.Errorf("encountered struct field %q without JSON tag in type %q", field.Name, ctx.info.Name), field.RawField))
			continue
		}
		jsonOpts := strings.Split(jsonTag, ",")
		if len(jsonOpts) == 1 && jsonOpts[0] == "-" {
			// skipped fields have the tag "-" (note that "-," means the field is named "-")
			continue
		}

		inline := false
		omitEmpty := false
		for _, opt := range jsonOpts[1:] {
			switch opt {
			case "inline":
				inline = true
			case "omitempty":
				omitEmpty = true
			}
		}
		fieldName := jsonOpts[0]
		inline = inline || fieldName == "" // anonymous fields are inline fields in YAML/JSON

		// if no default required mode is set, default to required
		defaultMode := "required"
		if ctx.PackageMarkers.Get("kubebuilder:validation:Optional") != nil {
			defaultMode = "optional"
		}

		switch defaultMode {
		// if this package isn't set to optional default...
		case "required":
			// ...everything that's not inline, omitempty, or explicitly optional is required
			if !inline && !omitEmpty && field.Markers.Get("kubebuilder:validation:Optional") == nil && field.Markers.Get("optional") == nil {
				props.Required = append(props.Required, fieldName)
			}

		// if this package isn't set to required default...
		case "optional":
			// ...everything that isn't explicitly required is optional
			if field.Markers.Get("kubebuilder:validation:Required") != nil {
				props.Required = append(props.Required, fieldName)
			}
		}

		var propSchema *apiext.JSONSchemaProps
		if field.Markers.Get(crdmarkers.SchemalessName) != nil {
			propSchema = &apiext.JSONSchemaProps{}
		} else {
			propSchema = typeToSchema(ctx.ForInfo(&markers.TypeInfo{}), field.RawField.Type)
		}
		propSchema.Description = field.Doc

		applyMarkers(ctx, field.Markers, propSchema, field.RawField)

		if inline {
			props.AllOf = append(props.AllOf, *propSchema)
			continue
		}

		props.Properties[fieldName] = *propSchema
	}

	return props
}

// builtinToType converts builtin basic types to their equivalent JSON schema form.
// It *only* handles types allowed by the kubernetes API standards. Floats are not
// allowed unless allowDangerousTypes is true
func builtinToType(basic *types.Basic, allowDangerousTypes bool) (typ, format string, err error) {
	// NB(directxman12): formats from OpenAPI v3 are slightly different than those defined
	// in JSONSchema.  This'll use the OpenAPI v3 ones, since they're useful for bounding our
	// non-string types.
	basicInfo := basic.Info()
	switch {
	case basicInfo&types.IsBoolean != 0:
		typ = "boolean"
	case basicInfo&types.IsString != 0:
		typ = "string"
	case basicInfo&types.IsInteger != 0:
		typ = "integer"
	case basicInfo&types.IsFloat != 0:
		if allowDangerousTypes {
			typ = "number"
		} else {
			return "", "", errors.New("found float, the usage of which is highly discouraged, as support for them varies across languages. " +
				"Please consider serializing your float as string instead. " +
				"If you are really sure you want to use them, re-run with crd:allowDangerousTypes=true")
		}
	default:
		return "", "", fmt.Errorf("unsupported type %q", basic.String())
	}

	switch basic.Kind() {
	case types.Int32, types.Uint32:
		format = "int32"
	case types.Int64, types.Uint64:
		format = "int64"
	}

	return typ, format, nil
}

// Open coded go/types representation of encoding/json.Marshaller
var jsonMarshaler = types.NewInterfaceType([]*types.Func{
	types.NewFunc(token.NoPos, nil, "MarshalJSON",
		types.NewSignature(nil, nil,
			types.NewTuple(
				types.NewVar(token.NoPos, nil, "", types.NewSlice(types.Universe.Lookup("byte").Type())),
				types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type())), false)),
}, nil).Complete()

func implementsJSONMarshaler(typ types.Type) bool {
	return types.Implements(typ, jsonMarshaler) || types.Implements(types.NewPointer(typ), jsonMarshaler)
}

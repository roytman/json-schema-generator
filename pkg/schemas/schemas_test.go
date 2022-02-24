package schemas

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/xeipuuv/gojsonschema"

	fybrikobject "fybrik.io/json-schema-generator/testPkgs/fybrikobject"
	schemapkg "fybrik.io/json-schema-generator/testPkgs/schemapkg"
)

func TestValidApp(t *testing.T) {
	schemaPath := "../../testdata/schema/sample_crd.json"
	schemaPath, err := filepath.Abs(schemaPath)
	if err != nil {
		t.Errorf("error %v\n", err)
		return
	}
	resourceJSON, err := createResource()
	if err != nil {
		t.Errorf("error M %v\n", err)
		return
	}
	// Validate resource against taxonomy
	taxonomyLoader := gojsonschema.NewReferenceLoader("file://" + schemaPath)
	documentLoader := gojsonschema.NewBytesLoader(resourceJSON)
	result, err := gojsonschema.Validate(taxonomyLoader, documentLoader)
	if err != nil {
		t.Errorf("could not validate resource against the provided schema, err: %v\n", err)
		return
	}
	errors := result.Errors()
	for i, e := range errors {
		t.Errorf("Error %d %v\n", i, e)
		return
	}
	if !result.Valid() {
		t.Error("test failed")
	}
}

func TestInvalidApp(t *testing.T) {
	schemaPath := "../../testdata/schema/sample_crd.json"
	schemaPath, err := filepath.Abs(schemaPath)
	if err != nil {
		t.Errorf("error %v\n", err)
		return
	}
	resourceJSON, err := createInvalidResource()
	if err != nil {
		t.Errorf("error M %v\n", err)
		return
	}
	// Validate resource against schema
	taxonomyLoader := gojsonschema.NewReferenceLoader("file://" + schemaPath)
	documentLoader := gojsonschema.NewBytesLoader(resourceJSON)
	result, err := gojsonschema.Validate(taxonomyLoader, documentLoader)
	if err != nil {
		t.Errorf("could not validate resource against the provided schema, err: %v\n", err)
		return
	}
	errors := result.Errors()
	for _, e := range errors {
		if e.String() != "field1.type1f1: schemaf2 is required" {
			t.Error("Wrong error message\n")
			return
		}
	}
	if result.Valid() {
		t.Error("test failed")
	}
}

func createResource() ([]byte, error) {
	crd := fybrikobject.SampleCrd{}
	schemaType1 := schemapkg.SchemaType1{}
	schemaType1.SchemaF1 = true
	schemaType1.SchemaF2 = "schema"
	field1 := fybrikobject.Type1{}
	field1.Type1F1 = schemaType1
	crd.Field1 = field1
	crd.Field3 = "crd"
	buf, err := json.Marshal(crd)
	return buf, err
}

func createInvalidResource() ([]byte, error) {
	crd := fybrikobject.SampleCrd{}
	schemaType1 := schemapkg.SchemaType1{}
	schemaType1.SchemaF1 = true
	field1 := fybrikobject.Type1{}
	field1.Type1F1 = schemaType1
	crd.Field1 = field1
	crd.Field3 = "crd"
	buf, err := json.Marshal(crd)
	return buf, err
}

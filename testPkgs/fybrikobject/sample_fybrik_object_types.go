package fybrikobject

import schemapkg "fybrik.io/json-schema-generator/testPkgs/schemapkg"

// +fybrik:validation:object="sample_crd"
type SampleCrd struct {
	Field1 Type1  `json:"field1"`
	Field2 Type2  `json:"field2"`
	Field3 string `json:"field3"`
}

type Type1 struct {
	Type1F1 schemapkg.SchemaType1 `json:"type1f1,omitempty"`
	Type1F2 string                `json:"type1f2,omitempty"`
}

type Type2 struct {
	Type2F1 bool   `json:"type2f1,omitempty"`
	Type2F2 string `json:"type2f2,omitempty"`
}

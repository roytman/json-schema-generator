package schemapkg

type SchemaType1 struct {

	// +kubebuilder:validation:Required
	SchemaF1 bool `json:"schemaf1,omitempty"`

	// +kubebuilder:validation:Required
	SchemaF2 string `json:"schemaf2,omitempty"`
}

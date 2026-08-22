package basic

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidatorAcceptsSupportedSchemaFeatures(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"name":{"type":"string","minLength":2,"maxLength":4},
			"count":{"type":"integer","minimum":1,"maximum":3},
			"mode":{"type":"string","enum":["fast","safe"]},
			"tags":{"type":"array","items":{"type":"string"}}
		},
		"required":["name","count"],
		"additionalProperties":false
	}`)
	instance := json.RawMessage(`{"name":"你好","count":2,"mode":"safe","tags":["go"]}`)

	if err := New().Validate(schema, instance); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidatorReportsStableValidationDetails(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"count":{"type":"integer","minimum":1}},
		"required":["count"],
		"additionalProperties":false
	}`)
	tests := []struct {
		name     string
		instance string
		path     string
		keyword  string
	}{
		{name: "required", instance: `{}`, path: "$.count", keyword: "required"},
		{name: "type", instance: `{"count":1.5}`, path: "$.count", keyword: "type"},
		{name: "minimum", instance: `{"count":0}`, path: "$.count", keyword: "minimum"},
		{name: "additional", instance: `{"count":1,"extra":true}`, path: "$.extra", keyword: "additionalProperties"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := New().Validate(schema, json.RawMessage(test.instance))
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Validate() error = %v, want ValidationError", err)
			}
			if validationErr.Path != test.path || validationErr.Keyword != test.keyword {
				t.Fatalf("ValidationError = %+v", validationErr)
			}
		})
	}
}

func TestValidatorRejectsMalformedOrUnsupportedInput(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		instance string
	}{
		{name: "multiple values", schema: `{"type":"object"}`, instance: `{} {}`},
		{name: "schema root", schema: `[]`, instance: `{}`},
		{name: "unsupported type", schema: `{"type":"function"}`, instance: `{}`},
		{name: "unsupported additional schema", schema: `{"type":"object","additionalProperties":{}}`, instance: `{}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := New().Validate(json.RawMessage(test.schema), json.RawMessage(test.instance)); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

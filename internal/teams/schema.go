package teams

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// MaxReportSummaryBytes is the 1 KiB cap on a task summary (Q-127, Q-131).
	MaxReportSummaryBytes = 1024
	// MaxReportOutputBytes is the 64 KiB cap on structured task output (Q-127).
	MaxReportOutputBytes = 64 * 1024
	// MaxOutputSchemaBytes bounds an output_schema before parsing.
	MaxOutputSchemaBytes = 64 * 1024
	// MaxSchemaErrorBytes bounds the schema validation error text (Q-128).
	MaxSchemaErrorBytes = 512
)

// ValidateOutputAgainstSchema validates outputRaw against schemaRaw using
// jsonschema/v6 (OR-E03, Q-128). Size bounds are checked before parsing.
// An empty schemaRaw returns (true, ""). A wrong or uncompilable schema or
// non-conforming output returns (false, boundedError).
func ValidateOutputAgainstSchema(schemaRaw, outputRaw json.RawMessage) (bool, string) {
	if len(bytes.TrimSpace(schemaRaw)) == 0 {
		return true, ""
	}
	if len(schemaRaw) > MaxOutputSchemaBytes {
		return false, "output_schema exceeds size limit"
	}
	if len(outputRaw) == 0 {
		return false, "output is required when output_schema is set"
	}
	if len(outputRaw) > MaxReportOutputBytes {
		return false, "output exceeds size limit"
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
	if err != nil {
		return false, boundSchemaError("invalid output_schema JSON: " + err.Error())
	}
	c := jsonschema.NewCompiler()
	if addErr := c.AddResource("schema.json", schemaDoc); addErr != nil {
		return false, boundSchemaError("invalid output_schema: " + addErr.Error())
	}
	compiled, err := c.Compile("schema.json")
	if err != nil {
		return false, boundSchemaError("uncompilable output_schema: " + err.Error())
	}
	val, err := jsonschema.UnmarshalJSON(bytes.NewReader(outputRaw))
	if err != nil {
		return false, boundSchemaError("invalid output JSON: " + err.Error())
	}
	if err := compiled.Validate(val); err != nil {
		return false, boundSchemaError(err.Error())
	}
	return true, ""
}

func boundSchemaError(msg string) string {
	msg = strings.ReplaceAll(msg, "\r", " ")
	msg = strings.ReplaceAll(msg, "\n", " ")
	if len(msg) <= MaxSchemaErrorBytes {
		return msg
	}
	return msg[:MaxSchemaErrorBytes]
}

package githubrunners

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/pkg/v3/codegen/schema"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The schema infer writes types every object, map and array input, and every field inside an
// object, as pulumi.Input in the SDKs, whatever the Go field is. A program could then pass an
// Output where the Go field is plain, and the deployment failed at Construct ("copying input ...:
// expected destination type to implement pulumi.Input or pulumi.Output"). So the schema is
// corrected here: an input whose Go field cannot take an Output is marked plain, and the SDK types
// it as a plain value, which a program's type check holds to.

// componentArgs are the Go inputs of each component, by token.
var componentArgs = map[string]reflect.Type{
	"github-runners:index:Controller": reflect.TypeFor[ControllerArgs](),
	"github-runners:index:Pool":       reflect.TypeFor[PoolArgs](),
	"github-runners:index:GateAgent":  reflect.TypeFor[GateAgentArgs](),
}

// withPlainInputs serves the schema with plain marked where the Go field is plain.
func withPlainInputs(prov p.Provider) p.Provider {
	getSchema := prov.GetSchema
	prov.GetSchema = func(ctx context.Context, req p.GetSchemaRequest) (p.GetSchemaResponse, error) {
		resp, err := getSchema(ctx, req)
		if err != nil {
			return resp, err
		}
		resp.Schema, err = markPlainInputs(resp.Schema)
		return resp, err
	}
	return prov
}

func markPlainInputs(raw string) (string, error) {
	var spec schema.PackageSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return "", err
	}
	for token, args := range componentArgs {
		res, ok := spec.Resources[token]
		if !ok {
			return "", fmt.Errorf("schema has no component %s", token)
		}
		if err := markPlainProperties(&spec, args, res.InputProperties); err != nil {
			return "", fmt.Errorf("%s: %w", token, err)
		}
		spec.Resources[token] = res
	}
	out, err := json.Marshal(spec)
	return string(out), err
}

// takesOutput reports whether a Go input field accepts an Output.
func takesOutput(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Implements(reflect.TypeFor[pulumi.Input]())
}

// markPlainProperties marks plain every property of a Go struct that cannot take an Output, and
// does the same inside the object types those properties reach.
func markPlainProperties(spec *schema.PackageSpec, goType reflect.Type, props map[string]schema.PropertySpec) error {
	for _, field := range reflect.VisibleFields(goType) {
		name, _, _ := strings.Cut(field.Tag.Get("pulumi"), ",")
		prop, ok := props[name]
		if name == "" || !ok {
			continue
		}
		if takesOutput(field.Type) {
			continue
		}
		setPlain(&prop.TypeSpec)
		props[name] = prop
		if err := markPlainType(spec, field.Type, prop.TypeSpec); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// setPlain marks a type, and the elements of a plain array or map, plain.
func setPlain(t *schema.TypeSpec) {
	t.Plain = true
	if t.Items != nil {
		setPlain(t.Items)
	}
	if t.AdditionalProperties != nil {
		setPlain(t.AdditionalProperties)
	}
}

// markPlainType follows a plain field to the object type it holds, through pointers, arrays and
// maps, and marks that type's plain fields.
func markPlainType(spec *schema.PackageSpec, goType reflect.Type, t schema.TypeSpec) error {
	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	switch {
	case t.Items != nil:
		return markPlainType(spec, goType.Elem(), *t.Items)
	case t.AdditionalProperties != nil:
		return markPlainType(spec, goType.Elem(), *t.AdditionalProperties)
	case strings.HasPrefix(t.Ref, "#/types/"):
		token := strings.TrimPrefix(t.Ref, "#/types/")
		object, ok := spec.Types[token]
		if !ok {
			return fmt.Errorf("schema has no type %s", token)
		}
		if err := markPlainProperties(spec, goType, object.Properties); err != nil {
			return fmt.Errorf("%s: %w", token, err)
		}
		spec.Types[token] = object
	}
	return nil
}

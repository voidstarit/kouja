package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

//go:embed client.go.tmpl
var clientTemplate string

var pathParameterPattern = regexp.MustCompile(`\{([^}=]+)(?:=[^}]*)?\}`)

type templateData struct {
	ProtoImport string
	Sources     string
	Methods     []string
}

func main() {
	protogen.Options{}.Run(generate)
}

func generate(plugin *protogen.Plugin) error {
	plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
	data, err := collect(plugin)
	if err != nil {
		return err
	}
	if data == nil {
		return nil
	}
	parsed, err := template.New("client").Parse(clientTemplate)
	if err != nil {
		return fmt.Errorf("parse client template: %w", err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, data); err != nil {
		return fmt.Errorf("execute client template: %w", err)
	}
	generated := plugin.NewGeneratedFile("client/client.gen.go", "github.com/voidstarit/kouja/gen/client")
	generated.P(strings.TrimSpace(output.String()))
	return nil
}

func collect(plugin *protogen.Plugin) (*templateData, error) {
	var protoImport protogen.GoImportPath
	var sources []string
	var methods []string
	seen := make(map[string]bool)
	for _, file := range plugin.Files {
		if !file.Generate {
			continue
		}
		if protoImport == "" {
			protoImport = file.GoImportPath
		}
		if file.GoImportPath != protoImport {
			return nil, fmt.Errorf("all generated services must share one Go package: %s and %s", protoImport, file.GoImportPath)
		}
		sources = append(sources, file.Desc.Path())
		for _, service := range file.Services {
			for _, method := range service.Methods {
				rule := httpRule(method)
				if rule == nil {
					continue
				}
				if seen[method.GoName] {
					return nil, fmt.Errorf("duplicate client method %s", method.GoName)
				}
				seen[method.GoName] = true
				code, err := generateMethod(service, method, rule)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", service.Desc.FullName(), method.Desc.Name(), err)
				}
				methods = append(methods, code)
			}
		}
	}
	if len(methods) == 0 {
		return nil, nil
	}
	return &templateData{
		ProtoImport: string(protoImport),
		Sources:     strings.Join(sources, ", "),
		Methods:     methods,
	}, nil
}

func httpRule(method *protogen.Method) *annotations.HttpRule {
	options, ok := method.Desc.Options().(*descriptorpb.MethodOptions)
	if !ok || options == nil {
		return nil
	}
	rule, _ := proto.GetExtension(options, annotations.E_Http).(*annotations.HttpRule)
	if rule == nil || rule.GetPattern() == nil {
		return nil
	}
	return rule
}

func generateMethod(service *protogen.Service, method *protogen.Method, rule *annotations.HttpRule) (string, error) {
	httpMethod, path, body, err := ruleDetails(rule)
	if err != nil {
		return "", err
	}
	pathNames := pathParameterNames(path)
	pathExpression, pathPrelude, err := buildPath(path, method.Input)
	if err != nil {
		return "", err
	}
	queryCode, queryUsesRequest, err := buildQuery(method.Input, pathNames, body)
	if err != nil {
		return "", err
	}
	usesRequest := body == "*" || len(pathNames) > 0 || queryUsesRequest
	parameter := "request"
	if !usesRequest {
		parameter = "_"
	}

	var code strings.Builder
	fmt.Fprintf(&code, "// %s calls the %s RPC.\n", method.GoName, service.GoName)
	fmt.Fprintf(
		&code,
		"func (client *HTTP) %s(ctx context.Context, %s *pb.%s) (*pb.%s, error) {\n",
		method.GoName,
		parameter,
		method.Input.GoIdent.GoName,
		method.Output.GoIdent.GoName,
	)
	if usesRequest {
		fmt.Fprintf(&code, "\tif request == nil {\n\t\trequest = &pb.%s{}\n\t}\n", method.Input.GoIdent.GoName)
	}
	code.WriteString(pathPrelude)
	queryArgument := "nil"
	if queryCode != "" {
		code.WriteString("\tquery := url.Values{}\n")
		code.WriteString(queryCode)
		queryArgument = "query"
	}
	fmt.Fprintf(&code, "\tresponse := &pb.%s{}\n", method.Output.GoIdent.GoName)
	requestArgument := "nil"
	if body == "*" {
		requestArgument = "request"
	}
	fmt.Fprintf(
		&code,
		"\tif err := client.do(ctx, %s, %s, %s, %s, response); err != nil {\n\t\treturn nil, err\n\t}\n",
		methodConstant(httpMethod),
		pathExpression,
		queryArgument,
		requestArgument,
	)
	code.WriteString("\treturn response, nil\n}\n")
	return code.String(), nil
}

func ruleDetails(rule *annotations.HttpRule) (string, string, string, error) {
	var method, path string
	switch pattern := rule.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		method, path = http.MethodGet, pattern.Get
	case *annotations.HttpRule_Post:
		method, path = http.MethodPost, pattern.Post
	case *annotations.HttpRule_Put:
		method, path = http.MethodPut, pattern.Put
	case *annotations.HttpRule_Delete:
		method, path = http.MethodDelete, pattern.Delete
	case *annotations.HttpRule_Patch:
		method, path = http.MethodPatch, pattern.Patch
	default:
		return "", "", "", fmt.Errorf("unsupported HTTP pattern %T", pattern)
	}
	body := rule.GetBody()
	if body != "" && body != "*" {
		return "", "", "", fmt.Errorf("unsupported HTTP body %q", body)
	}
	return method, path, body, nil
}

func methodConstant(method string) string {
	switch method {
	case http.MethodGet:
		return "http.MethodGet"
	case http.MethodPost:
		return "http.MethodPost"
	case http.MethodPut:
		return "http.MethodPut"
	case http.MethodDelete:
		return "http.MethodDelete"
	case http.MethodPatch:
		return "http.MethodPatch"
	default:
		return strconv.Quote(method)
	}
}

func pathParameterNames(path string) []string {
	matches := pathParameterPattern.FindAllStringSubmatch(path, -1)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

func buildPath(path string, message *protogen.Message) (string, string, error) {
	matches := pathParameterPattern.FindAllStringSubmatchIndex(path, -1)
	if len(matches) == 0 {
		return strconv.Quote(path), "", nil
	}
	var parts []string
	var prelude strings.Builder
	last := 0
	for index, match := range matches {
		name := path[match[2]:match[3]]
		field := findField(message, name)
		if field == nil {
			return "", "", fmt.Errorf("path parameter %q not found in %s", name, message.Desc.FullName())
		}
		if literal := path[last:match[0]]; literal != "" {
			parts = append(parts, strconv.Quote(literal))
		}
		variable := fmt.Sprintf("pathValue%d", index)
		fmt.Fprintf(&prelude, "\t%s, err := pathParam(%q, request.Get%s())\n", variable, name, field.GoName)
		prelude.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
		parts = append(parts, variable)
		last = match[1]
	}
	if literal := path[last:]; literal != "" {
		parts = append(parts, strconv.Quote(literal))
	}
	return strings.Join(parts, " + "), prelude.String(), nil
}

func buildQuery(message *protogen.Message, pathNames []string, body string) (string, bool, error) {
	if body == "*" {
		return "", false, nil
	}
	pathSet := make(map[string]bool, len(pathNames))
	for _, name := range pathNames {
		pathSet[name] = true
	}
	var code strings.Builder
	usesRequest := false
	for _, field := range message.Fields {
		if pathSet[string(field.Desc.Name())] {
			continue
		}
		if field.Desc.HasPresence() {
			return "", false, fmt.Errorf("optional query parameter %q is not supported", field.Desc.FullName())
		}
		if field.Desc.IsList() || field.Desc.IsMap() {
			return "", false, fmt.Errorf("repeated query parameter %q is not supported", field.Desc.FullName())
		}
		name := field.Desc.JSONName()
		getter := "request.Get" + field.GoName + "()"
		switch field.Desc.Kind() {
		case protoreflect.StringKind:
			fmt.Fprintf(&code, "\taddString(query, %q, %s)\n", name, getter)
		case protoreflect.BoolKind:
			fmt.Fprintf(&code, "\taddBool(query, %q, %s)\n", name, getter)
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			fmt.Fprintf(&code, "\taddInt32(query, %q, %s)\n", name, getter)
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			fmt.Fprintf(&code, "\taddInt64(query, %q, %s)\n", name, getter)
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
			fmt.Fprintf(&code, "\taddUint32(query, %q, %s)\n", name, getter)
		case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			fmt.Fprintf(&code, "\taddUint64(query, %q, %s)\n", name, getter)
		case protoreflect.FloatKind:
			fmt.Fprintf(&code, "\taddFloat32(query, %q, %s)\n", name, getter)
		case protoreflect.DoubleKind:
			fmt.Fprintf(&code, "\taddFloat64(query, %q, %s)\n", name, getter)
		case protoreflect.EnumKind:
			fmt.Fprintf(&code, "\tif value := %s; value != 0 {\n\t\tquery.Set(%q, value.String())\n\t}\n", getter, name)
		default:
			return "", false, fmt.Errorf("unsupported query parameter type %s for %q", field.Desc.Kind(), field.Desc.FullName())
		}
		usesRequest = true
	}
	return code.String(), usesRequest, nil
}

func findField(message *protogen.Message, name string) *protogen.Field {
	for _, field := range message.Fields {
		if string(field.Desc.Name()) == name {
			return field
		}
	}
	return nil
}

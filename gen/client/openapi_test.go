package client

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func bddTestOpenAPISpec(t testing.TB) {
	payload, err := os.ReadFile("../openapi/openapi.swagger.json")
	if err != nil {
		t.Fatalf("read OpenAPI spec: %v", err)
	}
	var spec struct {
		Swagger string `json:"swagger"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(payload, &spec); err != nil {
		t.Fatalf("decode OpenAPI spec: %v", err)
	}
	if spec.Swagger != "2.0" {
		t.Fatalf("swagger = %q, want 2.0", spec.Swagger)
	}
	if spec.Info.Title != "kouja API" || spec.Info.Version != "v1" {
		t.Fatalf("info = %#v", spec.Info)
	}

	actual := make(map[string]map[string]string, len(spec.Paths))
	for path, operations := range spec.Paths {
		actual[path] = make(map[string]string, len(operations))
		for method, operation := range operations {
			actual[path][method] = operation.OperationID
		}
	}
	expected := map[string]map[string]string{
		"/v1/admin/drift/{volumeId}:resolve":       {"post": "ResolveDrift"},
		"/v1/admin/grants":                         {"post": "GrantCredential"},
		"/v1/admin/grants/{volumeId}/{nodeId}":     {"delete": "RevokeGrant"},
		"/v1/admin/import":                         {"post": "ImportVolume"},
		"/v1/admin/orphans":                        {"get": "ListOrphans"},
		"/v1/admin/ownership:transfer":             {"post": "TransferOwnership"},
		"/v1/admin/vgs":                            {"get": "ListVGs", "post": "RegisterVG"},
		"/v1/admin/vgs/{vgName}":                   {"delete": "DeregisterVG"},
		"/v1/admin/volumes/{volumeId}":             {"delete": "AdminDeleteVolume"},
		"/v1/admin/volumes/{volumeId}:wipe":        {"post": "WipeVolume"},
		"/v1/capacity":                             {"get": "GetCapacity"},
		"/v1/info":                                 {"get": "GetInfo"},
		"/v1/storage-classes":                      {"get": "ListStorageClasses"},
		"/v1/volumes":                              {"get": "ListVolumes", "post": "CreateVolume"},
		"/v1/volumes/{volumeId}":                   {"delete": "DeleteVolume", "get": "GetVolume"},
		"/v1/volumes/{volumeId}/credential":        {"get": "GetCredential"},
		"/v1/volumes/{volumeId}:export":            {"post": "ExportVolume"},
		"/v1/volumes/{volumeId}:resize":            {"post": "ResizeVolume"},
		"/v1/volumes/{volumeId}:rotate-credential": {"post": "RotateCredential"},
		"/v1/volumes/{volumeId}:unexport":          {"post": "UnexportVolume"},
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("OpenAPI operations = %#v, want %#v", actual, expected)
	}
}

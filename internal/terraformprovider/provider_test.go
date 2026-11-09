package terraformprovider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderMetadataSchemaAndResources(t *testing.T) {
	instance := New("test-version")()
	var metadata provider.MetadataResponse
	instance.Metadata(context.Background(), provider.MetadataRequest{}, &metadata)
	if metadata.TypeName != "kouja" || metadata.Version != "test-version" {
		t.Fatalf("metadata = %#v", metadata)
	}
	var schemaResponse provider.SchemaResponse
	instance.Schema(context.Background(), provider.SchemaRequest{}, &schemaResponse)
	for _, name := range []string{"endpoint", "server_ca_pem", "server_ca_file", "client_cert_pem", "client_cert_file", "client_key_pem", "client_key_file", "server_name", "timeout"} {
		if _, ok := schemaResponse.Schema.Attributes[name]; !ok {
			t.Fatalf("schema missing %s", name)
		}
	}
	if len(instance.Resources(context.Background())) != 3 {
		t.Fatalf("resources = %d, want 3", len(instance.Resources(context.Background())))
	}
	if len(instance.DataSources(context.Background())) != 1 {
		t.Fatalf("data sources = %d, want 1", len(instance.DataSources(context.Background())))
	}
}

func TestResolveMaterialRejectsBothForms(t *testing.T) {
	_, err := resolveMaterial(types.StringValue("pem"), types.StringValue("/tmp/ca.pem"), "KOUJA_TEST_PEM", "KOUJA_TEST_FILE", true)
	if err == nil {
		t.Fatal("expected an error when both PEM and file are set")
	}
}

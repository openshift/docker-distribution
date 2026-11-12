//nolint:staticcheck // Schema 1 compatibility is intentionally retained.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/manifest/schema1"
	"github.com/distribution/distribution/v3/manifest/schema2"
	"github.com/distribution/reference"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestSchema1ManifestAPI(t *testing.T) {
	config := configuration.Configuration{
		Storage: configuration.Storage{
			"inmemory": configuration.Parameters{},
			"maintenance": configuration.Parameters{"uploadpurging": map[any]any{
				"enabled": false,
			}},
		},
	}
	config.Compatibility.Schema1.Enabled = true
	config.HTTP.Headers = headerConfig

	ctx := context.Background()
	app := NewApp(ctx, &config)
	named, err := reference.WithName("test/schema1")
	if err != nil {
		t.Fatal(err)
	}
	repository, err := app.registry.Repository(ctx, named)
	if err != nil {
		t.Fatal(err)
	}
	blobs := repository.Blobs(ctx)

	configJSON := []byte(`{
		"architecture":"amd64",
		"history":[{"created_by":"schema1 compatibility test"}],
		"rootfs":{"type":"layers","diff_ids":["sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"]}
	}`)
	configDescriptor, err := blobs.Put(ctx, schema2.MediaTypeImageConfig, configJSON)
	if err != nil {
		t.Fatal(err)
	}
	layerDescriptor, err := blobs.Put(ctx, schema2.MediaTypeLayer, []byte("schema1 compatibility layer"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := schema2.FromStruct(schema2.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: schema2.MediaTypeManifest,
		Config:    configDescriptor,
		Layers:    []v1.Descriptor{layerDescriptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := repository.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := manifests.Put(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Tags(ctx).Tag(ctx, "schema2", v1.Descriptor{Digest: manifestDigest, MediaType: schema2.MediaTypeManifest}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v2/test/schema1/manifests/schema2", nil)
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	checkResponse(t, "convert schema2 manifest for schema1 client", response, http.StatusOK)
	if contentType := response.Header.Get("Content-Type"); contentType != schema1.MediaTypeSignedManifest {
		t.Fatalf("converted manifest Content-Type = %q, want %q", contentType, schema1.MediaTypeSignedManifest)
	}

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read converted schema1 manifest: %v", err)
	}
	var converted schema1.SignedManifest
	if err := json.Unmarshal(payload, &converted); err != nil {
		t.Fatalf("decode converted schema1 manifest: %v", err)
	}
	if len(converted.Canonical) == 0 {
		t.Fatal("converted schema1 manifest has no canonical payload")
	}

	request = httptest.NewRequest(http.MethodPut, "/v2/test/schema1/manifests/schema1", bytes.NewReader(payload))
	request.Header.Set("Content-Type", schema1.MediaTypeSignedManifest)
	recorder = httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	putResponse := recorder.Result()
	defer putResponse.Body.Close()
	checkResponse(t, "put converted schema1 manifest", putResponse, http.StatusCreated)
}

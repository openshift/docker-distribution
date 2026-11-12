//nolint:staticcheck // Schema 1 compatibility is intentionally retained.
package storage

import (
	"bytes"
	"context"
	"testing"

	"github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/manifest/schema1"
	"github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	"github.com/distribution/reference"
	"github.com/docker/libtrust"
)

func TestSchema1ManifestStorage(t *testing.T) {
	ctx := context.Background()
	driver := inmemory.New()
	key, err := libtrust.GenerateECP256PrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	namespace, err := NewRegistry(ctx, driver, Schema1SigningKey(key), EnableSchema1)
	if err != nil {
		t.Fatal(err)
	}
	named, err := reference.WithName("test/schema1")
	if err != nil {
		t.Fatal(err)
	}
	repository, err := namespace.Repository(ctx, named)
	if err != nil {
		t.Fatal(err)
	}

	layer, err := repository.Blobs(ctx).Put(ctx, schema1.MediaTypeManifestLayer, []byte("schema1 layer"))
	if err != nil {
		t.Fatal(err)
	}
	unsigned := &schema1.Manifest{
		Versioned: schema1.SchemaVersion,
		Name:      named.Name(),
		Tag:       "latest",
		FSLayers:  []schema1.FSLayer{{BlobSum: layer.Digest}},
		History:   []schema1.History{{V1Compatibility: `{}`}},
	}
	signed, err := schema1.Sign(unsigned, key)
	if err != nil {
		t.Fatal(err)
	}

	manifests, err := repository.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifests.Put(ctx, signed)
	if err != nil {
		t.Fatalf("put schema1 manifest: %v", err)
	}
	stored, err := manifests.Get(ctx, digest)
	if err != nil {
		t.Fatalf("get schema1 manifest: %v", err)
	}
	storedSchema1, ok := stored.(*schema1.SignedManifest)
	if !ok {
		t.Fatalf("stored manifest has type %T", stored)
	}
	if !bytes.Equal(storedSchema1.Canonical, signed.Canonical) {
		t.Fatal("stored schema1 canonical payload changed")
	}
	if _, err := schema1.Verify(storedSchema1); err != nil {
		t.Fatalf("verify stored schema1 manifest: %v", err)
	}

	disabledNamespace, err := NewRegistry(ctx, driver, Schema1SigningKey(key))
	if err != nil {
		t.Fatal(err)
	}
	disabledRepository, err := disabledNamespace.Repository(ctx, named)
	if err != nil {
		t.Fatal(err)
	}
	disabledManifests, err := disabledRepository.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabledManifests.Put(ctx, signed); err != distribution.ErrSchemaV1Unsupported {
		t.Fatalf("schema1 disabled put error = %v, want %v", err, distribution.ErrSchemaV1Unsupported)
	}
}

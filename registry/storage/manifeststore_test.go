package storage

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/manifest"
	"github.com/distribution/distribution/v3/manifest/manifestlist"
	"github.com/distribution/distribution/v3/manifest/ocischema"
	"github.com/distribution/distribution/v3/manifest/schema1" //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	"github.com/distribution/distribution/v3/reference"
	"github.com/distribution/distribution/v3/registry/storage/cache/memory"
	"github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	"github.com/distribution/distribution/v3/testutil"
	"github.com/docker/libtrust"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type manifestStoreTestEnv struct {
	ctx        context.Context
	driver     driver.StorageDriver
	registry   distribution.Namespace
	repository distribution.Repository
	name       reference.Named
	tag        string
}

func newManifestStoreTestEnv(t *testing.T, name reference.Named, tag string, options ...RegistryOption) *manifestStoreTestEnv {
	ctx := context.Background()
	drvr := inmemory.New()
	registry, err := NewRegistry(ctx, drvr, options...)
	if err != nil {
		t.Fatalf("error creating registry: %v", err)
	}

	repo, err := registry.Repository(ctx, name)
	if err != nil {
		t.Fatalf("unexpected error getting repo: %v", err)
	}

	return &manifestStoreTestEnv{
		ctx:        ctx,
		driver:     drvr,
		registry:   registry,
		repository: repo,
		name:       name,
		tag:        tag,
	}
}

func TestManifestStorage(t *testing.T) {
	k, err := libtrust.GenerateECP256PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	testManifestStorage(t, true, BlobDescriptorCacheProvider(memory.NewInMemoryBlobDescriptorCacheProvider(memory.UnlimitedSize)), EnableDelete, EnableRedirect, Schema1SigningKey(k), EnableSchema1, EnableValidateImageIndexImagesExist)
}

func TestManifestStorageV1Unsupported(t *testing.T) {
	k, err := libtrust.GenerateECP256PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	testManifestStorage(t, false, BlobDescriptorCacheProvider(memory.NewInMemoryBlobDescriptorCacheProvider(memory.UnlimitedSize)), EnableDelete, EnableRedirect, Schema1SigningKey(k))
}

func testManifestStorage(t *testing.T, schema1Enabled bool, options ...RegistryOption) {
	repoName, _ := reference.WithName("foo/bar")
	env := newManifestStoreTestEnv(t, repoName, "thetag", options...)
	ctx := context.Background()
	ms, err := env.repository.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	m := schema1.Manifest{ //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
		Versioned: manifest.Versioned{
			SchemaVersion: 1,
		},
		Name: env.name.Name(),
		Tag:  env.tag,
	}

	// Build up some test layers and add them to the manifest, saving the
	// readseekers for upload later.
	testLayers := map[digest.Digest]io.ReadSeeker{}
	for i := 0; i < 2; i++ {
		rs, dgst, err := testutil.CreateRandomTarFile()
		if err != nil {
			t.Fatalf("unexpected error generating test layer file")
		}

		testLayers[dgst] = rs
		m.FSLayers = append(m.FSLayers, schema1.FSLayer{ //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
			BlobSum: dgst,
		})
		m.History = append(m.History, schema1.History{ //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
			V1Compatibility: "",
		})

	}

	pk, err := libtrust.GenerateECP256PrivateKey()
	if err != nil {
		t.Fatalf("unexpected error generating private key: %v", err)
	}

	sm, merr := schema1.Sign(&m, pk) //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if merr != nil {
		t.Fatalf("error signing manifest: %v", err)
	}

	_, err = ms.Put(ctx, sm)
	if err == nil {
		t.Fatalf("expected errors putting manifest with full verification")
	}

	// If schema1 is not enabled, do a short version of this test, just checking
	// if we get the right error when we Put
	if !schema1Enabled {
		if err != distribution.ErrSchemaV1Unsupported {
			t.Fatalf("got the wrong error when schema1 is disabled: %s", err)
		}
		return
	}

	switch err := err.(type) {
	case distribution.ErrManifestVerification:
		if len(err) != 2 {
			t.Fatalf("expected 2 verification errors: %#v", err)
		}

		for _, err := range err {
			if _, ok := err.(distribution.ErrManifestBlobUnknown); !ok {
				t.Fatalf("unexpected error type: %v", err)
			}
		}
	default:
		t.Fatalf("unexpected error verifying manifest: %v", err)
	}

	// Now, upload the layers that were missing!
	for dgst, rs := range testLayers {
		wr, err := env.repository.Blobs(env.ctx).Create(env.ctx)
		if err != nil {
			t.Fatalf("unexpected error creating test upload: %v", err)
		}

		if _, err := io.Copy(wr, rs); err != nil {
			t.Fatalf("unexpected error copying to upload: %v", err)
		}

		if _, err := wr.Commit(env.ctx, distribution.Descriptor{Digest: dgst}); err != nil {
			t.Fatalf("unexpected error finishing upload: %v", err)
		}
	}

	var manifestDigest digest.Digest
	if manifestDigest, err = ms.Put(ctx, sm); err != nil {
		t.Fatalf("unexpected error putting manifest: %v", err)
	}

	exists, err := ms.Exists(ctx, manifestDigest)
	if err != nil {
		t.Fatalf("unexpected error checking manifest existence: %#v", err)
	}

	if !exists {
		t.Fatalf("manifest should exist")
	}

	fromStore, err := ms.Get(ctx, manifestDigest)
	if err != nil {
		t.Fatalf("unexpected error fetching manifest: %v", err)
	}

	fetchedManifest, ok := fromStore.(*schema1.SignedManifest) //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if !ok {
		t.Fatalf("unexpected manifest type from signedstore")
	}

	if !bytes.Equal(fetchedManifest.Canonical, sm.Canonical) {
		t.Fatalf("fetched payload does not match original payload: %q != %q", fetchedManifest.Canonical, sm.Canonical)
	}

	_, pl, err := fetchedManifest.Payload() //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if err != nil {
		t.Fatalf("error getting payload %#v", err)
	}

	fetchedJWS, err := libtrust.ParsePrettySignature(pl, "signatures")
	if err != nil {
		t.Fatalf("unexpected error parsing jws: %v", err)
	}

	payload, err := fetchedJWS.Payload()
	if err != nil {
		t.Fatalf("unexpected error extracting payload: %v", err)
	}

	// Now that we have a payload, take a moment to check that the manifest is
	// return by the payload digest.

	dgst := digest.FromBytes(payload)
	exists, err = ms.Exists(ctx, dgst)
	if err != nil {
		t.Fatalf("error checking manifest existence by digest: %v", err)
	}

	if !exists {
		t.Fatalf("manifest %s should exist", dgst)
	}

	fetchedByDigest, err := ms.Get(ctx, dgst)
	if err != nil {
		t.Fatalf("unexpected error fetching manifest by digest: %v", err)
	}

	byDigestManifest, ok := fetchedByDigest.(*schema1.SignedManifest) //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if !ok {
		t.Fatalf("unexpected manifest type from signedstore")
	}

	if !bytes.Equal(byDigestManifest.Canonical, fetchedManifest.Canonical) {
		t.Fatalf("fetched manifest not equal: %q != %q", byDigestManifest.Canonical, fetchedManifest.Canonical)
	}

	sigs, err := fetchedJWS.Signatures()
	if err != nil {
		t.Fatalf("unable to extract signatures: %v", err)
	}

	if len(sigs) != 1 {
		t.Fatalf("unexpected number of signatures: %d != %d", len(sigs), 1)
	}

	// Now, push the same manifest with a different key
	pk2, err := libtrust.GenerateECP256PrivateKey()
	if err != nil {
		t.Fatalf("unexpected error generating private key: %v", err)
	}

	sm2, err := schema1.Sign(&m, pk2) //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if err != nil {
		t.Fatalf("unexpected error signing manifest: %v", err)
	}
	_, pl, err = sm2.Payload() //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if err != nil {
		t.Fatalf("error getting payload %#v", err)
	}

	jws2, err := libtrust.ParsePrettySignature(pl, "signatures")
	if err != nil {
		t.Fatalf("error parsing signature: %v", err)
	}

	sigs2, err := jws2.Signatures()
	if err != nil {
		t.Fatalf("unable to extract signatures: %v", err)
	}

	if len(sigs2) != 1 {
		t.Fatalf("unexpected number of signatures: %d != %d", len(sigs2), 1)
	}

	if manifestDigest, err = ms.Put(ctx, sm2); err != nil {
		t.Fatalf("unexpected error putting manifest: %v", err)
	}

	fromStore, err = ms.Get(ctx, manifestDigest)
	if err != nil {
		t.Fatalf("unexpected error fetching manifest: %v", err)
	}

	fetched, ok := fromStore.(*schema1.SignedManifest) //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if !ok {
		t.Fatalf("unexpected type from signed manifeststore : %T", fetched)
	}

	if _, err := schema1.Verify(fetched); err != nil { //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
		t.Fatalf("unexpected error verifying manifest: %v", err)
	}

	_, pl, err = fetched.Payload() //nolint:staticcheck // Ignore SA1019: "github.com/distribution/distribution/v3/manifest/schema1" is deprecated, as it's used for backward compatibility.
	if err != nil {
		t.Fatalf("error getting payload %#v", err)
	}

	receivedJWS, err := libtrust.ParsePrettySignature(pl, "signatures")
	if err != nil {
		t.Fatalf("unexpected error parsing jws: %v", err)
	}

	receivedPayload, err := receivedJWS.Payload()
	if err != nil {
		t.Fatalf("unexpected error extracting received payload: %v", err)
	}

	if !bytes.Equal(receivedPayload, payload) {
		t.Fatalf("payloads are not equal")
	}

	// Test deleting manifests
	err = ms.Delete(ctx, dgst)
	if err != nil {
		t.Fatalf("unexpected an error deleting manifest by digest: %v", err)
	}

	exists, err = ms.Exists(ctx, dgst)
	if err != nil {
		t.Fatalf("Error querying manifest existence")
	}
	if exists {
		t.Errorf("Deleted manifest should not exist")
	}

	deletedManifest, err := ms.Get(ctx, dgst)
	if err == nil {
		t.Errorf("Unexpected success getting deleted manifest")
	}
	switch err.(type) {
	case distribution.ErrManifestUnknownRevision:
		break
	default:
		t.Errorf("Unexpected error getting deleted manifest: %s", reflect.ValueOf(err).Type())
	}

	if deletedManifest != nil {
		t.Errorf("Deleted manifest get returned non-nil")
	}

	// Re-upload should restore manifest to a good state
	_, err = ms.Put(ctx, sm)
	if err != nil {
		t.Errorf("Error re-uploading deleted manifest")
	}

	exists, err = ms.Exists(ctx, dgst)
	if err != nil {
		t.Fatalf("Error querying manifest existence")
	}
	if !exists {
		t.Errorf("Restored manifest should exist")
	}

	deletedManifest, err = ms.Get(ctx, dgst)
	if err != nil {
		t.Errorf("Unexpected error getting manifest")
	}
	if deletedManifest == nil {
		t.Errorf("Deleted manifest get returned non-nil")
	}

	r, err := NewRegistry(ctx, env.driver, BlobDescriptorCacheProvider(memory.NewInMemoryBlobDescriptorCacheProvider(memory.UnlimitedSize)), EnableRedirect)
	if err != nil {
		t.Fatalf("error creating registry: %v", err)
	}
	repo, err := r.Repository(ctx, env.name)
	if err != nil {
		t.Fatalf("unexpected error getting repo: %v", err)
	}
	ms, err = repo.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = ms.Delete(ctx, dgst)
	if err == nil {
		t.Errorf("Unexpected success deleting while disabled")
	}
}

func TestOCIManifestStorage(t *testing.T) {
	testOCIManifestStorage(t, "includeMediaTypes=true", true)
	testOCIManifestStorage(t, "includeMediaTypes=false", false)
}

func testOCIManifestStorage(t *testing.T, testname string, includeMediaTypes bool) {
	var imageMediaType string
	var indexMediaType string
	if includeMediaTypes {
		imageMediaType = v1.MediaTypeImageManifest
		indexMediaType = v1.MediaTypeImageIndex
	} else {
		imageMediaType = ""
		indexMediaType = ""
	}

	repoName, _ := reference.WithName("foo/bar")
	env := newManifestStoreTestEnv(t, repoName, "thetag",
		BlobDescriptorCacheProvider(memory.NewInMemoryBlobDescriptorCacheProvider(memory.UnlimitedSize)),
		EnableDelete, EnableRedirect, EnableValidateImageIndexImagesExist)

	ctx := context.Background()
	ms, err := env.repository.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Build a manifest and store its layers in the registry

	blobStore := env.repository.Blobs(ctx)
	mfst, err := createRandomImage(t, testname, imageMediaType, blobStore)
	if err != nil {
		t.Fatalf("%s: unexpected error generating random image: %v", testname, err)
	}

	// create an image index

	platformSpec := &v1.Platform{
		Architecture: "atari2600",
		OS:           "CP/M",
	}

	mfstDescriptors := []distribution.Descriptor{
		createOciManifestDescriptor(t, testname, mfst, platformSpec),
	}

	imageIndex, err := ociIndexFromDesriptorsWithMediaType(mfstDescriptors, indexMediaType)
	if err != nil {
		t.Fatalf("%s: unexpected error creating image index: %v", testname, err)
	}

	_, err = ms.Put(ctx, imageIndex)
	if err == nil {
		t.Fatalf("%s: expected error putting image index without child manifests in the registry: %v", testname, err)
	}

	// Test for proper handling of SchemaVersion for the image

	if mfst.(*ocischema.DeserializedManifest).Manifest.SchemaVersion != 2 {
		t.Fatalf("%s: unexpected error generating default version for oci manifest", testname)
	}
	mfst.(*ocischema.DeserializedManifest).Manifest.SchemaVersion = 0

	var manifestDigest digest.Digest
	if manifestDigest, err = ms.Put(ctx, mfst); err != nil {
		if err.Error() != "unrecognized manifest schema version 0" {
			t.Fatalf("%s: unexpected error putting manifest: %v", testname, err)
		}
		mfst.(*ocischema.DeserializedManifest).Manifest.SchemaVersion = 2
		if manifestDigest, err = ms.Put(ctx, mfst); err != nil {
			t.Fatalf("%s: unexpected error putting manifest: %v", testname, err)
		}
	}

	// We can now push the index

	var indexDigest digest.Digest
	if indexDigest, err = ms.Put(ctx, imageIndex); err != nil {
		t.Fatalf("%s: unexpected error putting image index: %v", testname, err)
	}

	// Now check that we can retrieve the manifest

	fromStore, err := ms.Get(ctx, manifestDigest)
	if err != nil {
		t.Fatalf("%s: unexpected error fetching manifest: %v", testname, err)
	}

	fetchedManifest, ok := fromStore.(*ocischema.DeserializedManifest)
	if !ok {
		t.Fatalf("%s: unexpected type for fetched manifest", testname)
	}

	if fetchedManifest.MediaType != imageMediaType {
		t.Fatalf("%s: unexpected MediaType for result, %s", testname, fetchedManifest.MediaType)
	}

	if fetchedManifest.SchemaVersion != ocischema.SchemaVersion.SchemaVersion {
		t.Fatalf("%s: unexpected schema version for result, %d", testname, fetchedManifest.SchemaVersion)
	}

	payloadMediaType, _, err := fromStore.Payload()
	if err != nil {
		t.Fatalf("%s: error getting payload %v", testname, err)
	}

	if payloadMediaType != v1.MediaTypeImageManifest {
		t.Fatalf("%s: unexpected MediaType for manifest payload, %s", testname, payloadMediaType)
	}

	// and the image index

	fromStore, err = ms.Get(ctx, indexDigest)
	if err != nil {
		t.Fatalf("%s: unexpected error fetching image index: %v", testname, err)
	}

	fetchedIndex, ok := fromStore.(*manifestlist.DeserializedManifestList)
	if !ok {
		t.Fatalf("%s: unexpected type for fetched manifest", testname)
	}

	if fetchedIndex.MediaType != indexMediaType {
		t.Fatalf("%s: unexpected MediaType for result, %s", testname, fetchedIndex.MediaType)
	}

	payloadMediaType, _, err = fromStore.Payload()
	if err != nil {
		t.Fatalf("%s: error getting payload %v", testname, err)
	}

	if payloadMediaType != v1.MediaTypeImageIndex {
		t.Fatalf("%s: unexpected MediaType for index payload, %s", testname, payloadMediaType)
	}
}

func TestIndexManifestStorageWithoutImageCheck(t *testing.T) {
	imageMediaType := v1.MediaTypeImageManifest
	indexMediaType := v1.MediaTypeImageIndex

	repoName, _ := reference.WithName("foo/bar")
	env := newManifestStoreTestEnv(t, repoName, "thetag",
		BlobDescriptorCacheProvider(memory.NewInMemoryBlobDescriptorCacheProvider(memory.UnlimitedSize)),
		EnableDelete, EnableRedirect)

	ctx := context.Background()
	ms, err := env.repository.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Build a manifest and store its layers in the registry

	blobStore := env.repository.Blobs(ctx)
	manifest, err := createRandomImage(t, t.Name(), imageMediaType, blobStore)
	if err != nil {
		t.Fatalf("unexpected error generating random image: %v", err)
	}

	// create an image index

	ociPlatformSpec := &v1.Platform{
		Architecture: "atari2600",
		OS:           "CP/M",
	}

	ociManifestDescriptors := []distribution.Descriptor{
		createOciManifestDescriptor(t, t.Name(), manifest, ociPlatformSpec),
	}

	imageIndex, err := ociIndexFromDesriptorsWithMediaType(ociManifestDescriptors, indexMediaType)
	if err != nil {
		t.Fatalf("unexpected error creating image index: %v", err)
	}

	// We should be able to put the index without having put the image

	_, err = ms.Put(ctx, imageIndex)
	if err != nil {
		t.Fatalf("unexpected error putting sparse OCI image index: %v", err)
	}

	// same for a manifest list

	listPlatformSpec := &manifestlist.PlatformSpec{
		Architecture: "atari2600",
		OS:           "CP/M",
	}

	listManifestDescriptors := []manifestlist.ManifestDescriptor{
		createManifestListDescriptor(t, t.Name(), manifest, listPlatformSpec),
	}

	list, err := manifestlist.FromDescriptors(listManifestDescriptors)
	if err != nil {
		t.Fatalf("unexpected error creating manifest list: %v", err)
	}

	// We should be able to put the list without having put the image

	_, err = ms.Put(ctx, list)
	if err != nil {
		t.Fatalf("unexpected error putting sparse manifest list: %v", err)
	}
}

func TestIndexManifestStorageWithSelectivePlatforms(t *testing.T) {
	imageMediaType := v1.MediaTypeImageManifest
	indexMediaType := v1.MediaTypeImageIndex

	repoName, _ := reference.WithName("foo/bar")
	env := newManifestStoreTestEnv(t, repoName, "thetag",
		BlobDescriptorCacheProvider(memory.NewInMemoryBlobDescriptorCacheProvider(memory.UnlimitedSize)),
		EnableDelete, EnableRedirect, EnableValidateImageIndexImagesExist,
		AddValidateImageIndexImagesExistPlatform("amd64", "linux"))

	ctx := context.Background()
	ms, err := env.repository.Manifests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Build a manifests their layers in the registry

	blobStore := env.repository.Blobs(ctx)
	amdManifest, err := createRandomImage(t, t.Name(), imageMediaType, blobStore)
	if err != nil {
		t.Fatalf("%s: unexpected error generating random image: %v", t.Name(), err)
	}
	armManifest, err := createRandomImage(t, t.Name(), imageMediaType, blobStore)
	if err != nil {
		t.Fatalf("%s: unexpected error generating random image: %v", t.Name(), err)
	}
	atariManifest, err := createRandomImage(t, t.Name(), imageMediaType, blobStore)
	if err != nil {
		t.Fatalf("%s: unexpected error generating random image: %v", t.Name(), err)
	}

	// create an image index

	amdPlatformSpec := &v1.Platform{
		Architecture: "amd64",
		OS:           "linux",
	}
	armPlatformSpec := &v1.Platform{
		Architecture: "arm",
		OS:           "plan9",
	}
	atariPlatformSpec := &v1.Platform{
		Architecture: "atari2600",
		OS:           "CP/M",
	}

	manifestDescriptors := []distribution.Descriptor{
		createOciManifestDescriptor(t, t.Name(), amdManifest, amdPlatformSpec),
		createOciManifestDescriptor(t, t.Name(), armManifest, armPlatformSpec),
		createOciManifestDescriptor(t, t.Name(), atariManifest, atariPlatformSpec),
	}

	imageIndex, err := ociIndexFromDesriptorsWithMediaType(manifestDescriptors, indexMediaType)
	if err != nil {
		t.Fatalf("unexpected error creating image index: %v", err)
	}

	// Test we can't push with no image manifests existing in the registry

	_, err = ms.Put(ctx, imageIndex)
	if err == nil {
		t.Fatalf("expected error putting image index without existing images: %v", err)
	}

	// Test we can't push with a manifest but not the right one

	_, err = ms.Put(ctx, atariManifest)
	if err != nil {
		t.Fatalf("unexpected error putting manifest: %v", err)
	}

	_, err = ms.Put(ctx, imageIndex)
	if err == nil {
		t.Fatalf("expected error putting image index without correct existing images: %v", err)
	}

	// Test we can push with the right manifest

	_, err = ms.Put(ctx, amdManifest)
	if err != nil {
		t.Fatalf("unexpected error putting manifest: %v", err)
	}

	_, err = ms.Put(ctx, imageIndex)
	if err != nil {
		t.Fatalf("unexpected error putting image index: %v", err)
	}
}

// createRandomImage builds an image manifest and store it and its layers in the registry
func createRandomImage(t *testing.T, testname string, imageMediaType string, blobStore distribution.BlobStore) (distribution.Manifest, error) {
	builder := ocischema.NewManifestBuilder(blobStore, []byte{}, map[string]string{})
	err := builder.(*ocischema.Builder).SetMediaType(imageMediaType)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Add some layers
	for i := 0; i < 2; i++ {
		rs, dgst, err := testutil.CreateRandomTarFile()
		if err != nil {
			t.Fatalf("%s: unexpected error generating test layer file", testname)
		}

		wr, err := blobStore.Create(ctx)
		if err != nil {
			t.Fatalf("%s: unexpected error creating test upload: %v", testname, err)
		}

		if _, err := io.Copy(wr, rs); err != nil {
			t.Fatalf("%s: unexpected error copying to upload: %v", testname, err)
		}

		if _, err := wr.Commit(ctx, distribution.Descriptor{Digest: dgst}); err != nil {
			t.Fatalf("%s: unexpected error finishing upload: %v", testname, err)
		}

		if err := builder.AppendReference(distribution.Descriptor{Digest: dgst, MediaType: v1.MediaTypeImageLayer}); err != nil {
			t.Fatalf("%s unexpected error appending references: %v", testname, err)
		}
	}

	return builder.Build(ctx)
}

// createOciManifestDescriptor builds a manifest descriptor from a manifest and a platform descriptor
func createOciManifestDescriptor(t *testing.T, testname string, manifest distribution.Manifest, platformSpec *v1.Platform) distribution.Descriptor {
	manifestMediaType, manifestPayload, err := manifest.Payload()
	if err != nil {
		t.Fatalf("%s: unexpected error getting manifest payload: %v", testname, err)
	}
	manifestDigest := digest.FromBytes(manifestPayload)

	return distribution.Descriptor{
		Digest:    manifestDigest,
		Size:      int64(len(manifestPayload)),
		MediaType: manifestMediaType,
		Platform: &v1.Platform{
			Architecture: platformSpec.Architecture,
			OS:           platformSpec.OS,
		},
	}
}

// createManifestListDescriptor builds a manifest descriptor from a manifest and a platform descriptor
func createManifestListDescriptor(t *testing.T, testname string, manifest distribution.Manifest, platformSpec *manifestlist.PlatformSpec) manifestlist.ManifestDescriptor {
	manifestMediaType, manifestPayload, err := manifest.Payload()
	if err != nil {
		t.Fatalf("%s: unexpected error getting manifest payload: %v", testname, err)
	}
	manifestDigest := digest.FromBytes(manifestPayload)

	return manifestlist.ManifestDescriptor{
		Descriptor: distribution.Descriptor{
			Digest:    manifestDigest,
			Size:      int64(len(manifestPayload)),
			MediaType: manifestMediaType,
		},
		Platform: manifestlist.PlatformSpec{
			Architecture: platformSpec.Architecture,
			OS:           platformSpec.OS,
		},
	}
}

// TestLinkPathFuncs ensures that the link path functions behavior are locked
// down and implemented as expected.
func TestLinkPathFuncs(t *testing.T) {
	for _, testcase := range []struct {
		repo       string
		digest     digest.Digest
		linkPathFn linkPathFunc
		expected   string
	}{
		{
			repo:       "foo/bar",
			digest:     "sha256:deadbeaf98fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			linkPathFn: blobLinkPath,
			expected:   "/docker/registry/v2/repositories/foo/bar/_layers/sha256/deadbeaf98fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855/link",
		},
		{
			repo:       "foo/bar",
			digest:     "sha256:deadbeaf98fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			linkPathFn: manifestRevisionLinkPath,
			expected:   "/docker/registry/v2/repositories/foo/bar/_manifests/revisions/sha256/deadbeaf98fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855/link",
		},
	} {
		p, err := testcase.linkPathFn(testcase.repo, testcase.digest)
		if err != nil {
			t.Fatalf("unexpected error calling linkPathFn(pm, %q, %q): %v", testcase.repo, testcase.digest, err)
		}

		if p != testcase.expected {
			t.Fatalf("incorrect path returned: %q != %q", p, testcase.expected)
		}
	}
}

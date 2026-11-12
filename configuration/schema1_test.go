package configuration

import (
	"bytes"
	"testing"
)

func TestSchema1CompatibilityConfiguration(t *testing.T) {
	config, err := Parse(bytes.NewBufferString(`
version: 0.1
storage: inmemory
compatibility:
  schema1:
    signingkeyfile: /etc/distribution/schema1.key
    enabled: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if !config.Compatibility.Schema1.Enabled {
		t.Fatal("schema1 compatibility is not enabled")
	}
	if got, want := config.Compatibility.Schema1.TrustKey, "/etc/distribution/schema1.key"; got != want {
		t.Fatalf("schema1 signing key = %q, want %q", got, want)
	}
}

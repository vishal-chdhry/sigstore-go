package policy

import (
	"testing"

	"github.com/github/sigstore-verifier/pkg/testing/ca"
	"github.com/stretchr/testify/assert"
)

func TestTlogPolicy(t *testing.T) {
	virtualSigstore, err := ca.NewVirtualSigstore()
	assert.NoError(t, err)

	policy := NewArtifactTransparencyLogPolicy(virtualSigstore, 1)
	statement := []byte(`{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"customFoo","subject":[{"name":"subject","digest":{"sha256":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}}],"predicate":{}}`)
	entity, err := virtualSigstore.Attest("foo@fighters.com", "issuer", statement)
	assert.NoError(t, err)

	err = policy.VerifyPolicy(entity)
	assert.NoError(t, err)

	virtualSigstore2, err := ca.NewVirtualSigstore()
	assert.NoError(t, err)

	policy2 := NewArtifactTransparencyLogPolicy(virtualSigstore2, 1)
	err = policy2.VerifyPolicy(entity)
	assert.Error(t, err) // different sigstore instance should fail to verify
}
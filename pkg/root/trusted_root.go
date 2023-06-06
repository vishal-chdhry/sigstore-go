package root

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	prototrustroot "github.com/sigstore/protobuf-specs/gen/pb-go/trustroot/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const TrustedRootMediaType01 = "application/vnd.dev.sigstore.trustedroot+json;version=0.1"

type TrustedRoot interface {
	TSACertificateAuthorities() []CertificateAuthority
	FulcioCertificateAuthorities() []CertificateAuthority
	TlogVerifiers() map[string]*TlogVerifier
}

type ParsedTrustedRoot struct {
	trustedRoot           *prototrustroot.TrustedRoot
	tlogVerifiers         map[string]*TlogVerifier
	fulcioCertAuthorities []CertificateAuthority
	tsaCertAuthorities    []CertificateAuthority
}

type CertificateAuthority struct {
	Root                *x509.Certificate
	Intermediates       []*x509.Certificate
	Leaf                *x509.Certificate
	ValidityPeriodStart time.Time
	ValidityPeriodEnd   time.Time
}

type TlogVerifier struct {
	BaseURL             string
	ID                  []byte
	ValidityPeriodStart time.Time
	ValidityPeriodEnd   time.Time
	HashFunc            crypto.Hash
	PublicKey           crypto.PublicKey
}

func (tr *ParsedTrustedRoot) TSACertificateAuthorities() []CertificateAuthority {
	return tr.tsaCertAuthorities
}

func (tr *ParsedTrustedRoot) FulcioCertificateAuthorities() []CertificateAuthority {
	return tr.fulcioCertAuthorities
}

func (tr *ParsedTrustedRoot) TlogVerifiers() map[string]*TlogVerifier {
	return tr.tlogVerifiers
}

func NewTrustedRootFromProtobuf(trustedRoot *prototrustroot.TrustedRoot) (parsedTrustedRoot *ParsedTrustedRoot, err error) {
	if trustedRoot.GetMediaType() != TrustedRootMediaType01 {
		return nil, fmt.Errorf("unsupported TrustedRoot media type: %s", trustedRoot.GetMediaType())
	}

	parsedTrustedRoot = &ParsedTrustedRoot{trustedRoot: trustedRoot}
	parsedTrustedRoot.tlogVerifiers, err = ParseTlogVerifiers(trustedRoot)
	if err != nil {
		return nil, err
	}

	parsedTrustedRoot.fulcioCertAuthorities, err = ParseCertificateAuthorities(trustedRoot.GetCertificateAuthorities())
	if err != nil {
		return nil, err
	}

	parsedTrustedRoot.tsaCertAuthorities, err = ParseCertificateAuthorities(trustedRoot.GetTimestampAuthorities())
	if err != nil {
		return nil, err
	}

	// TODO: Handle CT logs (trustedRoot.Ctlogs)
	return parsedTrustedRoot, nil
}

func ParseTlogVerifiers(trustedRoot *prototrustroot.TrustedRoot) (tlogVerifiers map[string]*TlogVerifier, err error) {
	tlogVerifiers = make(map[string]*TlogVerifier)
	for _, tlog := range trustedRoot.GetTlogs() {
		if tlog.GetHashAlgorithm() != protocommon.HashAlgorithm_SHA2_256 {
			return nil, fmt.Errorf("unsupported tlog hash algorithm: %s", tlog.GetHashAlgorithm())
		}
		if tlog.GetLogId() == nil {
			return nil, fmt.Errorf("tlog missing log ID")
		}
		if tlog.GetLogId().GetKeyId() == nil {
			return nil, fmt.Errorf("tlog missing log ID key ID")
		}
		encodedKeyID := hex.EncodeToString(tlog.GetLogId().GetKeyId())

		if tlog.GetPublicKey() == nil {
			return nil, fmt.Errorf("tlog missing public key")
		}
		if tlog.GetPublicKey().GetRawBytes() == nil {
			return nil, fmt.Errorf("tlog missing public key raw bytes")
		}

		switch tlog.GetPublicKey().GetKeyDetails() {
		case protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256:
			key, err := x509.ParsePKIXPublicKey(tlog.GetPublicKey().GetRawBytes())
			if err != nil {
				return nil, err
			}
			var ecKey *ecdsa.PublicKey
			var ok bool
			if ecKey, ok = key.(*ecdsa.PublicKey); !ok {
				return nil, fmt.Errorf("tlog public key is not ECDSA P256")
			}
			tlogVerifier := &TlogVerifier{
				BaseURL:   tlog.GetBaseUrl(),
				ID:        tlog.GetLogId().GetKeyId(),
				HashFunc:  crypto.SHA256,
				PublicKey: ecKey,
			}
			if validFor := tlog.GetPublicKey().GetValidFor(); validFor != nil {
				if validFor.GetStart() != nil {
					tlogVerifiers[encodedKeyID].ValidityPeriodStart = validFor.GetStart().AsTime()
				}
				if validFor.GetEnd() != nil {
					tlogVerifiers[encodedKeyID].ValidityPeriodEnd = validFor.GetEnd().AsTime()
				}
			}
			tlogVerifiers[encodedKeyID] = tlogVerifier
		default:
			return nil, fmt.Errorf("unsupported tlog public key type: %s", tlog.GetPublicKey().GetKeyDetails())
		}
	}
	return tlogVerifiers, nil
}

func ParseCertificateAuthorities(certAuthorities []*prototrustroot.CertificateAuthority) (certificateAuthorities []CertificateAuthority, err error) {
	certificateAuthorities = make([]CertificateAuthority, len(certAuthorities))
	for i, certAuthority := range certAuthorities {
		certificateAuthority, err := ParseCertificateAuthority(certAuthority)
		if err != nil {
			return nil, err
		}
		certificateAuthorities[i] = *certificateAuthority
	}
	return certificateAuthorities, nil
}

func ParseCertificateAuthority(certAuthority *prototrustroot.CertificateAuthority) (certificateAuthority *CertificateAuthority, err error) {
	if certAuthority == nil {
		return nil, fmt.Errorf("CertificateAuthority is nil")
	}
	certChain := certAuthority.GetCertChain()
	if certChain == nil {
		return nil, fmt.Errorf("CertificateAuthority missing cert chain")
	}
	chainLen := len(certChain.GetCertificates())
	if chainLen < 1 {
		return nil, fmt.Errorf("CertificateAuthority cert chain is empty")
	}

	certificateAuthority = &CertificateAuthority{}
	for i, cert := range certChain.GetCertificates() {
		parsedCert, err := x509.ParseCertificate(cert.RawBytes)
		if err != nil {
			return nil, err
		}
		switch {
		case i == 0 && !parsedCert.IsCA:
			certificateAuthority.Leaf = parsedCert
		case i < chainLen-1:
			certificateAuthority.Intermediates = append(certificateAuthority.Intermediates, parsedCert)
		case i == chainLen-1:
			certificateAuthority.Root = parsedCert
		}
	}
	validFor := certAuthority.GetValidFor()
	if validFor != nil {
		start := validFor.GetStart()
		if start != nil {
			certificateAuthority.ValidityPeriodStart = start.AsTime()
		}
		end := validFor.GetEnd()
		if end != nil {
			certificateAuthority.ValidityPeriodEnd = end.AsTime()
		}
	}

	// TODO: Should we inspect/enforce ca.Subject and ca.Uri?
	// TODO: Handle validity period (ca.ValidFor)

	return certificateAuthority, nil
}

func NewTrustedRootFromPath(path string) (*ParsedTrustedRoot, error) {
	trustedrootJSON, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return NewTrustedRootFromJSON(trustedrootJSON)
}

// NewTrustedRootFromJSON returns the Sigstore trusted root.
func NewTrustedRootFromJSON(rootJSON []byte) (*ParsedTrustedRoot, error) {
	pbTrustedRoot, err := NewTrustedRootProtobuf(rootJSON)
	if err != nil {
		return nil, err
	}

	return NewTrustedRootFromProtobuf(pbTrustedRoot)
}

// NewTrustedRootProtobuf returns the Sigstore trusted root as a protobuf.
func NewTrustedRootProtobuf(rootJSON []byte) (*prototrustroot.TrustedRoot, error) {
	pbTrustedRoot := &prototrustroot.TrustedRoot{}
	err := protojson.Unmarshal(rootJSON, pbTrustedRoot)
	if err != nil {
		return nil, err
	}
	return pbTrustedRoot, nil
}
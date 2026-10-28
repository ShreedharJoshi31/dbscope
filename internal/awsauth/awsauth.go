// Package awsauth signs dbscope's connections with AWS credentials from the
// SDK's own chain (environment, shared config and SSO, instance roles),
// never from dbscope's vault: RDS IAM authentication tokens for PostgreSQL
// and MySQL, and SigV4 request signing for Amazon OpenSearch Service.
package awsauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	rdsauth "github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

// ErrCredentials marks failures to find or use AWS credentials, so they are
// reported as an AWS sign-in problem rather than a wrong password.
var ErrCredentials = errors.New("no usable AWS credentials")

// Settings are the AWS options of a profile: auth, region and an optional
// shared-config profile name, plus service for SigV4.
type Settings struct {
	Auth    string // "rds-iam" or "sigv4"
	Region  string
	Profile string
	// Service is what SigV4 signs for: "es" (managed domains) or "aoss" (serverless).
	Service string
}

// FromOptions reads the AWS settings from a profile's options query; Auth
// is empty when the profile does not use AWS authentication.
func FromOptions(options string) Settings {
	q, _ := url.ParseQuery(options)
	s := Settings{Auth: q.Get("auth"), Region: q.Get("region"), Profile: q.Get("profile"), Service: q.Get("service")}
	if s.Service == "" {
		s.Service = "es"
	}
	return s
}

// Load resolves credentials and the region through the SDK's default chain.
func Load(ctx context.Context, s Settings) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(s.Region)}
	if s.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(s.Profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("%w: loading the AWS configuration: %w", ErrCredentials, err)
	}
	return cfg, nil
}

// RDSToken builds an IAM authentication token, valid for 15 minutes, to use
// as the password of a new connection to an RDS or Aurora instance.
func RDSToken(ctx context.Context, cfg aws.Config, host string, port int, user string) (string, error) {
	tok, err := rdsauth.BuildAuthToken(ctx, net.JoinHostPort(host, strconv.Itoa(port)), cfg.Region, user, cfg.Credentials)
	if err != nil {
		return "", fmt.Errorf("%w for the RDS IAM token: %w", ErrCredentials, err)
	}
	return tok, nil
}

// Transport signs every request with SigV4 for an OpenSearch domain.
type Transport struct {
	Base        http.RoundTripper
	Credentials aws.CredentialsProvider
	Region      string
	Service     string
	signer      *v4.Signer
	now         func() time.Time
}

// NewTransport wraps base so each request is signed with cfg's credentials.
func NewTransport(base http.RoundTripper, cfg aws.Config, service string) *Transport {
	return &Transport{Base: base, Credentials: cfg.Credentials, Region: cfg.Region, Service: service, signer: v4.NewSigner(), now: time.Now}
}

// RoundTrip hashes the body, signs the request and sends it. OpenSearch
// Serverless requires the X-Amz-Content-Sha256 header; managed domains accept it.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	creds, err := t.Credentials.Retrieve(req.Context())
	if err != nil {
		return nil, fmt.Errorf("%w to sign the request: %w", ErrCredentials, err)
	}
	var body []byte
	if req.Body != nil {
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body.Close()
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	signed := req.Clone(req.Context())
	signed.Body = io.NopCloser(bytes.NewReader(body))
	signed.ContentLength = int64(len(body))
	signed.Header.Set("X-Amz-Content-Sha256", hash)
	signed.Header.Del("Authorization")
	if err := t.signer.SignHTTP(req.Context(), creds, signed, hash, t.Service, t.Region, t.now()); err != nil {
		return nil, fmt.Errorf("signing the request: %w", err)
	}
	return t.Base.RoundTrip(signed)
}

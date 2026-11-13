// Copyright 2026 The Kagent Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file is adapted from kagent's go/core/pkg/install/substrate_init.go
// (kagent-dev/kagent, tag v1.0.1). Changes from the original:
//   - every object, including the three pod certificate controller CA pools,
//     goes to one caller-supplied namespace (the Helm release namespace)
//     instead of the hardcoded ate-system and podcertificate-controller-system;
//   - it no longer creates or labels namespaces, so no Namespace can end up
//     owned by the wrong Helm release;
//   - the RustFS credentials are generated randomly when absent, instead of
//     being copied from chart values;
//   - it takes a logger and reports what it created and what it kept.
// Existing objects are never replaced, except that the JWT pool's derived
// public keys are refreshed, as in the original.

package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/jwk"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	poolKeyID       = "1"
	inClusterIssuer = "https://kubernetes.default.svc"
	caValidity      = 365 * 24 * time.Hour

	// The keys the substrate-atelet chart's objectStore.credentialsSecret and
	// the AWS SDK read.
	rustfsAccessKeyField = "AWS_ACCESS_KEY_ID"
	rustfsSecretKeyField = "AWS_SECRET_ACCESS_KEY"
)

type caKeyType int

const (
	caKeyED25519 caKeyType = iota
	caKeyECDSAP256
)

// The Secret names are fixed by the substrate and substrate-podcert charts,
// which mount them by these names in the release namespace.
const (
	actorIDJWTPool    = "actor-id-jwt-pool"
	actorIDCAPool     = "actor-id-ca-pool"
	actorIDCACerts    = "actor-id-ca-certs"
	egressMITMCAPool  = "egress-mitm-ca-pool"
	serviceDNSCAPool  = "service-dns-ca-pool"
	podIdentityCAPool = "pod-identity-ca-pool"
	postgresCAPool    = "postgres-ca-pool"
	apiAuthentication = "ate-api-authentication"
)

type serializedCAPool struct {
	CAs              []*serializedCA
	ActiveForSigning string
}

type serializedCA struct {
	ID                 string
	SigningKeyPKCS8    []byte
	RootCertificateDER []byte
}

type serializedJWTPool struct {
	Authorities      []*serializedJWTAuthority
	ActiveForSigning string
}

type serializedJWTAuthority struct {
	ID              string
	Algorithm       string
	SigningKeyPKCS8 []byte
}

type bootstrapper struct {
	client    kubernetes.Interface
	namespace string
	logger    *slog.Logger
}

// prepareSubstrate creates or preserves the identity pools and authentication
// configuration needed by Substrate and the pod certificate controller.
// Existing signing keys are never rotated.
func (b *bootstrapper) prepareSubstrate(ctx context.Context) error {
	if err := b.ensureJWTPool(ctx, actorIDJWTPool); err != nil {
		return err
	}
	if err := b.ensureCAPool(ctx, actorIDCAPool, caKeyED25519); err != nil {
		return err
	}
	if err := b.ensureActorIDCACerts(ctx); err != nil {
		return err
	}
	if err := b.ensureCAPool(ctx, egressMITMCAPool, caKeyECDSAP256); err != nil {
		return err
	}
	// The pod certificate controller runs in the release namespace too, so
	// its pools live here, not in podcertificate-controller-system.
	for _, name := range []string{serviceDNSCAPool, podIdentityCAPool, postgresCAPool} {
		if err := b.ensureCAPool(ctx, name, caKeyED25519); err != nil {
			return err
		}
	}
	return b.ensureAPIAuthentication(ctx)
}

// ensureRustFSCredentials creates the RustFS access and secret keys with random
// values when the Secret is absent, and leaves an existing Secret alone. An
// existing Secret without both keys is an error, not something to overwrite.
func (b *bootstrapper) ensureRustFSCredentials(ctx context.Context, name string) error {
	existing, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		for _, field := range []string{rustfsAccessKeyField, rustfsSecretKeyField} {
			if len(existing.Data[field]) == 0 {
				return fmt.Errorf("secret %s/%s exists but has no %s; fix or delete it", b.namespace, name, field)
			}
		}
		b.logger.Info("kept existing Secret", "secret", name)
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get secret %s/%s: %w", b.namespace, name, err)
	}
	accessKey, err := randomString(20)
	if err != nil {
		return err
	}
	secretKey, err := randomString(40)
	if err != nil {
		return err
	}
	return b.createSecret(ctx, name, corev1.SecretTypeOpaque, map[string][]byte{
		rustfsAccessKeyField: []byte(accessKey),
		rustfsSecretKeyField: []byte(secretKey),
	})
}

const alphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// randomString returns n characters drawn uniformly from letters and digits,
// which every S3 implementation accepts in an access key or secret key.
func randomString(n int) (string, error) {
	out := make([]byte, n)
	limit := big.NewInt(int64(len(alphanumeric)))
	for i := range out {
		index, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate random credential: %w", err)
		}
		out[i] = alphanumeric[index.Int64()]
	}
	return string(out), nil
}

func (b *bootstrapper) ensureCAPool(ctx context.Context, name string, keyType caKeyType) error {
	if exists, err := b.secretExists(ctx, name); err != nil {
		return err
	} else if exists {
		b.logger.Info("kept existing Secret", "secret", name)
		return nil
	}
	data, err := newCAPoolSecretData(keyType)
	if err != nil {
		return fmt.Errorf("generate CA pool %s/%s: %w", b.namespace, name, err)
	}
	return b.createSecret(ctx, name, corev1.SecretTypeTLS, data)
}

func newCAPoolSecretData(keyType caKeyType) (map[string][]byte, error) {
	var privateKey crypto.PrivateKey
	var publicKey crypto.PublicKey
	switch keyType {
	case caKeyED25519:
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		privateKey, publicKey = private, public
	case caKeyECDSAP256:
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		privateKey, publicKey = private, &private.PublicKey
	default:
		return nil, fmt.Errorf("unsupported CA key type %d", keyType)
	}

	now := time.Now()
	template := &x509.Certificate{
		Subject:               pkix.Name{CommonName: rand.Text()},
		NotBefore:             now,
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, err
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	pool, err := json.Marshal(&serializedCAPool{
		CAs: []*serializedCA{{
			ID:                 poolKeyID,
			SigningKeyPKCS8:    privateKeyDER,
			RootCertificateDER: certificateDER,
		}},
		ActiveForSigning: poolKeyID,
	})
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"pool":                  pool,
		corev1.TLSCertKey:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		corev1.TLSPrivateKeyKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}),
	}, nil
}

func (b *bootstrapper) ensureJWTPool(ctx context.Context, name string) error {
	secret, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		// Keep signing keys intact when upgrading an existing installation.
		jwks, err := jwtPoolPublicKeys(secret.Data["pool"])
		if err != nil {
			return fmt.Errorf("read JWT authority pool %s/%s: %w", b.namespace, name, err)
		}
		if bytes.Equal(secret.Data["jwks"], jwks) {
			b.logger.Info("kept existing Secret", "secret", name)
			return nil
		}
		secret.Data["jwks"] = jwks
		if _, err := b.client.CoreV1().Secrets(b.namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update JWT authority public keys %s/%s: %w", b.namespace, name, err)
		}
		b.logger.Info("refreshed public keys in existing Secret", "secret", name)
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get secret %s/%s: %w", b.namespace, name, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate JWT authority: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode JWT authority: %w", err)
	}
	pool, err := json.Marshal(&serializedJWTPool{
		Authorities:      []*serializedJWTAuthority{{ID: poolKeyID, Algorithm: "ES256", SigningKeyPKCS8: keyDER}},
		ActiveForSigning: poolKeyID,
	})
	if err != nil {
		return fmt.Errorf("encode JWT authority pool: %w", err)
	}
	jwks, err := jwtPoolPublicKeys(pool)
	if err != nil {
		return err
	}
	return b.createSecret(ctx, name, corev1.SecretTypeOpaque, map[string][]byte{"pool": pool, "jwks": jwks})
}

// jwtPoolPublicKeys derives the public-only JWKS mounted by Substrate's snapshot
// exchange. The upstream pool serializer is internal to the Substrate module.
func jwtPoolPublicKeys(data []byte) ([]byte, error) {
	var pool serializedJWTPool
	if err := json.Unmarshal(data, &pool); err != nil {
		return nil, fmt.Errorf("decode JWT authority pool: %w", err)
	}
	if len(pool.Authorities) == 0 {
		return nil, fmt.Errorf("JWT authority pool has no authorities")
	}
	keys := jwk.NewSet()
	for _, authority := range pool.Authorities {
		if authority == nil || authority.ID == "" || authority.Algorithm == "" {
			return nil, fmt.Errorf("JWT authority is missing its ID or algorithm")
		}
		key, err := x509.ParsePKCS8PrivateKey(authority.SigningKeyPKCS8)
		if err != nil {
			return nil, fmt.Errorf("parse JWT authority %s: %w", authority.ID, err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("JWT authority %s has unsupported key type %T", authority.ID, key)
		}
		publicKey, err := jwk.New(signer.Public())
		if err != nil {
			return nil, fmt.Errorf("encode JWT authority %s public key: %w", authority.ID, err)
		}
		for field, value := range map[string]string{jwk.KeyIDKey: authority.ID, jwk.AlgorithmKey: authority.Algorithm, jwk.KeyUsageKey: "sig"} {
			if err := publicKey.Set(field, value); err != nil {
				return nil, fmt.Errorf("set JWT authority %s %s: %w", authority.ID, field, err)
			}
		}
		keys.Add(publicKey)
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("encode JWT authority public keys: %w", err)
	}
	return data, nil
}

func (b *bootstrapper) ensureActorIDCACerts(ctx context.Context) error {
	if exists, err := b.secretExists(ctx, actorIDCACerts); err != nil {
		return err
	} else if exists {
		b.logger.Info("kept existing Secret", "secret", actorIDCACerts)
		return nil
	}
	poolSecret, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, actorIDCAPool, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get actor identity CA pool: %w", err)
	}
	var pool serializedCAPool
	if err := json.Unmarshal(poolSecret.Data["pool"], &pool); err != nil {
		return fmt.Errorf("decode actor identity CA pool: %w", err)
	}
	if len(pool.CAs) == 0 {
		return fmt.Errorf("actor identity CA pool contains no CAs")
	}
	var roots strings.Builder
	for _, ca := range pool.CAs {
		if len(ca.RootCertificateDER) == 0 {
			return fmt.Errorf("CA %q has no root certificate", ca.ID)
		}
		roots.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.RootCertificateDER}))
	}
	return b.createSecret(ctx, actorIDCACerts, corev1.SecretTypeOpaque, map[string][]byte{"ca.crt": []byte(roots.String())})
}

// ensureAPIAuthentication writes the JWT provider that lets Kubernetes service
// account tokens authenticate to ate-api-server, for `kubectl ate`. The
// audience is a plain string that Substrate's own CLI and docs use as
// api.<namespace>.svc; it is not resolved as a DNS name.
func (b *bootstrapper) ensureAPIAuthentication(ctx context.Context) error {
	configMaps := b.client.CoreV1().ConfigMaps(b.namespace)
	configMap, err := configMaps.Get(ctx, apiAuthentication, metav1.GetOptions{})
	if err == nil {
		// Earlier installers wrote this field, which Substrate v0.6.0 removed.
		// Preserve the configured providers and any operator customizations.
		authentication, obsolete := strings.CutPrefix(configMap.Data["authentication.yaml"], "actorIdentityJWTProvider: kubernetes\n")
		if !obsolete {
			b.logger.Info("kept existing ConfigMap", "configmap", apiAuthentication)
			return nil
		}
		configMap.Data["authentication.yaml"] = authentication
		if _, err := configMaps.Update(ctx, configMap, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update API authentication config: %w", err)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get API authentication config: %w", err)
	}
	issuer := b.clusterOIDCIssuer(ctx)
	authentication := fmt.Sprintf("jwtProviders:\n- name: kubernetes\n  issuer: %s\n  audiences: [api.%s.svc]\n", issuer, b.namespace)
	if issuer == inClusterIssuer || issuer == inClusterIssuer+".cluster.local" {
		authentication += "  certificateAuthorityFile: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt\n" +
			"  discoveryTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token\n"
	}
	configMap = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: b.namespace, Name: apiAuthentication},
		Data:       map[string]string{"authentication.yaml": strings.TrimRight(authentication, "\n")},
	}
	if _, err := configMaps.Create(ctx, configMap, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create API authentication config: %w", err)
	}
	b.logger.Info("created ConfigMap", "configmap", apiAuthentication, "issuer", issuer)
	return nil
}

func (b *bootstrapper) clusterOIDCIssuer(ctx context.Context) string {
	restClient := b.client.Discovery().RESTClient()
	if restClient == nil {
		return inClusterIssuer
	}
	raw, err := restClient.Get().AbsPath("/.well-known/openid-configuration").DoRaw(ctx)
	if err != nil {
		return inClusterIssuer
	}
	var document struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || document.Issuer == "" {
		return inClusterIssuer
	}
	return document.Issuer
}

func (b *bootstrapper) secretExists(ctx context.Context, name string) (bool, error) {
	_, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("get secret %s/%s: %w", b.namespace, name, err)
}

func (b *bootstrapper) createSecret(ctx context.Context, name string, secretType corev1.SecretType, data map[string][]byte) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: b.namespace, Name: name}, Type: secretType, Data: data}
	_, err := b.client.CoreV1().Secrets(b.namespace).Create(ctx, secret, metav1.CreateOptions{})
	switch {
	case err == nil:
		b.logger.Info("created Secret", "secret", name)
	case apierrors.IsAlreadyExists(err):
		// Something else created it since the Get; keep theirs.
		b.logger.Info("kept Secret created concurrently", "secret", name)
	default:
		return fmt.Errorf("create secret %s/%s: %w", b.namespace, name, err)
	}
	return nil
}

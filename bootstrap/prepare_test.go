package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testNamespace = "ate-system"

func newTestBootstrapper(objects ...*corev1.Secret) (*bootstrapper, *fake.Clientset) {
	client := fake.NewClientset()
	for _, s := range objects {
		if _, err := client.CoreV1().Secrets(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{}); err != nil {
			panic(err)
		}
	}
	return &bootstrapper{client: client, namespace: testNamespace, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, client
}

func secretData(t *testing.T, client *fake.Clientset, namespace, name string) map[string][]byte {
	t.Helper()
	s, err := client.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret %s/%s: %v", namespace, name, err)
	}
	return s.Data
}

// Every pool, including the three of the pod certificate controller, lands in
// the one namespace, and nothing is written anywhere else.
func TestPrepareSubstrateWritesOnlyToTheReleaseNamespace(t *testing.T) {
	b, client := newTestBootstrapper()
	if err := b.prepareSubstrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		actorIDJWTPool, actorIDCAPool, actorIDCACerts, egressMITMCAPool,
		serviceDNSCAPool, podIdentityCAPool, postgresCAPool,
	}
	list, err := client.CoreV1().Secrets(metav1.NamespaceAll).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range list.Items {
		if s.Namespace != testNamespace {
			t.Errorf("secret %s/%s written outside the release namespace", s.Namespace, s.Name)
		}
		got[s.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("secret %s was not created", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("created %d secrets, want %d", len(got), len(want))
	}
	if namespaces, _ := client.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{}); len(namespaces.Items) != 0 {
		t.Errorf("the bootstrap must not create namespaces, created %d", len(namespaces.Items))
	}
}

func TestCAPoolsHoldAUsablePool(t *testing.T) {
	b, client := newTestBootstrapper()
	if err := b.prepareSubstrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{actorIDCAPool, egressMITMCAPool, serviceDNSCAPool, podIdentityCAPool, postgresCAPool} {
		data := secretData(t, client, testNamespace, name)
		var pool serializedCAPool
		if err := json.Unmarshal(data["pool"], &pool); err != nil {
			t.Fatalf("%s: pool: %v", name, err)
		}
		if pool.ActiveForSigning != poolKeyID || len(pool.CAs) != 1 {
			t.Fatalf("%s: pool %+v", name, pool)
		}
		cert, err := x509.ParseCertificate(pool.CAs[0].RootCertificateDER)
		if err != nil || !cert.IsCA {
			t.Fatalf("%s: root certificate: %v", name, err)
		}
		if _, err := x509.ParsePKCS8PrivateKey(pool.CAs[0].SigningKeyPKCS8); err != nil {
			t.Fatalf("%s: signing key: %v", name, err)
		}
		if block, _ := pem.Decode(data[corev1.TLSCertKey]); block == nil || block.Type != "CERTIFICATE" {
			t.Fatalf("%s: tls.crt is not a certificate", name)
		}
	}
	// actor-id-ca-certs is the actor identity pool's root certificate.
	var pool serializedCAPool
	_ = json.Unmarshal(secretData(t, client, testNamespace, actorIDCAPool)["pool"], &pool)
	want := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pool.CAs[0].RootCertificateDER})
	if got := secretData(t, client, testNamespace, actorIDCACerts)["ca.crt"]; !bytes.Equal(got, want) {
		t.Error("actor-id-ca-certs does not hold the actor identity pool's root certificate")
	}
	var jwtPool serializedJWTPool
	if err := json.Unmarshal(secretData(t, client, testNamespace, actorIDJWTPool)["pool"], &jwtPool); err != nil || len(jwtPool.Authorities) != 1 {
		t.Fatalf("JWT pool: %v", err)
	}
	if !bytes.Contains(secretData(t, client, testNamespace, actorIDJWTPool)["jwks"], []byte(`"kid":"1"`)) {
		t.Error("jwks lacks the key id")
	}
}

// A second run, and a run against existing Secrets, change nothing: the CAs
// are never rotated.
func TestPrepareSubstrateNeverRotates(t *testing.T) {
	b, client := newTestBootstrapper()
	ctx := context.Background()
	if err := b.prepareSubstrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.ensureRustFSCredentials(ctx, "rustfs-credentials"); err != nil {
		t.Fatal(err)
	}
	before, _ := client.CoreV1().Secrets(testNamespace).List(ctx, metav1.ListOptions{})
	client.ClearActions()

	if err := b.prepareSubstrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.ensureRustFSCredentials(ctx, "rustfs-credentials"); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if verb := action.GetVerb(); verb != "get" {
			t.Errorf("second run issued %s %s, want only gets", verb, action.GetResource().Resource)
		}
	}
	after, _ := client.CoreV1().Secrets(testNamespace).List(ctx, metav1.ListOptions{})
	for _, old := range before.Items {
		for _, now := range after.Items {
			if old.Name != now.Name {
				continue
			}
			for key, value := range old.Data {
				if !bytes.Equal(value, now.Data[key]) {
					t.Errorf("secret %s key %s changed on the second run", old.Name, key)
				}
			}
		}
	}
}

func TestExistingCAPoolIsKept(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: serviceDNSCAPool},
		Data:       map[string][]byte{"pool": []byte("operator-managed")},
	}
	b, client := newTestBootstrapper(existing)
	if err := b.ensureCAPool(context.Background(), serviceDNSCAPool, caKeyED25519); err != nil {
		t.Fatal(err)
	}
	if got := string(secretData(t, client, testNamespace, serviceDNSCAPool)["pool"]); got != "operator-managed" {
		t.Errorf("existing pool replaced: %q", got)
	}
}

func TestJWTPublicKeysAreRefreshedWithoutTouchingTheSigningKey(t *testing.T) {
	b, client := newTestBootstrapper()
	ctx := context.Background()
	if err := b.ensureJWTPool(ctx, actorIDJWTPool); err != nil {
		t.Fatal(err)
	}
	secret, _ := client.CoreV1().Secrets(testNamespace).Get(ctx, actorIDJWTPool, metav1.GetOptions{})
	pool := append([]byte(nil), secret.Data["pool"]...)
	secret.Data["jwks"] = []byte("stale")
	if _, err := client.CoreV1().Secrets(testNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.ensureJWTPool(ctx, actorIDJWTPool); err != nil {
		t.Fatal(err)
	}
	data := secretData(t, client, testNamespace, actorIDJWTPool)
	if !bytes.Equal(data["pool"], pool) {
		t.Error("the signing pool changed")
	}
	if string(data["jwks"]) == "stale" {
		t.Error("jwks was not refreshed")
	}
}

func TestAPIAuthenticationUsesTheReleaseNamespaceAudience(t *testing.T) {
	b, client := newTestBootstrapper()
	if err := b.ensureAPIAuthentication(context.Background()); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), apiAuthentication, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	config := cm.Data["authentication.yaml"]
	for _, want := range []string{"audiences: [api.ate-system.svc]", "issuer: " + inClusterIssuer, "certificateAuthorityFile:"} {
		if !strings.Contains(config, want) {
			t.Errorf("authentication.yaml lacks %q:\n%s", want, config)
		}
	}
	// An operator's edit survives a later run.
	cm.Data["authentication.yaml"] = "custom"
	if _, err := client.CoreV1().ConfigMaps(testNamespace).Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.ensureAPIAuthentication(context.Background()); err != nil {
		t.Fatal(err)
	}
	cm, _ = client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), apiAuthentication, metav1.GetOptions{})
	if cm.Data["authentication.yaml"] != "custom" {
		t.Error("existing authentication config was replaced")
	}
}

func TestRustFSCredentialsAreRandomAndKept(t *testing.T) {
	b, client := newTestBootstrapper()
	ctx := context.Background()
	if err := b.ensureRustFSCredentials(ctx, "rustfs-credentials"); err != nil {
		t.Fatal(err)
	}
	data := secretData(t, client, testNamespace, "rustfs-credentials")
	access, secret := string(data[rustfsAccessKeyField]), string(data[rustfsSecretKeyField])
	if len(access) != 20 || len(secret) != 40 {
		t.Fatalf("lengths %d and %d", len(access), len(secret))
	}
	for _, known := range []string{"rustfsadmin", "password", "minioadmin"} {
		if strings.Contains(access, known) || strings.Contains(secret, known) {
			t.Errorf("credential contains %q", known)
		}
	}
	if err := b.ensureRustFSCredentials(ctx, "rustfs-credentials"); err != nil {
		t.Fatal(err)
	}
	again := secretData(t, client, testNamespace, "rustfs-credentials")
	if string(again[rustfsAccessKeyField]) != access || string(again[rustfsSecretKeyField]) != secret {
		t.Error("credentials were regenerated")
	}

	// Two clusters do not share keys.
	other, otherClient := newTestBootstrapper()
	if err := other.ensureRustFSCredentials(ctx, "rustfs-credentials"); err != nil {
		t.Fatal(err)
	}
	if string(secretData(t, otherClient, testNamespace, "rustfs-credentials")[rustfsSecretKeyField]) == secret {
		t.Error("two runs generated the same secret key")
	}
}

func TestIncompleteRustFSSecretIsAnError(t *testing.T) {
	b, client := newTestBootstrapper(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "rustfs-credentials"},
		Data:       map[string][]byte{rustfsAccessKeyField: []byte("only-access")},
	})
	if err := b.ensureRustFSCredentials(context.Background(), "rustfs-credentials"); err == nil {
		t.Fatal("want an error for a Secret without a secret key")
	}
	if _, ok := secretData(t, client, testNamespace, "rustfs-credentials")[rustfsSecretKeyField]; ok {
		t.Error("the incomplete Secret was modified")
	}
}

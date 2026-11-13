package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// The key of the bundle ConfigMap. The chart's MutatingAdmissionPolicy
	// mounts it with this subPath over the egress gateway's own bundle.
	egressCABundleKey = "ca-certificates.crt"
	// The label of the egress gateway pods, which the Deployment recreates.
	egressPodSelector = "app=atenet-egress"
)

// parseCertificatesPEM returns the certificates in data, re-encoded as PEM. It
// fails when there is none, when a block is not a CERTIFICATE (so that a
// private key pasted by mistake never lands in a ConfigMap) or when one does
// not parse as X.509. Text between the blocks is dropped.
func parseCertificatesPEM(data []byte) ([]byte, error) {
	var out bytes.Buffer
	count := 0
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("PEM block %d is a %s, not a CERTIFICATE", count+1, block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("PEM block %d is not a valid certificate: %w", count+1, err)
		}
		if err := pem.Encode(&out, block); err != nil {
			return nil, err
		}
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("no CERTIFICATE PEM block found")
	}
	return out.Bytes(), nil
}

// ensureEgressCABundle writes ConfigMap name with the system CA bundle (the
// public roots in this image, read from systemCAFile) followed by the
// certificates in inputFile. The egress gateway trusts only its own image's
// bundle, so this one replaces it, and has to keep the public roots.
//
// When the bundle is new or changed it deletes the egress gateway pods: the
// ConfigMap is mounted with subPath, which never updates in a running pod, and
// patching the Deployment would show as drift to Flux. The Deployment
// recreates the pods, and the chart's MutatingAdmissionPolicy mounts the new
// bundle into them. On a first install there are no pods yet and the delete is
// a no-op; on an upgrade that first sets the CA the running pods predate the
// mount, so they are replaced too.
func (b *bootstrapper) ensureEgressCABundle(ctx context.Context, name, inputFile, systemCAFile string) error {
	input, err := os.ReadFile(inputFile)
	if err != nil {
		return fmt.Errorf("read extra CA file: %w", err)
	}
	extra, err := parseCertificatesPEM(input)
	if err != nil {
		return fmt.Errorf("extra CA file %s: %w", inputFile, err)
	}
	system, err := os.ReadFile(systemCAFile)
	if err != nil {
		return fmt.Errorf("read system CA file: %w", err)
	}
	bundle := append([]byte{}, system...)
	if !bytes.HasSuffix(bundle, []byte("\n")) {
		bundle = append(bundle, '\n')
	}
	bundle = append(bundle, extra...)

	configMaps := b.client.CoreV1().ConfigMaps(b.namespace)
	existing, err := configMaps.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: b.namespace, Name: name},
			Data:       map[string]string{egressCABundleKey: string(bundle)},
		}
		if _, err := configMaps.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create configmap %s/%s: %w", b.namespace, name, err)
		}
		b.logger.Info("created ConfigMap", "configmap", name)
	case err != nil:
		return fmt.Errorf("get configmap %s/%s: %w", b.namespace, name, err)
	case existing.Data[egressCABundleKey] == string(bundle):
		b.logger.Info("kept ConfigMap, the bundle is unchanged", "configmap", name)
		return nil
	default:
		existing.Data = map[string]string{egressCABundleKey: string(bundle)}
		if _, err := configMaps.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update configmap %s/%s: %w", b.namespace, name, err)
		}
		b.logger.Info("updated ConfigMap", "configmap", name)
	}

	pods := b.client.CoreV1().Pods(b.namespace)
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: egressPodSelector})
	if err != nil {
		return fmt.Errorf("list egress gateway pods: %w", err)
	}
	for _, pod := range list.Items {
		if err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pod %s/%s: %w", b.namespace, pod.Name, err)
		}
		b.logger.Info("deleted egress gateway pod so it mounts the new bundle", "pod", pod.Name)
	}
	return nil
}

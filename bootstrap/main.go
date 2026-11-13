// Command bootstrap creates the cluster-side prerequisites of the kagent
// addon's Substrate install: certificate authorities, the actor JWT signing
// pool, the API authentication config and the RustFS credentials. It runs as a
// Helm pre-install and pre-upgrade hook Job, writes only to the release
// namespace, and creates each object only when it is absent.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	var (
		namespace       = flag.String("namespace", "", "namespace of the Helm release, where every object is written (required)")
		podSecurity     = flag.String("pod-security-level", "", "Pod Security Admission level (privileged, baseline or restricted) to set as enforce, audit and warn labels on the namespace; empty leaves its labels alone")
		rustfsSecret    = flag.String("rustfs-credentials-secret", "", "name of the RustFS credentials Secret to create when absent; empty skips it")
		kubeconfigPath  = flag.String("kubeconfig", "", "path to a kubeconfig; empty uses the in-cluster configuration")
		overallDeadline = flag.Duration("timeout", 5*time.Minute, "how long the whole run may take")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger, *namespace, *podSecurity, *rustfsSecret, *kubeconfigPath, *overallDeadline); err != nil {
		logger.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, namespace, podSecurity, rustfsSecret, kubeconfigPath string, deadline time.Duration) error {
	if namespace == "" {
		return fmt.Errorf("--namespace is required")
	}
	config, err := restConfig(kubeconfigPath)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	b := &bootstrapper{client: client, namespace: namespace, logger: logger}
	// First, before anything else in the release can start a pod: the
	// namespace is created by the helm-controller without Pod Security labels,
	// and atelet needs more than `restricted` allows.
	if podSecurity != "" {
		if err := b.ensurePodSecurityLevel(ctx, podSecurity); err != nil {
			return fmt.Errorf("label namespace %s: %w", namespace, err)
		}
	}
	if err := b.prepareSubstrate(ctx); err != nil {
		return fmt.Errorf("prepare Substrate prerequisites: %w", err)
	}
	if rustfsSecret != "" {
		if err := b.ensureRustFSCredentials(ctx, rustfsSecret); err != nil {
			return fmt.Errorf("prepare RustFS credentials: %w", err)
		}
	}
	logger.Info("bootstrap complete", "namespace", namespace)
	return nil
}

func restConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %s: %w", kubeconfigPath, err)
		}
		return config, nil
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("load in-cluster configuration: %w", err)
	}
	return config, nil
}

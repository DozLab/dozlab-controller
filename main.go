package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"dozlab-controller/controllers"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	
	// Register the LabSession CRD
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{
			Group:   "dozlab.io",
			Version: "v1",
			Kind:    "LabSession",
		},
		&controllers.UnstructuredLabSession{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{
			Group:   "dozlab.io",
			Version: "v1",
			Kind:    "LabSessionList",
		},
		&controllers.UnstructuredLabSessionList{},
	)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var watchNamespace string
	var syncPeriod time.Duration
	var cacheSyncTimeout time.Duration
	var maxConcurrentReconciles int
	var gracefulShutdownTimeout time.Duration

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&watchNamespace, "namespace", "",
		"Namespace to watch for LabSession resources. If empty, watches all namespaces.")
	flag.DurationVar(&syncPeriod, "sync-period", 10*time.Minute,
		"Minimum sync period for controller reconciliation (default: 10 minutes)")
	flag.DurationVar(&cacheSyncTimeout, "cache-sync-timeout", 2*time.Minute,
		"Timeout for initial cache sync (default: 2 minutes)")
	flag.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", 10,
		"Maximum number of concurrent reconciles (default: 10)")
	flag.DurationVar(&gracefulShutdownTimeout, "graceful-shutdown-timeout", 30*time.Second,
		"Timeout for graceful shutdown (default: 30 seconds)")
	
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancelation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	// Configure cache settings for optimal performance
	// Cache only necessary resources to reduce memory overhead
	cacheOpts := ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "dozlab-controller-leader-election",
		// Cache configuration with sync period
		Cache: cache.Options{
			// Sync period: minimum time between reconciliation cycles
			// Prevents excessive reconciliation, reducing API server load
			SyncPeriod: &syncPeriod,
		},
		// Graceful shutdown timeout for clean termination
		GracefulShutdownTimeout: &gracefulShutdownTimeout,
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	}

	// If watching a specific namespace, configure cache to only watch that namespace
	// This significantly reduces memory footprint and API server load
	if watchNamespace != "" {
		setupLog.Info("watching specific namespace", "namespace", watchNamespace)
		cacheOpts.Cache.DefaultNamespaces = map[string]cache.Config{
			watchNamespace: {},
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), cacheOpts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.LabSessionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr, maxConcurrentReconciles); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LabSession")
		os.Exit(1)
	}

	// Add health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Add custom health check for controller dependencies
	if err := mgr.AddHealthzCheck("controller-dependencies", func(req *http.Request) error {
		return checkControllerDependencies()
	}); err != nil {
		setupLog.Error(err, "unable to set up controller dependencies health check")
		os.Exit(1)
	}

	// Add custom readiness check for cache sync (following dozlab-api pattern)
	// This ensures the controller is only marked ready after cache is fully synchronized
	if err := mgr.AddReadyzCheck("cache-sync", func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), cacheSyncTimeout)
		defer cancel()
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("cache not synced within timeout (%v)", cacheSyncTimeout)
		}
		return nil
	}); err != nil {
		setupLog.Error(err, "unable to set up cache sync check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"sync-period", syncPeriod,
		"max-concurrent-reconciles", maxConcurrentReconciles,
		"cache-sync-timeout", cacheSyncTimeout,
		"graceful-shutdown-timeout", gracefulShutdownTimeout,
		"watch-namespace", watchNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// checkControllerDependencies performs a health check on controller dependencies
func checkControllerDependencies() error {
	// Check if we can connect to the Kubernetes API
	cfg := ctrl.GetConfigOrDie()
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("failed to connect to kubernetes API: %w", err)
	}

	_ = ctx // Use ctx to avoid unused variable warning
	return nil
}
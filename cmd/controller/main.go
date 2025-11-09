package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"dozlab-controller/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(controller.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
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

	// Configure cache settings for optimal performance
	cacheOpts := cache.Options{
		// Sync period: minimum time between reconciliation cycles
		SyncPeriod: &syncPeriod,
	}

	// If watching a specific namespace, configure cache to only watch that namespace
	if watchNamespace != "" {
		setupLog.Info("watching specific namespace", "namespace", watchNamespace)
		cacheOpts.DefaultNamespaces = map[string]cache.Config{
			watchNamespace: {},
		}
	}

	// Create manager with optimizations
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port: 9443,
		}),
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "dozlab-controller-leader-election",
		Cache:                   cacheOpts,
		GracefulShutdownTimeout: &gracefulShutdownTimeout,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup controller with optimizations
	if err = (&controller.LabSessionReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("dozlab-controller"),
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

	// Add custom readiness check for cache sync
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
		"version", getVersion(),
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

func getVersion() string {
	version := os.Getenv("VERSION")
	if version == "" {
		return "dev"
	}
	return version
}

package main

import (
	"flag"
	"os"
	"time"

	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	"github.com/gocrane/kubeocean/pkg/manager/controller"
	"github.com/gocrane/kubeocean/pkg/manager/metrics"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cloudv1beta1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var leaderElectionID string
	var leaderElectionNamespace string
	var leaderElectionLeaseDuration time.Duration
	var leaderElectionRenewDeadline time.Duration
	var leaderElectionRetryPeriod time.Duration
	var kubeClientQPS int
	var kubeClientBurst int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&leaderElectionID, "leader-election-id", "kubeocean-manager-leader",
		"The name of the leader election ID to use.")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"The namespace where the leader election resource will be created. "+
			"If not set, the namespace from the service account will be used.")
	flag.DurationVar(&leaderElectionLeaseDuration, "leader-election-lease-duration", 15*time.Second,
		"The duration that non-leader candidates will wait to force acquire leadership.")
	flag.DurationVar(&leaderElectionRenewDeadline, "leader-election-renew-deadline", 10*time.Second,
		"The duration that the acting master will retry refreshing leadership before giving up.")
	flag.DurationVar(&leaderElectionRetryPeriod, "leader-election-retry-period", 2*time.Second,
		"The duration the clients should wait between attempting acquisition and renewal of a leadership.")
	flag.IntVar(&kubeClientQPS, "kube-client-qps", 0, "QPS for kubernetes client.(default 0 means no limit)")
	flag.IntVar(&kubeClientBurst, "kube-client-burst", 0, "Burst for kubernetes client.(default 0 means no limit)")

	opts := zap.Options{
		Development:     false,
		StacktraceLevel: zapcore.DPanicLevel,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Get the kubernetes config and modify it with QPS and Burst settings
	cfg := ctrl.GetConfigOrDie()
	if kubeClientQPS > 0 {
		cfg.QPS = float32(kubeClientQPS)
	}
	if kubeClientBurst > 0 {
		cfg.Burst = kubeClientBurst
	}

	// Setup manager options with enhanced leader election configuration
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},

		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       leaderElectionNamespace,
		LeaseDuration:                 &leaderElectionLeaseDuration,
		RenewDeadline:                 &leaderElectionRenewDeadline,
		RetryPeriod:                   &leaderElectionRetryPeriod,
		LeaderElectionReleaseOnCancel: true,
		Cache: cache.Options{
			SyncPeriod: ptr.To(10 * time.Minute),
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup context for graceful shutdown
	ctx := ctrl.SetupSignalHandler()

	// Add leader election callbacks
	if enableLeaderElection {
		setupLog.Info("Leader election enabled",
			"leaderElectionID", leaderElectionID,
			"leaderElectionNamespace", leaderElectionNamespace,
			"leaseDuration", leaderElectionLeaseDuration,
			"renewDeadline", leaderElectionRenewDeadline,
			"retryPeriod", leaderElectionRetryPeriod,
		)
	}

	// Log kubernetes client configuration
	setupLog.Info("Kubernetes client configuration",
		"qps", kubeClientQPS,
		"burst", kubeClientBurst,
	)

	// Register ClusterBinding metrics collector
	metrics.RegisterClusterBindingCollector(mgr.GetClient(), setupLog.WithName("metrics"))

	// Setup ClusterBinding controller
	if err = controller.NewClusterBindingReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		ctrl.Log.WithName("controllers").WithName("ClusterBinding"),
		mgr.GetEventRecorderFor("clusterbinding-controller"),
	).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterBinding")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

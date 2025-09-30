package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	//+kubebuilder:scaffold:imports

	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clsv1 "github.com/TKEColocation/kubeocean/api/cls/v1"
	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/syncer"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cloudv1beta1.AddToScheme(scheme))
	utilruntime.Must(clsv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var leaderElectionID string
	var clusterBindingName string
	var virtualClientQPS int
	var virtualClientBurst int
	var physicalClientQPS int
	var physicalClientBurst int
	var prometheusVNodeBasePort int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for syncer. "+
			"Enabling this will ensure there is only one active syncer instance per ClusterBinding.")
	flag.StringVar(&leaderElectionID, "leader-election-id", "",
		"The name of the leader election ID to use. If empty, will be generated from cluster-binding-name.")
	flag.StringVar(&clusterBindingName, "cluster-binding-name", "",
		"The name of the ClusterBinding resource this syncer is responsible for.")
	flag.IntVar(&virtualClientQPS, "virtual-client-qps", 0, "QPS for virtual kubernetes client.(default 0 means no limit)")
	flag.IntVar(&virtualClientBurst, "virtual-client-burst", 0, "Burst for virtual kubernetes client.(default 0 means no limit)")
	flag.IntVar(&physicalClientQPS, "physical-client-qps", 0, "QPS for physical kubernetes client.(default 0 means no limit)")
	flag.IntVar(&physicalClientBurst, "physical-client-burst", 0, "Burst for physical kubernetes client.(default 0 means no limit)")
	flag.IntVar(&prometheusVNodeBasePort, "prometheus-vnode-base-port", 9006, "The port used in VNode Prometheus URL annotations (default 9006)")

	opts := zap.Options{
		Development:     false,
		StacktraceLevel: zapcore.DPanicLevel,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if clusterBindingName == "" {
		setupLog.Error(fmt.Errorf("cluster-binding-name is required"), "missing required parameter")
		os.Exit(1)
	}

	// Generate leader election ID if not provided
	if leaderElectionID == "" {
		leaderElectionID = fmt.Sprintf("kubeocean-syncer-%s", clusterBindingName)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Get clusterBinding to determine clusterID for label filtering
	config := ctrl.GetConfigOrDie()

	// Set QPS and Burst for the virtual client
	if virtualClientQPS > 0 {
		config.QPS = float32(virtualClientQPS)
	}
	if virtualClientBurst > 0 {
		config.Burst = virtualClientBurst
	}

	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create client")
		os.Exit(1)
	}

	// Get clusterBinding to determine clusterID
	clusterBinding := &cloudv1beta1.ClusterBinding{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{Name: clusterBindingName}, clusterBinding)
	if err != nil {
		setupLog.Error(err, "unable to get clusterBinding", "name", clusterBindingName)
		os.Exit(1)
	}

	clusterID := clusterBinding.Spec.ClusterID
	if clusterID == "" {
		setupLog.Error(fmt.Errorf("clusterID is empty"), "missing required parameter")
		os.Exit(1)
	}
	//managedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterID)

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       leaderElectionID,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Node{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding: clusterBindingName,
					}),
				},
				// TODO: Uncomment this when we have a way to get the kubeconfig from the secret
				/*&corev1.Secret{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						managedByClusterIDLabel:     "true",
					}),
				},
				&corev1.ConfigMap{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						managedByClusterIDLabel:     "true",
					}),
				},
				&corev1.PersistentVolumeClaim{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						managedByClusterIDLabel:     "true",
					}),
				},
				&corev1.PersistentVolume{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						managedByClusterIDLabel:     "true",
					}),
				},*/
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Initialize the Kubeocean Syncer
	kubeoceanSyncer, err := syncer.NewKubeoceanSyncer(mgr, mgr.GetClient(), mgr.GetScheme(), clusterBindingName, physicalClientQPS, physicalClientBurst)
	if err != nil {
		setupLog.Error(err, "unable to create kubeocean syncer")
		os.Exit(1)
	}

	// Configure VNode Prometheus base port for bottom-up syncer (used in annotations)
	kubeoceanSyncer.SetPrometheusVNodeBasePort(prometheusVNodeBasePort)

	ctx := ctrl.SetupSignalHandler()

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Add the kubeocean syncer as a runnable to the manager
	if err := mgr.Add(kubeoceanSyncer); err != nil {
		setupLog.Error(err, "unable to add kubeocean syncer to manager")
		os.Exit(1)
	}

	// Log client configuration
	setupLog.Info("Client configuration",
		"virtualClientQPS", virtualClientQPS,
		"virtualClientBurst", virtualClientBurst,
		"physicalClientQPS", physicalClientQPS,
		"physicalClientBurst", physicalClientBurst,
	)

	setupLog.Info("starting syncer manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

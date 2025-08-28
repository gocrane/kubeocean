package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"go.uber.org/zap/zapcore"
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

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	"github.com/TKEColocation/tapestry/pkg/syncer"
	corev1 "k8s.io/api/core/v1"
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
	var clusterBindingName string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for syncer. "+
			"Enabling this will ensure there is only one active syncer instance per ClusterBinding.")
	flag.StringVar(&leaderElectionID, "leader-election-id", "",
		"The name of the leader election ID to use. If empty, will be generated from cluster-binding-name.")
	flag.StringVar(&clusterBindingName, "cluster-binding-name", "",
		"The name of the ClusterBinding resource this syncer is responsible for.")

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
		leaderElectionID = fmt.Sprintf("tapestry-syncer-%s", clusterBindingName)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Get clusterBinding to determine clusterID for label filtering
	config := ctrl.GetConfigOrDie()
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

	// Initialize the Tapestry Syncer
	tapestrySyncer, err := syncer.NewTapestrySyncer(mgr, mgr.GetClient(), mgr.GetScheme(), clusterBindingName)
	if err != nil {
		setupLog.Error(err, "unable to create tapestry syncer")
		os.Exit(1)
	}

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

	// Add the tapestry syncer as a runnable to the manager
	if err := mgr.Add(tapestrySyncer); err != nil {
		setupLog.Error(err, "unable to add tapestry syncer to manager")
		os.Exit(1)
	}

	setupLog.Info("starting syncer manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

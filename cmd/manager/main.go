// Copyright Red Hat

package manager

import (
	"os"

	singaporev1alpha1 "github.com/stolostron/cluster-registration-operator/api/singapore/v1alpha1"
	"github.com/stolostron/cluster-registration-operator/pkg/helpers"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	clusterapiv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	clusterreg "github.com/stolostron/cluster-registration-operator/controllers/cluster-registration"
	workspace "github.com/stolostron/cluster-registration-operator/controllers/workspace"

	"github.com/spf13/cobra"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

type managerOptions struct {
	metricsAddr          string
	probeAddr            string
	enableLeaderElection bool
}

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterapiv1.AddToScheme(scheme)
	_ = singaporev1alpha1.AddToScheme(scheme)

	// +kubebuilder:scaffold:scheme
}

func NewManager() *cobra.Command {
	o := &managerOptions{}
	cmd := &cobra.Command{
		Use:   "manager",
		Short: "manager for cluster-registration-operator",
		Run: func(cmd *cobra.Command, args []string) {
			o.run()
			os.Exit(1)
		},
	}
	cmd.Flags().StringVar(&o.metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	cmd.Flags().StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	cmd.Flags().BoolVar(&o.enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	return cmd
}

func (o *managerOptions) run() {

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	setupLog.Info("Setup Manager")

	// Get REST config for MCE cluster
	// TODO - read from a configmap or otherwise
	// TODO - support multiple clusters
	kubeconfig, err := os.ReadFile("mce-kubeconfig") // Add a kubeconfig file called mce-kubeconfig to base directory of repo or update to path to your kubeconfig
	if err != nil {
		setupLog.Error(err, "unable to read kubeconfig for MCE cluster")
		os.Exit(1)
	}
	mceKubeconfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		setupLog.Error(err, "unable to create REST config for MCE cluster")
		os.Exit(1)
	}

	// Add MCE cluster
	mceCluster, err := cluster.New(mceKubeconfig,
		func(o *cluster.Options) {
			o.Scheme = scheme // Explicitly set the scheme which includes ManagedCluster
		},
	)
	if err != nil {
		setupLog.Error(err, "unable to setup MCE cluster")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     o.metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: o.probeAddr,
		LeaderElection:         o.enableLeaderElection,
		LeaderElectionID:       "628f2987.cluster-registratiion.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Add MCE cluster to manager
	if err := mgr.Add(mceCluster); err != nil {
		setupLog.Error(err, "unable to add MCE cluster")
		os.Exit(1)
	}

	setupLog.Info("Add RegisteredCluster reconciler")

	mceInstance := helpers.MceInstance{
		Cluster:            mceCluster,
		Client:             mceCluster.GetClient(),
		APIReader:          mceCluster.GetAPIReader(),
		KubeClient:         kubernetes.NewForConfigOrDie(mceKubeconfig),
		DynamicClient:      dynamic.NewForConfigOrDie(mceKubeconfig),
		APIExtensionClient: apiextensionsclient.NewForConfigOrDie(mceKubeconfig),
	}

	if err = (&clusterreg.RegisteredClusterReconciler{
		Client:             mgr.GetClient(),
		KubeClient:         kubernetes.NewForConfigOrDie(ctrl.GetConfigOrDie()),
		DynamicClient:      dynamic.NewForConfigOrDie(ctrl.GetConfigOrDie()),
		APIExtensionClient: apiextensionsclient.NewForConfigOrDie(ctrl.GetConfigOrDie()),
		Log:                ctrl.Log.WithName("controllers").WithName("RegistredCluster"),
		Scheme:             mgr.GetScheme(),
		MceCluster:         []helpers.MceInstance{mceInstance},
	}).SetupWithManager(mgr, mceCluster); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Cluster Registration")
		os.Exit(1)
	}

	setupLog.Info("Add workspace reconciler")

	if err = (&workspace.WorkspaceReconciler{
		Client:             mgr.GetClient(),
		KubeClient:         kubernetes.NewForConfigOrDie(ctrl.GetConfigOrDie()),
		DynamicClient:      dynamic.NewForConfigOrDie(ctrl.GetConfigOrDie()),
		APIExtensionClient: apiextensionsclient.NewForConfigOrDie(ctrl.GetConfigOrDie()),
		Log:                ctrl.Log.WithName("controllers").WithName("Workspace"),
		Scheme:             mgr.GetScheme(),
		MceClusters:        []helpers.MceInstance{mceInstance},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "workspace")
		os.Exit(1)
	}

	// add healthz/readyz check handler
	setupLog.Info("Add health check")
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add healthz check handler ")
		os.Exit(1)
	}

	setupLog.Info("Add ready check")
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add readyz check handler ")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

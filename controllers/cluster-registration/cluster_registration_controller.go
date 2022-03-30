// Copyright Red Hat

package registeredcluster

import (
	"context"
	"errors"
	"os"

	"github.com/go-logr/logr"
	singaporev1alpha1 "github.com/stolostron/cluster-registration-operator/api/singapore/v1alpha1"
	"github.com/stolostron/cluster-registration-operator/pkg/helpers"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clusterapiv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// +kubebuilder:rbac:groups="",resources={secrets},verbs=get;list;watch
// +kubebuilder:rbac:groups="singapore.open-cluster-management.io",resources={hubconfigs},verbs=get;list;watch
// +kubebuilder:rbac:groups="singapore.open-cluster-management.io",resources={registeredclusters},verbs=get;list;watch;create;update;delete

// +kubebuilder:rbac:groups="coordination.k8s.io",resources={leases},verbs=get;list;create;update;patch;delete;watch
// +kubebuilder:rbac:groups="";events.k8s.io,resources=events,verbs=create;update;patch

// RegisteredClusterReconciler reconciles a RegisteredCluster object
type RegisteredClusterReconciler struct {
	client.Client
	KubeClient         kubernetes.Interface
	DynamicClient      dynamic.Interface
	APIExtensionClient apiextensionsclient.Interface
	Log                logr.Logger
	Scheme             *runtime.Scheme
}

func (r *RegisteredClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.Info("Reconciling...")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.

func (r *RegisteredClusterReconciler) SetupWithManager(mgr ctrl.Manager, scheme *runtime.Scheme) ([]helpers.HubInstance, error) {
	r.Log.Info("setup registeredCluster manager")
	r.Log.Info("create dynamic client")
	dynamicClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}

	r.Log.Info("create kube client")
	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}

	r.Log.Info("retrieve POD namespace")
	namespace := os.Getenv("POD_NAMESPACE")
	if len(namespace) == 0 {
		err := errors.New("POD_NAMESPACE not defined")
		return nil, err
	}

	gvr := schema.GroupVersionResource{Group: "singapore.open-cluster-management.io", Version: "v1alpha1", Resource: "hubconfigs"}

	r.Log.Info("retrieve list of hubConfig")
	hubConfigListU, err := dynamicClient.Resource(gvr).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	r.Log.Info("nb of hubConfig unstructured found", "sze", len(hubConfigListU.Items))

	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&singaporev1alpha1.RegisteredCluster{})

	hubInstances := make([]helpers.HubInstance, 0)

	for _, hubConfigU := range hubConfigListU.Items {
		r.Log.Info("convert to hubConfig structure", "name", hubConfigU.GetName())
		hubConfig := &singaporev1alpha1.HubConfig{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(hubConfigU.Object, hubConfig); err != nil {
			return nil, err
		}

		r.Log.Info("get config secret", "name", hubConfig.Spec.KubeConfigSecretRef.Name)
		configSecret, err := kubeClient.CoreV1().Secrets(hubConfig.Namespace).Get(context.TODO(),
			hubConfig.Spec.KubeConfigSecretRef.Name,
			metav1.GetOptions{})
		if err != nil {
			r.Log.Error(err, "unable to read kubeconfig secret for MCE cluster",
				"HubConfig Name", hubConfig.GetName(),
				"HubConfig Secret Name", hubConfig.Spec.KubeConfigSecretRef.Name)
			return nil, err
		}

		r.Log.Info("generate hubKubeConfig")
		hubKubeconfig, err := clientcmd.RESTConfigFromKubeConfig(configSecret.Data["kubeConfig"])
		if err != nil {
			r.Log.Error(err, "unable to create REST config for MCE cluster")
			return nil, err
		}

		// Add MCE cluster
		hubCluster, err := cluster.New(hubKubeconfig,
			func(o *cluster.Options) {
				o.Scheme = scheme // Explicitly set the scheme which includes ManagedCluster
			},
		)
		if err != nil {
			r.Log.Error(err, "unable to setup MCE cluster")
			return nil, err
		}

		hubInstance := helpers.HubInstance{
			Cluster:            hubCluster,
			KubeClient:         kubernetes.NewForConfigOrDie(hubKubeconfig),
			DynamicClient:      dynamic.NewForConfigOrDie(hubKubeconfig),
			APIExtensionClient: apiextensionsclient.NewForConfigOrDie(hubKubeconfig),
		}

		hubInstances = append(hubInstances, hubInstance)
		// Add MCE cluster to manager
		if err := mgr.Add(hubCluster); err != nil {
			r.Log.Error(err, "unable to add MCE cluster")
			return nil, err
		}

		r.Log.Info("add watcher for ", "hubConfig.Name", hubConfig.Name)
		controllerBuilder.Watches(source.NewKindWithCache(&clusterapiv1.ManagedCluster{}, hubCluster.GetCache()), handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
			managedCluster := o.(*clusterapiv1.ManagedCluster)
			// Just log it for now...
			r.Log.Info("managedCluster", "name", managedCluster.Name)
			req := make([]reconcile.Request, 0)
			req = append(req, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      managedCluster.Name,
					Namespace: hubConfig.Namespace,
				},
			})
			return req
		}))
	}

	return hubInstances, controllerBuilder.
		Complete(r)
}

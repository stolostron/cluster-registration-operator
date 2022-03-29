// Copyright Red Hat

package registeredcluster

import (
	"context"

	"github.com/ghodss/yaml"
	"github.com/go-logr/logr"
	giterrors "github.com/pkg/errors"
	"github.com/stolostron/cluster-registration-operator/deploy"
	"github.com/stolostron/cluster-registration-operator/pkg/helpers"

	corev1 "k8s.io/api/core/v1"

	clusteradmapply "open-cluster-management.io/clusteradm/pkg/helpers/apply"
	// corev1 "k8s.io/api/core/v1"
	singaporev1alpha1 "github.com/stolostron/cluster-registration-operator/api/singapore/v1alpha1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clusterapiv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// RegisteredClusterReconciler reconciles a RegisteredCluster object
type RegisteredClusterReconciler struct {
	client.Client
	KubeClient         kubernetes.Interface
	DynamicClient      dynamic.Interface
	APIExtensionClient apiextensionsclient.Interface
	Log                logr.Logger
	Scheme             *runtime.Scheme
	MceCluster         []helpers.MceInstance
}

func (r *RegisteredClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.Info("Reconciling...")

	instance := &singaporev1alpha1.RegisteredCluster{}

	if err := r.Client.Get(
		context.TODO(),
		types.NamespacedName{Namespace: req.Namespace, Name: req.Name},
		instance,
	); err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, giterrors.WithStack(err)
	}

	// create managecluster on creation of registeredcluster CR
	if err := r.createManagedCluster(req.Name, ctx); err != nil {
		logger.Error(err, "failed to create ManagedCluster")

		return ctrl.Result{}, err
	}

	// update status of registeredcluster - add import command
	if err := r.updateImportCommand(instance, req.Name, req.Namespace, ctx); err != nil {
		logger.Error(err, "failed to update import command")

		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *RegisteredClusterReconciler) updateImportCommand(regCluster *singaporev1alpha1.RegisteredCluster, name string, namespace string, ctx context.Context) error {
	// get import secret from mce managecluster namespace

	importSecret := &corev1.Secret{}
	if err := r.MceCluster[0].APIReader.Get(ctx, types.NamespacedName{Namespace: name, Name: name + "-import"}, importSecret); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return giterrors.WithStack(err)
	}

	applierBuilder := &clusteradmapply.ApplierBuilder{}
	applier := applierBuilder.WithClient(r.KubeClient, r.APIExtensionClient, r.DynamicClient).Build()
	readerDeploy := deploy.GetScenarioResourcesReader()

	files := []string{
		"cluster-registration-operator/import_configmap.yaml",
	}

	// Get yaml representation of import command
	crdsYaml, err := yaml.Marshal(importSecret.Data["crds.yaml"])
	crdsv1Yaml, err := yaml.Marshal(importSecret.Data["crdsv1.yaml"])

	crdsv1beta1Yaml, err := yaml.Marshal(importSecret.Data["crdsv1beta1.yaml"])

	importYaml, err := yaml.Marshal(importSecret.Data["import.yaml"])

	values := struct {
		Name        string
		Namespace   string
		CrdsYaml    string
		CrdsV1Yaml  string
		CrdsV1beta1 string
		ImportYaml  string
	}{
		Name:        name,
		Namespace:   namespace,
		CrdsYaml:    string(crdsYaml),
		CrdsV1Yaml:  string(crdsv1Yaml),
		CrdsV1beta1: string(crdsv1beta1Yaml),
		ImportYaml:  string(importYaml),
	}

	_, err = applier.ApplyDirectly(readerDeploy, values, false, "", files...)
	if err != nil {
		return giterrors.WithStack(err)
	}

	// patch := client.MergeFrom(regCluster.DeepCopy())
	regCluster.Status.ImportCommandRef = corev1.LocalObjectReference{
		Name: name + "-import",
	}

	// patch := []byte(fmt.Sprintf(`{"status":{"importCommandRef":{"name:":"%s"}}`, name+"-import"))
	// err = r.Client.Status().Patch(context.TODO(), regCluster, client.RawPatch(types.MergePatchType, patch))
	// if err != nil {
	// 	fmt.Println("err: ", err)
	// 	return err
	// }

	// return giterrors.WithStack(r.Client.Status().Patch(context.TODO(), regCluster, patch))
	if err := r.Client.Update(context.TODO(), regCluster, &client.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

func (r *RegisteredClusterReconciler) createManagedCluster(name string, ctx context.Context) error {
	applierBuilder := &clusteradmapply.ApplierBuilder{}
	applier := applierBuilder.WithClient(r.MceCluster[0].KubeClient, r.MceCluster[0].APIExtensionClient, r.MceCluster[0].DynamicClient).Build()
	readerDeploy := deploy.GetScenarioResourcesReader()

	files := []string{
		"cluster-registration-operator/managed_cluster.yaml",
	}

	values := struct {
		Name string
	}{
		Name: name,
	}

	_, err := applier.ApplyCustomResources(readerDeploy, values, false, "", files...)
	if err != nil {
		return giterrors.WithStack(err)
	}
	return nil
}

func registeredClusterPredicate() predicate.Predicate {
	return predicate.Predicate(predicate.Funcs{
		GenericFunc: func(e event.GenericEvent) bool { return false },
		CreateFunc: func(e event.CreateEvent) bool {

			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return false
		},
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *RegisteredClusterReconciler) SetupWithManager(mgr ctrl.Manager, mceCluster cluster.Cluster) error {
	clusterapiv1.AddToScheme(r.Scheme)
	return ctrl.NewControllerManagedBy(mgr).
		For(&singaporev1alpha1.RegisteredCluster{}, builder.WithPredicates(registeredClusterPredicate())).
		Watches(source.NewKindWithCache(&clusterapiv1.ManagedCluster{}, mceCluster.GetCache()), handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
			managedCluster := o.(*clusterapiv1.ManagedCluster)
			// Just log it for now...
			r.Log.Info("managedCluster", "name", managedCluster.Name)
			req := make([]reconcile.Request, 0)
			return req
		})).
		Complete(r)
}

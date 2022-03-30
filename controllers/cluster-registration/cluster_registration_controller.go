// Copyright Red Hat

package registeredcluster

import (
	"context"
	"time"

	"github.com/ghodss/yaml"
	"github.com/go-logr/logr"
	giterrors "github.com/pkg/errors"
	"github.com/stolostron/cluster-registration-operator/deploy"
	"github.com/stolostron/cluster-registration-operator/pkg/helpers"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

const (
	RegisteredClusterNamelabel      string = "registeredcluster.singapore.open-cluster-management.io/name"
	RegisteredClusterNamespacelabel string = "registeredcluster.singapore.open-cluster-management.io/namespace"
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
	if err := r.createManagedCluster(instance, ctx); err != nil {
		logger.Error(err, "failed to create ManagedCluster")

		return ctrl.Result{}, err
	}

	// update status of registeredcluster - add import command
	if err := r.updateImportCommand(instance, ctx); err != nil {
		logger.Error(err, "failed to update import command")
		if errors.IsNotFound(err) {
			return reconcile.Result{Requeue: true, RequeueAfter: 1 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *RegisteredClusterReconciler) updateImportCommand(regCluster *singaporev1alpha1.RegisteredCluster, ctx context.Context) error {

	// get the managedcluster nameespace
	managedClusterList := &clusterapiv1.ManagedClusterList{}
	if err := r.MceCluster[0].Client.List(context.Background(), managedClusterList, client.MatchingLabels{RegisteredClusterNamelabel: regCluster.Name, RegisteredClusterNamespacelabel: regCluster.Namespace}); err != nil {
		// Error reading the object - requeue the request.
		return giterrors.WithStack(err)
	}

	if len(managedClusterList.Items) == 1 {
		managedclusterNamespace := managedClusterList.Items[0].Name
		// get import secret from mce managecluster namespace
		importSecret := &corev1.Secret{}
		if err := r.MceCluster[0].APIReader.Get(ctx, types.NamespacedName{Namespace: managedclusterNamespace, Name: managedclusterNamespace + "-import"}, importSecret); err != nil {
			if errors.IsNotFound(err) {
				return err
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
			Name:        regCluster.Name,
			Namespace:   regCluster.Namespace,
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
			Name: regCluster.Name + "-import",
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
	}
	return nil
}

func (r *RegisteredClusterReconciler) createManagedCluster(regCluster *singaporev1alpha1.RegisteredCluster, ctx context.Context) error {

	// check if managedcluster is already exists
	managedClusterList := &clusterapiv1.ManagedClusterList{}
	if err := r.MceCluster[0].Client.List(context.Background(), managedClusterList, client.MatchingLabels{RegisteredClusterNamelabel: regCluster.Name, RegisteredClusterNamespacelabel: regCluster.Namespace}); err != nil {
		// Error reading the object - requeue the request.
		return giterrors.WithStack(err)
	}

	if len(managedClusterList.Items) < 1 {
		managedCluster := &clusterapiv1.ManagedCluster{
			TypeMeta: metav1.TypeMeta{
				APIVersion: clusterapiv1.SchemeGroupVersion.String(),
				Kind:       "ManagedCluster",
			},
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "registered-cluster-",
				Labels: map[string]string{
					RegisteredClusterNamelabel:      regCluster.Name,
					RegisteredClusterNamespacelabel: regCluster.Namespace,
				},
			},
			Spec: clusterapiv1.ManagedClusterSpec{
				HubAcceptsClient: true,
			},
		}

		if err := r.MceCluster[0].Client.Create(context.TODO(), managedCluster, &client.CreateOptions{}); err != nil {
			return err
		}
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

func managedClusterPredicate() predicate.Predicate {
	f := func(obj client.Object) bool {
		log := ctrl.Log.WithName("controllers").WithName("ManagedCluster").WithName("managedClusterPredicate").WithValues("namespace", obj.GetNamespace(), "name", obj.GetName())
		if _, ok := obj.GetLabels()[RegisteredClusterNamelabel]; ok {
			if _, ok := obj.GetLabels()[RegisteredClusterNamespacelabel]; ok {
				log.V(1).Info("process managedcluster")
				return true
			}

		}
		return false
	}

	return predicate.Funcs{
		CreateFunc: func(event event.CreateEvent) bool {
			return f(event.Object)
		},
		UpdateFunc: func(event event.UpdateEvent) bool {
			return f(event.ObjectNew)
		},
		GenericFunc: func(event event.GenericEvent) bool {
			return f(event.Object)
		},
		DeleteFunc: func(event event.DeleteEvent) bool {
			return f(event.Object)
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RegisteredClusterReconciler) SetupWithManager(mgr ctrl.Manager, mceCluster cluster.Cluster) error {
	clusterapiv1.AddToScheme(r.Scheme) // not needed? set in main
	return ctrl.NewControllerManagedBy(mgr).
		For(&singaporev1alpha1.RegisteredCluster{}, builder.WithPredicates(registeredClusterPredicate())).
		Watches(source.NewKindWithCache(&clusterapiv1.ManagedCluster{}, mceCluster.GetCache()), handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
			managedCluster := o.(*clusterapiv1.ManagedCluster)
			// Just log it for now...
			r.Log.Info("managedCluster", "name", managedCluster.Name)

			req := make([]reconcile.Request, 0)
			req = append(req, reconcile.Request{types.NamespacedName{Name: managedCluster.Name}})
			return req
		}), builder.WithPredicates(managedClusterPredicate())).
		Complete(r)
}

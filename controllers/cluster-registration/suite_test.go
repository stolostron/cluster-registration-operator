// Copyright Red Hat
package registeredcluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ghodss/yaml"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/scheme"
	clusterapiv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	clusteradmasset "open-cluster-management.io/clusteradm/pkg/helpers/asset"

	croconfig "github.com/stolostron/cluster-registration-operator/config"

	singaporev1alpha1 "github.com/stolostron/cluster-registration-operator/api/singapore/v1alpha1"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

const (
	userNamespace string = "user-namespace"
)

var (
	cfg       *rest.Config
	r         *RegisteredClusterReconciler
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
	mgr       ctrl.Manager
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	// fetch the current config
	suiteConfig, reporterConfig := GinkgoConfiguration()
	// adjust it
	suiteConfig.SkipStrings = []string{"NEVER-RUN"}
	reporterConfig.FullTrace = true
	RunSpecs(t,
		"Controller Suite",
		reporterConfig)
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	// ctx, cancel = context.WithCancel(context.TODO())
	// ctx, cancel = context.WithCancel(ctrl.SetupSignalHandler())

	By("bootstrapping test environment")
	err := clientgoscheme.AddToScheme(scheme.Scheme)
	Expect(err).Should(BeNil())
	err = appsv1.AddToScheme(scheme.Scheme)
	Expect(err).Should(BeNil())
	err = clusterapiv1.AddToScheme(scheme.Scheme)
	Expect(err).Should(BeNil())
	err = singaporev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).Should(BeNil())

	// err = openshiftconfigv1.AddToScheme(scheme.Scheme)
	// Expect(err).NotTo(HaveOccurred())

	readerIDP := croconfig.GetScenarioResourcesReader()
	clusterRegistrarsCRD, err := getCRD(readerIDP, "crd/singapore.open-cluster-management.io_clusterregistrars.yaml")
	Expect(err).Should(BeNil())

	hubConfigsCRD, err := getCRD(readerIDP, "crd/singapore.open-cluster-management.io_hubconfigs.yaml")
	Expect(err).Should(BeNil())

	registeredClustersCRD, err := getCRD(readerIDP, "crd/singapore.open-cluster-management.io_registeredclusters.yaml")
	Expect(err).Should(BeNil())

	testEnv = &envtest.Environment{
		Scheme: scheme.Scheme,
		CRDs: []*apiextensionsv1.CustomResourceDefinition{
			clusterRegistrarsCRD,
			hubConfigsCRD,
			registeredClustersCRD,
		},
		// CRDDirectoryPaths: []string{
		// 	filepath.Join("..", "..", "test", "config", "crd", "external"),
		// },
		ErrorIfCRDPathMissing:    true,
		AttachControlPlaneOutput: true,
		ControlPlaneStartTimeout: 1 * time.Minute,
		ControlPlaneStopTimeout:  1 * time.Minute,
	}

	cfg, err = testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(k8sClient).ToNot(BeNil())

	mgr, err = ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(mgr).ToNot(BeNil())

	By("Init the controller", func() {
		r = &RegisteredClusterReconciler{
			Client:             k8sClient,
			KubeClient:         kubernetes.NewForConfigOrDie(cfg),
			DynamicClient:      dynamic.NewForConfigOrDie(cfg),
			APIExtensionClient: apiextensionsclient.NewForConfigOrDie(cfg),
			Log:                logf.Log,
			Scheme:             scheme.Scheme,
		}
		err := r.SetupWithManager(mgr, scheme.Scheme)
		Expect(err).To(BeNil())
	})

	go func() {
		defer GinkgoRecover()
		ctx, cancel = context.WithCancel(ctrl.SetupSignalHandler())
		err = mgr.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()

})

var _ = AfterSuite(func() {
	// cancel()
	By("tearing down the test environment")
	fmt.Println("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

var _ = Describe("Process registeredCluster: ", func() {
	It("Process registeredCluster creation", func() {
		By(fmt.Sprintf("creation of namespace %s", userNamespace), func() {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: userNamespace,
				},
			}
			err := k8sClient.Create(context.TODO(), ns)
			Expect(err).To(BeNil())
		})
		var registeredCluster *singaporev1alpha1.RegisteredCluster
		By("Create the RegisteredCluster", func() {
			registeredCluster = &singaporev1alpha1.RegisteredCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "registered-cluster",
					Namespace: userNamespace,
				},
				Spec: singaporev1alpha1.RegisteredClusterSpec{},
			}
			err := k8sClient.Create(context.TODO(), registeredCluster)
			Expect(err).To(BeNil())
		})
		By("Checking managedCluster", func() {
			Eventually(func() error {
				managedClusters := &clusterapiv1.ManagedClusterList{}

				if err := k8sClient.List(context.TODO(),
					managedClusters,
					client.MatchingLabels{
						RegisteredClusterNamelabel:      registeredCluster.Name,
						RegisteredClusterNamespacelabel: registeredCluster.Namespace,
					}); err != nil {
					logf.Log.Info("Waiting managedCluster", "Error", err)
					return err
				}
				if len(managedClusters.Items) != 1 {
					return fmt.Errorf("Number of managedCluster found %d", len(managedClusters.Items))
				}
				return nil
			}, 30, 1).Should(BeNil())
		})
	})

})

func getCRD(reader *clusteradmasset.ScenarioResourcesReader, file string) (*apiextensionsv1.CustomResourceDefinition, error) {
	b, err := reader.Asset(file)
	if err != nil {
		return nil, err
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(b, crd); err != nil {
		return nil, err
	}
	return crd, nil
}

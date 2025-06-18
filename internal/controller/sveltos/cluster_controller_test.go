// Copyright 2025
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sveltos

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	apiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var _ = Describe("SveltosCluster Controller Integration Tests", func() {
	var (
		ctx     context.Context
		cancel  context.CancelFunc
		testEnv *envtest.Environment
		cl      client.Client
		config  *rest.Config
	)

	BeforeEach(func() {
		RegisterFailHandler(Fail)

		logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		ctx, cancel = context.WithCancel(context.TODO())
		testEnv = &envtest.Environment{
			CRDDirectoryPaths: []string{
				filepath.Join("..", "..", "..", "templates", "provider", "kcm", "templates", "crds"),
				filepath.Join("..", "..", "..", "bin", "crd"),
			},
			ErrorIfCRDPathMissing: true,

			// The BinaryAssetsDirectory is only required if you want to run the tests directly
			// without call the makefile target test. If not informed it will look for the
			// default path defined in controller-runtime which is /usr/local/kubebuilder/.
			// Note that you must have the required binaries setup under the bin directory to perform
			// the tests directly. When we run make test it will be setup and used automatically.
			BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
				fmt.Sprintf("1.33.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
		}

		cfg, err := testEnv.Start()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		config = cfg

		Expect(libsveltosv1beta1.AddToScheme(scheme.Scheme)).To(Succeed())

		cl, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		Expect(cl).NotTo(BeNil())
	})

	AfterEach(func() {
		Expect(testEnv.Stop()).NotTo(HaveOccurred())
		cancel()
	})

	It("Should create a new TokenRequest and update the secret", func() {
		const (
			testClusterName = "test-cluster"
			testSAName      = "test-sa"
		)

		timeBeforeNode := time.Now().UTC().Add(-time.Hour)

		// Create a test SveltosCluster
		sveltosCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testClusterName,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				TokenRequestRenewalOption: &libsveltosv1beta1.TokenRequestRenewalOption{
					SANamespace: metav1.NamespaceDefault,
					SAName:      testSAName,
				},
			},
		}
		Expect(cl.Create(ctx, sveltosCluster)).NotTo(HaveOccurred())

		// Create SA to generate TokenRequest for
		Expect(cl.Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testSAName,
				Namespace: metav1.NamespaceDefault,
			},
		})).NotTo(HaveOccurred())

		// Create Sveltos Secret with a fake data
		secretName, _, err := clusterproxy.GetSveltosSecretNameAndKey(ctx, logf.Log, cl, sveltosCluster.Namespace, sveltosCluster.Name)
		Expect(err).NotTo(HaveOccurred())

		fakeBB, err := json.Marshal(&apiv1.Config{
			APIVersion: "v1",
			Kind:       "Config",
			Contexts: []apiv1.NamedContext{
				{
					Name: "fake",
					Context: apiv1.Context{
						AuthInfo:  testSAName,
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		sveltosSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: sveltosCluster.Namespace,
			},
			Data: map[string][]byte{"kubeconfig": fakeBB},
		}
		Expect(cl.Create(ctx, sveltosSecret)).NotTo(HaveOccurred())

		// Run the controller
		_, err = (&ClusterReconciler{
			Client: cl,
			config: config,
		}).Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(sveltosCluster),
		})
		Expect(err).NotTo(HaveOccurred())

		// Verify the KubeconfigKeyName and LastReconciledTokenRequestAt is updated
		const renewalConfigKey = "re-kubeconfig"
		Eventually(func() bool {
			updatedCluster := new(libsveltosv1beta1.SveltosCluster)
			if err := cl.Get(ctx, client.ObjectKeyFromObject(sveltosCluster), updatedCluster); err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get SveltosCluster: %v", err)
				return false
			}
			updatedAt, err := time.Parse(time.RFC3339, updatedCluster.Status.LastReconciledTokenRequestAt)
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to parse LastReconciledTokenRequestAt: %v", err)
				return false
			}

			if updatedCluster.Spec.KubeconfigKeyName != renewalConfigKey {
				_, _ = fmt.Fprintf(GinkgoWriter, "KubeconfigKeyName %s != %s", updatedCluster.Spec.KubeconfigKeyName, renewalConfigKey)
				return false
			}

			if !updatedAt.After(timeBeforeNode) {
				_, _ = fmt.Fprintf(GinkgoWriter, "LastReconciledTokenRequestAt %s is not after %s", updatedCluster.Status.LastReconciledTokenRequestAt, timeBeforeNode.Format(time.RFC3339))
				return false
			}

			return true
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(BeTrue())

		// Verify the sveltos secret is updated
		updatedSecret := new(corev1.Secret)
		Expect(cl.Get(ctx, client.ObjectKeyFromObject(sveltosSecret), updatedSecret)).NotTo(HaveOccurred())

		// Verify the token is present in the secret
		Expect(updatedSecret.Data).To(HaveKey(renewalConfigKey))
	})
})

func TestControllerIntegration(t *testing.T) {
	RunSpecs(t, "SveltosCluster Controller Integration Tests")
}

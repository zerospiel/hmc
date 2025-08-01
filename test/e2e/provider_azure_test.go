// Copyright 2024
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

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	internalutils "github.com/K0rdent/kcm/internal/utils"
	"github.com/K0rdent/kcm/test/e2e/clusterdeployment"
	"github.com/K0rdent/kcm/test/e2e/clusterdeployment/azure"
	"github.com/K0rdent/kcm/test/e2e/config"
	"github.com/K0rdent/kcm/test/e2e/credential"
	"github.com/K0rdent/kcm/test/e2e/kubeclient"
	"github.com/K0rdent/kcm/test/e2e/logs"
	"github.com/K0rdent/kcm/test/e2e/templates"
	"github.com/K0rdent/kcm/test/e2e/upgrade"
	"github.com/K0rdent/kcm/test/utils"
)

var _ = Context("Azure Templates", Label("provider:cloud", "provider:azure"), Ordered, func() {
	var (
		kc                    *kubeclient.KubeClient
		standaloneClusters    []string
		hostedDeleteFuncs     []func() error
		standaloneDeleteFuncs []func() error
		kubeconfigDeleteFuncs []func() error

		providerConfigs []config.ProviderTestingConfig
	)

	BeforeAll(func() {
		By("Get testing configuration")
		providerConfigs = config.Config[config.TestingProviderAzure]

		if len(providerConfigs) == 0 {
			Skip("Azure ClusterDeployment testing is skipped")
		}

		By("Ensuring that env vars are set correctly")
		azure.CheckEnv()

		By("Creating kube client")
		kc = kubeclient.NewFromLocal(internalutils.DefaultSystemNamespace)

		By("Providing cluster identity and credentials")
		if slices.ContainsFunc(providerConfigs, func(providerConfig config.ProviderTestingConfig) bool {
			return templates.GetType(providerConfig.Template) == templates.TemplateAzureAKS
		}) {
			credential.Apply("", "aks")
		}

		if slices.ContainsFunc(providerConfigs, func(providerConfig config.ProviderTestingConfig) bool {
			return templates.GetType(providerConfig.Template) == templates.TemplateAzureStandaloneCP ||
				templates.GetType(providerConfig.Template) == templates.TemplateAzureHostedCP
		}) {
			credential.Apply("", "azure")
		}
	})

	AfterAll(func() {
		// If we failed collect the support bundle before the cleanup
		if CurrentSpecReport().Failed() && cleanup() {
			By("collecting the support bundle from the management cluster")
			logs.SupportBundle("")

			for _, clusterName := range standaloneClusters {
				By(fmt.Sprintf("collecting the support bundle from the %s cluster", clusterName))
				logs.SupportBundle(clusterName)
			}
		}

		if cleanup() {
			By("deleting resources")
			deleteFuncs := append(hostedDeleteFuncs, append(standaloneDeleteFuncs, kubeconfigDeleteFuncs...)...)
			for _, deleteFunc := range deleteFuncs {
				if deleteFunc != nil {
					err := deleteFunc()
					Expect(err).NotTo(HaveOccurred())
				}
			}
		}
	})

	It("should work with an Azure provider", func() {
		for i, testingConfig := range providerConfigs {
			_, _ = fmt.Fprintf(GinkgoWriter, "Testing configuration:\n%s\n", testingConfig.String())

			sdName := clusterdeployment.GenerateClusterName(fmt.Sprintf("azure-%d", i))
			sdTemplate := testingConfig.Template
			sdTemplateType := templates.GetType(sdTemplate)

			// Supported architectures for Azure standalone deployment: amd64, arm64
			Expect(testingConfig.Architecture).To(SatisfyAny(
				Equal(config.ArchitectureAmd64),
				Equal(config.ArchitectureArm64)),
				fmt.Sprintf("architecture should be either %s or %s", config.ArchitectureAmd64, config.ArchitectureArm64),
			)
			azure.PopulateEnvVars(testingConfig.Architecture)

			templateBy(sdTemplateType, fmt.Sprintf("creating a ClusterDeployment %s with template %s", sdName, sdTemplate))

			sd := clusterdeployment.Generate(templates.TemplateAzureStandaloneCP, sdName, sdTemplate)

			standaloneDeleteFunc := clusterdeployment.Create(context.Background(), kc.CrClient, sd)
			standaloneClusters = append(standaloneClusters, sdName)
			standaloneDeleteFuncs = append(standaloneDeleteFuncs, func() error {
				By(fmt.Sprintf("Deleting the %s ClusterDeployment", sdName))
				err := standaloneDeleteFunc()
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("Verifying the %s ClusterDeployment deleted successfully", sdName))
				deploymentValidator := clusterdeployment.NewProviderValidator(
					templates.TemplateAzureStandaloneCP,
					sdName,
					clusterdeployment.ValidationActionDelete,
				)

				Eventually(func() error {
					return deploymentValidator.Validate(context.Background(), kc)
				}).WithTimeout(10 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
				return nil
			})

			// verify the standalone cluster is deployed correctly
			deploymentValidator := clusterdeployment.NewProviderValidator(
				sdTemplateType,
				sdName,
				clusterdeployment.ValidationActionDeploy,
			)

			templateBy(sdTemplateType, "waiting for infrastructure provider to deploy successfully")
			Eventually(func() error {
				return deploymentValidator.Validate(context.Background(), kc)
			}).WithTimeout(90 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

			if !testingConfig.Upgrade && testingConfig.Hosted == nil {
				continue
			}

			standaloneClient := new(kubeclient.KubeClient)
			var hdName string
			if testingConfig.Hosted != nil {
				// setup environment variables for deploying the hosted template (subnet name, etc)
				azure.SetAzureEnvironmentVariables(sdName, kc)

				kubeCfgPath, _, kubecfgDeleteFunc := kc.WriteKubeconfig(context.Background(), sdName)
				kubeconfigDeleteFuncs = append(kubeconfigDeleteFuncs, kubecfgDeleteFunc)

				By("Deploy onto standalone cluster")
				GinkgoT().Setenv("KUBECONFIG", kubeCfgPath)
				cmd := exec.Command("make", "test-apply")
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.Unsetenv("KUBECONFIG")).To(Succeed())

				standaloneClient = kc.NewFromCluster(context.Background(), internalutils.DefaultSystemNamespace, sdName)

				// TODO: remove after https://github.com/k0rdent/kcm/issues/1575 is fixed
				if testingConfig.Architecture == config.ArchitectureArm64 {
					removeInfobloxProvider(standaloneClient.CrClient)
				}

				// verify the cluster is ready prior to creating credentials
				Eventually(func() error {
					err := verifyManagementReadiness(standaloneClient)
					if err != nil {
						_, _ = fmt.Fprintf(GinkgoWriter, "%v\n", err)
						return err
					}
					return nil
				}).WithTimeout(15 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

				if testingConfig.Hosted.Upgrade {
					By("installing stable templates for further hosted upgrade testing")
					_, err = utils.Run(exec.Command("make", "stable-templates"))
					Expect(err).NotTo(HaveOccurred())
				}

				// Ensure Cluster Templates in the standalone cluster are valid
				Eventually(func() error {
					err = clusterdeployment.ValidateClusterTemplates(context.Background(), standaloneClient)
					if err != nil {
						_, _ = fmt.Fprintf(GinkgoWriter, "cluster template validation failed: %v\n", err)
						return err
					}
					return nil
				}).WithTimeout(15 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

				By("Create azure credential secret")
				credential.Apply(kubeCfgPath, "azure")

				By("Create default storage class for azure-disk CSI driver")
				azure.CreateDefaultStorageClass(standaloneClient)

				// Supported architectures for Azure hosted deployment: amd64, arm64
				Expect(testingConfig.Hosted.Architecture).To(SatisfyAny(
					Equal(config.ArchitectureAmd64),
					Equal(config.ArchitectureArm64)),
					fmt.Sprintf("architecture should be either %s or %s", config.ArchitectureAmd64, config.ArchitectureArm64),
				)
				azure.PopulateEnvVars(testingConfig.Architecture)

				hdName = clusterdeployment.GenerateClusterName(fmt.Sprintf("azure-hosted-%d", i))
				hdTemplate := testingConfig.Hosted.Template
				templateBy(templates.TemplateAzureHostedCP, fmt.Sprintf("creating a hosted ClusterDeployment %s with template %s", hdName, hdTemplate))

				hd := clusterdeployment.Generate(templates.TemplateAzureHostedCP, hdName, hdTemplate)

				templateBy(templates.TemplateAzureHostedCP, "creating a ClusterDeployment")
				hostedDeleteFunc := clusterdeployment.Create(context.Background(), standaloneClient.CrClient, hd)
				hostedDeleteFuncs = append(hostedDeleteFuncs, func() error {
					By(fmt.Sprintf("Deleting the %s ClusterDeployment", hdName))
					err = hostedDeleteFunc()
					Expect(err).NotTo(HaveOccurred())

					By(fmt.Sprintf("Verifying the %s ClusterDeployment deleted successfully", hdName))
					deploymentValidator = clusterdeployment.NewProviderValidator(
						templates.TemplateAzureHostedCP,
						hdName,
						clusterdeployment.ValidationActionDelete,
					)
					Eventually(func() error {
						return deploymentValidator.Validate(context.Background(), standaloneClient)
					}).WithTimeout(10 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
					return nil
				})

				templateBy(templates.TemplateAzureHostedCP, "Patching AzureCluster to ready")
				clusterdeployment.PatchHostedClusterReady(standaloneClient, clusterdeployment.ProviderAzure, hdName)

				templateBy(templates.TemplateAzureHostedCP, "waiting for infrastructure to deploy successfully")
				deploymentValidator = clusterdeployment.NewProviderValidator(
					templates.TemplateAzureHostedCP,
					hdName,
					clusterdeployment.ValidationActionDeploy,
				)

				Eventually(func() error {
					return deploymentValidator.Validate(context.Background(), standaloneClient)
				}).WithTimeout(10 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
			}

			if testingConfig.Upgrade {
				clusterUpgrade := upgrade.NewClusterUpgrade(
					kc.CrClient,
					standaloneClient.CrClient,
					internalutils.DefaultSystemNamespace,
					sdName,
					testingConfig.UpgradeTemplate,
					upgrade.NewDefaultClusterValidator(),
				)
				clusterUpgrade.Run(context.Background())

				Eventually(func() error {
					return deploymentValidator.Validate(context.Background(), kc)
				}).WithTimeout(30 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

				if testingConfig.Hosted != nil {
					// Validate hosted deployment after the standalone upgrade
					Eventually(func() error {
						return deploymentValidator.Validate(context.Background(), standaloneClient)
					}).WithTimeout(30 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
				}
			}
			if testingConfig.Hosted != nil && testingConfig.Hosted.Upgrade {
				By(fmt.Sprintf("updating hosted cluster to the %s template", testingConfig.Hosted.UpgradeTemplate))

				hostedClient := standaloneClient.NewFromCluster(context.Background(), internalutils.DefaultSystemNamespace, hdName)
				clusterUpgrade := upgrade.NewClusterUpgrade(
					standaloneClient.CrClient,
					hostedClient.CrClient,
					internalutils.DefaultSystemNamespace,
					hdName,
					testingConfig.Hosted.UpgradeTemplate,
					upgrade.NewDefaultClusterValidator(),
				)
				clusterUpgrade.Run(context.Background())

				Eventually(func() error {
					return deploymentValidator.Validate(context.Background(), standaloneClient)
				}).WithTimeout(30 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
			}
		}
	})
})

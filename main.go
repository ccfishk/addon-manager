/*
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"flag"
	"os"

	"golang.org/x/net/context"
	"k8s.io/client-go/dynamic"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	addonmgrv1alpha1 "github.com/keikoproj/addon-manager/pkg/apis/addon/v1alpha1"

	addonclientset "github.com/keikoproj/addon-manager/pkg/client/clientset/versioned"

	"github.com/keikoproj/addon-manager/controllers"
	"github.com/keikoproj/addon-manager/pkg/apis/addon"
	"github.com/keikoproj/addon-manager/pkg/common"
	"github.com/keikoproj/addon-manager/pkg/version"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	// +kubebuilder:scaffold:imports
)

var (
	setupLog             = ctrl.Log.WithName("setup")
	debug                bool
	metricsAddr          string
	enableLeaderElection bool
)

func init() {
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&debug, "debug", false, "Debug logging")
	flag.Parse()
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(debug)))

	setupLog.Info(version.ToString())
	nonCached := []client.Object{
		&wfv1.Workflow{},
		&addonmgrv1alpha1.Addon{},
		&apiextensionsv1.CustomResourceDefinition{},
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                common.GetAddonMgrScheme(),
		MetricsBindAddress:    metricsAddr,
		LeaderElection:        enableLeaderElection,
		LeaderElectionID:      addon.Group,
		ClientDisableCacheFor: nonCached, // if any cache is used, to bypass it for the given objects
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	stopChan := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := clientcmd.BuildConfigFromFlags("", "/Users/jiminh/.kube/config")
	if cfg == nil {
		panic(err)
	}

	// config, err := rest.InClusterConfig()
	// if err != nil {
	// 	setupLog.Error(err, "unable to connect to client")
	// 	os.Exit(1)
	// }

	dynCli, err := dynamic.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}
	k8sCli, _ := common.NewK8sClient("/Users/jiminh/.kube/config")
	addoncli, err := addonclientset.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}

	stopCh := make(chan struct{})
	defer close(stopCh)

	namespace := "addon-manager-system"
	config := controllers.NewConfig(namespace, 5, 5, 5, dynCli, k8sCli, mgr.GetClient(), mgr.GetScheme(), addoncli)
	controllers.StartAddonController(ctx, config)

	// +kubebuilder:scaffold:builder
	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
	close(stopChan)
}

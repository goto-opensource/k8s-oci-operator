/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"errors"
	"flag"
	"os"

	ociv1alpha1 "github.com/logmein/k8s-oci-operator/api/v1alpha1"
	"github.com/logmein/k8s-oci-operator/controllers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	// +kubebuilder:scaffold:imports

	ocicommon "github.com/oracle/oci-go-sdk/v31/common"
	ociauth "github.com/oracle/oci-go-sdk/v31/common/auth"
	ocicore "github.com/oracle/oci-go-sdk/v31/core"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	corev1.AddToScheme(scheme)
	ociv1alpha1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr, leaderElectionID, leaderElectionNamespace, compartmentID, vcnID, reservedIPNamePrefix, ociConfigFile string
	var ipr bool
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&leaderElectionID, "leader-election-id", "k8s-oci-operator", "the name of the configmap do use as leader election lock")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "", "the namespace in which the leader election lock will be held")
	flag.StringVar(&compartmentID, "compartment-id", "", "OCI compartment ID")
	flag.StringVar(&vcnID, "vcn-id", "", "OCI Virtual Cloud Network (VCN) ID")
	flag.StringVar(&reservedIPNamePrefix, "reserved-ip-name-prefix", "", "Name prefix to add to all ReservedIPs created by this controller")
	flag.BoolVar(&ipr, "instance-principals", false, "Use instance principals to talk to OCI API")
	flag.StringVar(&ociConfigFile, "oci-config", "", "OCI config file to use")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if leaderElectionNamespace == "" {
		setupLog.Error(errors.New("-leader-election-namespace flag is required"), "command line flag validation failed")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		MetricsBindAddress:      metricsAddr,
		LeaderElection:          true,
		LeaderElectionNamespace: leaderElectionNamespace,
		LeaderElectionID:        leaderElectionID,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	var ocicfg ocicommon.ConfigurationProvider
	if ipr {
		ocicfg, err = ociauth.InstancePrincipalConfigurationProvider()
		if err != nil {
			setupLog.Error(err, "unable to create OCI config via instance principals")
			os.Exit(1)
		}
		setupLog.Info("using OCI instance principle client")
	} else if ociConfigFile != "" {
		ocicfg, err = ocicommon.ConfigurationProviderFromFile(ociConfigFile, "")
		if err != nil {
			setupLog.Error(err, "unable to create OCI config from config file")
			os.Exit(1)
		}
		setupLog.Info("using OCI configuration file", ociConfigFile)

	} else {
		ocicfg = ocicommon.DefaultConfigProvider()
		setupLog.Info("Using OCI default client")
	}

	vnc, err := ocicore.NewVirtualNetworkClientWithConfigurationProvider(ocicfg)
	if err != nil {
		setupLog.Error(err, "unable to create OCI client")
		os.Exit(1)
	}
	vnc.UserAgent = "k8s-oci-operator"

	err = (&controllers.ReservedIPReconciler{
		Client:               mgr.GetClient(),
		Recorder:             mgr.GetEventRecorderFor("k8s-oci-operator"),
		Log:                  ctrl.Log.WithName("controllers").WithName("ReservedIP"),
		CompartmentID:        compartmentID,
		VcnID:                vcnID,
		ReservedIPNamePrefix: reservedIPNamePrefix,
		VNC:                  &vnc,
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ReservedIP")
		os.Exit(1)
	}
	err = (&controllers.ReservedIPAssociationReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("ReservedIPAssociation"),
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ReservedIPAssociation")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

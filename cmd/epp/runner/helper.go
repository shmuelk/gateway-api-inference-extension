/*
Copyright 2025 The Kubernetes Authors.

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

package runner

import (
	"sync/atomic"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	healthPb "google.golang.org/grpc/health/grpc_health_v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/gateway-api-inference-extension/internal/runnable"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/common"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/saturationdetector/framework/plugins/utilizationdetector"
	runserver "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/server"
)

// EppRunnerHelper is the EPP's RunnerHelper
type EppRunnerHelper struct {
}

// CreateAndRegisterServer creates the exp_proc server and registers it with the manager
func (h *EppRunnerHelper) CreateAndRegisterServer(ds datastore.Datastore, opts *runserver.Options,
	gknn common.GKNN, director *requestcontrol.Director, saturationDetector *utilizationdetector.Detector,
	useExperimentalDatalayerV2 bool, mgr ctrl.Manager, logger logr.Logger) error {
	// --- Setup ExtProc Server Runner ---
	serverRunner := &runserver.ExtProcServerRunner{
		GrpcPort:                         opts.GRPCPort,
		GKNN:                             gknn,
		Datastore:                        ds,
		SecureServing:                    opts.SecureServing,
		HealthChecking:                   opts.HealthChecking,
		CertPath:                         opts.CertPath,
		EnableCertReload:                 opts.EnableCertReload,
		RefreshPrometheusMetricsInterval: opts.RefreshPrometheusMetricsInterval,
		MetricsStalenessThreshold:        opts.MetricsStalenessThreshold,
		Director:                         director,
		SaturationDetector:               saturationDetector,
		UseExperimentalDatalayerV2:       useExperimentalDatalayerV2,
	}

	// Register ext-proc server.
	if err := registerExtProcServer(mgr, serverRunner, logger); err != nil {
		return err
	}
	return nil
}

// registerExtProcServer adds the ExtProcServerRunner as a Runnable to the manager.
func registerExtProcServer(mgr manager.Manager, runner *runserver.ExtProcServerRunner, logger logr.Logger) error {
	if err := mgr.Add(runner.AsRunnable(ctrl.Log.WithName("ext-proc"))); err != nil {
		logger.Error(err, "Failed to register ext-proc gRPC server runnable")
		return err
	}
	logger.Info("ExtProc server runner added to manager.")
	return nil
}

// RegisterHealthServer adds the Health gRPC server as a Runnable to the given manager.
func (h *EppRunnerHelper) RegisterHealthServer(mgr manager.Manager, logger logr.Logger, ds datastore.Datastore, port int, isLeader *atomic.Bool, leaderElectionEnabled bool) error {
	srv := grpc.NewServer()
	healthPb.RegisterHealthServer(srv, &healthServer{
		logger:                logger,
		datastore:             ds,
		isLeader:              isLeader,
		leaderElectionEnabled: leaderElectionEnabled,
	})
	if err := mgr.Add(
		runnable.NoLeaderElection(runnable.GRPCServer("health", srv, port))); err != nil {
		setupLog.Error(err, "Failed to register health server")
		return err
	}
	return nil
}

// AddPlugins enables the helper access to plugins if needed
func (h *EppRunnerHelper) AddPlugins(plugins ...plugins.Plugin) {}

// SPDX-FileCopyrightText: The RamenDR authors
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Deployer interface has methods to deploy a workload to a cluster
type Deployer interface {
	Deploy(Context) error
	Undeploy(Context) error
	GetName() string
	// GetNamespace return the namespace for the ramen resources, or empty string if not using a special namespace.
	GetNamespace() string
	// Return true for OCM discovered application, false for OCM managed applications.
	IsDiscovered() bool
}

type Workload interface {
	// Can differ based on the workload, hence part of the Workload interface
	Kustomize() string

	GetName() string
	GetAppName() string
	GetPath() string
	GetRevision() string

	// SupportsDeployer returns tue if this workload is compatible with deployer.
	SupportsDeployer(Deployer) bool

	// TODO: replace client with cluster.
	Health(ctx Context, client client.Client, namespace string) error
}

// Context combines workload, deployer and logger used in the content of one test.
// The context name is used for logging and resource names.
type Context interface {
	Deployer() Deployer
	Workload() Workload
	Name() string

	// Namespace for OCM and Ramen resources (Subscription, ApplicationSet, DRPC, VRG) on the hub and managed clusters.
	// Depending on the deployer, it may be the same as AppNamespace().
	ManagementNamespace() string

	// Namespace for application resources on the managed clusters.
	AppNamespace() string

	Logger() *zap.SugaredLogger
}
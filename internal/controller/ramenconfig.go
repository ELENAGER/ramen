// SPDX-FileCopyrightText: The RamenDR authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/go-logr/logr"
	ramendrv1alpha1 "github.com/ramendr/ramen/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	hubName                                           = "hub"
	drClusterName                                     = "dr-cluster"
	operatorNamePrefix                                = "ramen-"
	operatorNameSuffix                                = "-operator"
	hubOperatorNameDefault                            = operatorNamePrefix + hubName + operatorNameSuffix
	drClusterOperatorNameDefault                      = operatorNamePrefix + drClusterName + operatorNameSuffix
	configMapNameSuffix                               = "-config"
	HubOperatorConfigMapName                          = hubOperatorNameDefault + configMapNameSuffix
	DrClusterOperatorConfigMapName                    = drClusterOperatorNameDefault + configMapNameSuffix
	leaderElectionResourceNameSuffix                  = ".ramendr.openshift.io"
	HubLeaderElectionResourceName                     = hubName + leaderElectionResourceNameSuffix
	drClusterLeaderElectionResourceName               = drClusterName + leaderElectionResourceNameSuffix
	ConfigMapRamenConfigKeyName                       = "ramen_manager_config.yaml"
	drClusterOperatorPackageNameDefault               = drClusterOperatorNameDefault
	drClusterOperatorChannelNameDefault               = "alpha"
	drClusterOperatorCatalogSourceNameDefault         = "ramen-catalog"
	drClusterOperatorClusterServiceVersionNameDefault = drClusterOperatorPackageNameDefault + ".v0.0.1"
	DefaultCephFSCSIDriverName                        = "openshift-storage.cephfs.csi.ceph.com"
	VeleroNamespaceNameDefault                        = "velero"
	DefaultVolSyncCopyMethod                          = "Snapshot"
)

var VolumeUnprotectionEnabledForAsyncVolSync = false

// FIXME
const NoS3StoreAvailable = "NoS3"

var ControllerType ramendrv1alpha1.ControllerType

var cachedRamenConfigFileName string

func LoadControllerConfig(configFile string,
	log logr.Logger,
) (ramenConfig *ramendrv1alpha1.RamenConfig) {
	if configFile == "" {
		log.Info("Ramen config file not specified")

		return
	}

	log.Info("loading Ramen configuration from ", "file", configFile)

	cachedRamenConfigFileName = configFile

	ramenConfig, err := ReadRamenConfigFile(log)
	if err != nil {
		panic(fmt.Sprintf("could not parse config file: %v", err))
	}

	for profileName, s3Profile := range ramenConfig.S3StoreProfiles {
		log.Info("s3 profile", "key", profileName, "value", s3Profile)
	}

	return
}

func LoadControllerOptions(options *ctrl.Options, ramenConfig *ramendrv1alpha1.RamenConfig) {
	if ramenConfig == nil {
		return
	}

	options.HealthProbeBindAddress = ramenConfig.Health.HealthProbeBindAddress
	options.Metrics.BindAddress = ramenConfig.Metrics.BindAddress

	if ramenConfig.LeaderElection != nil {
		if ramenConfig.LeaderElection.LeaderElect != nil {
			options.LeaderElection = *ramenConfig.LeaderElection.LeaderElect
		}

		if ramenConfig.LeaderElection.ResourceName != "" {
			options.LeaderElectionID = ramenConfig.LeaderElection.ResourceName
		}
	}
}

// Read the RamenConfig file mounted in the local file system.  This file is
// expected to be cached in the local file system.  If reading of the
// RamenConfig file for every S3 store profile access turns out to be more
// expensive, we may need to enhance this logic to load it only when
// RamenConfig has changed.
func ReadRamenConfigFile(log logr.Logger) (ramenConfig *ramendrv1alpha1.RamenConfig, err error) {
	if cachedRamenConfigFileName == "" {
		err = fmt.Errorf("config file not specified")

		return
	}

	fileContents, err := os.ReadFile(cachedRamenConfigFileName)
	if err != nil {
		err = fmt.Errorf("unable to load the config file %s: %w",
			cachedRamenConfigFileName, err)

		return
	}

	err = yaml.Unmarshal(fileContents, &ramenConfig)
	if err != nil {
		err = fmt.Errorf("unable to marshal the config file %s: %w",
			cachedRamenConfigFileName, err)

		return
	}

	return
}

func GetRamenConfigS3StoreProfile(ctx context.Context, apiReader client.Reader, profileName string) (
	s3StoreProfile ramendrv1alpha1.S3StoreProfile, err error,
) {
	_, ramenConfig, err := ConfigMapGet(ctx, apiReader)
	if err != nil {
		return s3StoreProfile, err
	}

	s3StoreProfilePointer := RamenConfigS3StoreProfilePointerGet(ramenConfig, profileName)

	if s3StoreProfilePointer == nil {
		err = fmt.Errorf("s3 profile %s not found in RamenConfig", profileName)

		return s3StoreProfile, err
	}

	s3StoreProfile = *s3StoreProfilePointer

	err = s3StoreProfileFormatCheck(&s3StoreProfile)

	return
}

func RamenConfigS3StoreProfilePointerGet(ramenConfig *ramendrv1alpha1.RamenConfig, profileName string,
) *ramendrv1alpha1.S3StoreProfile {
	for i := range ramenConfig.S3StoreProfiles {
		s3Profile := &ramenConfig.S3StoreProfiles[i]
		if s3Profile.S3ProfileName == profileName {
			return s3Profile
		}
	}

	return nil
}

func s3StoreProfileFormatCheck(s3StoreProfile *ramendrv1alpha1.S3StoreProfile) (err error) {
	s3Endpoint := s3StoreProfile.S3CompatibleEndpoint
	if s3Endpoint == "" {
		err = fmt.Errorf("s3 endpoint has not been configured in s3 profile %s",
			s3StoreProfile.S3ProfileName)

		return err
	}

	_, err = url.ParseRequestURI(s3Endpoint)
	if err != nil {
		err = fmt.Errorf("invalid s3 endpoint <%s> in "+
			"profile %s, reason: %w", s3Endpoint, s3StoreProfile.S3ProfileName, err)

		return err
	}

	s3Bucket := s3StoreProfile.S3Bucket
	if s3Bucket == "" {
		err = fmt.Errorf("s3 bucket has not been configured in s3 profile %s",
			s3StoreProfile.S3ProfileName)

		return err
	}

	return nil
}

func getMaxConcurrentReconciles(log logr.Logger) int {
	const defaultMaxConcurrentReconciles = 1

	ramenConfig, err := ReadRamenConfigFile(log)
	if err != nil {
		return defaultMaxConcurrentReconciles
	}

	if ramenConfig.MaxConcurrentReconciles == 0 {
		return defaultMaxConcurrentReconciles
	}

	return ramenConfig.MaxConcurrentReconciles
}

func ConfigMapNew(
	namespaceName string,
	name string,
	ramenConfig *ramendrv1alpha1.RamenConfig,
) (*corev1.ConfigMap, error) {
	ramenConfigYaml, err := yaml.Marshal(ramenConfig)
	if err != nil {
		return nil, fmt.Errorf("config map yaml marshal %w", err)
	}

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespaceName,
		},
		Data: map[string]string{
			ConfigMapRamenConfigKeyName: string(ramenConfigYaml),
		},
	}, nil
}

func ConfigMapGet(
	ctx context.Context,
	apiReader client.Reader,
) (configMap *corev1.ConfigMap, ramenConfig *ramendrv1alpha1.RamenConfig, err error) {
	configMapName := HubOperatorConfigMapName
	if ControllerType != ramendrv1alpha1.DRHubType {
		configMapName = DrClusterOperatorConfigMapName
	}

	configMap = &corev1.ConfigMap{}
	if err = apiReader.Get(
		ctx,
		types.NamespacedName{
			Namespace: RamenOperatorNamespace(),
			Name:      configMapName,
		},
		configMap,
	); err != nil {
		return
	}

	ramenConfig = &ramendrv1alpha1.RamenConfig{}
	err = yaml.Unmarshal([]byte(configMap.Data[ConfigMapRamenConfigKeyName]), ramenConfig)

	return
}

func RamenOperatorNamespace() string {
	return os.Getenv("POD_NAMESPACE")
}

func RamenOperandsNamespace(config ramendrv1alpha1.RamenConfig) string {
	return config.RamenOpsNamespace
}

// vrgAdminNamespaceNames returns the namespace names where the vrg objects can
// be created for multi namespace protection.  The list includes the namespace
// where the ramen operator pod is running.  This is to keep backward
// compatibility with existing multi namespace protection.
func vrgAdminNamespaceNames(config ramendrv1alpha1.RamenConfig) []string {
	return []string{RamenOperandsNamespace(config), RamenOperatorNamespace()}
}

// drpcAdminNamespaceName returns the namespace name where the drpc objects can
// be created for multi namespace protection. The DRPC must be created only in
// RamenOperandsNamespace for multi namespace protection.
func drpcAdminNamespaceName(config ramendrv1alpha1.RamenConfig) string {
	return RamenOperandsNamespace(config)
}

func drClusterOperatorChannelNameOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.DrClusterOperator.ChannelName == "" {
		return drClusterOperatorChannelNameDefault
	}

	return ramenConfig.DrClusterOperator.ChannelName
}

func drClusterOperatorPackageNameOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.DrClusterOperator.PackageName == "" {
		return drClusterOperatorPackageNameDefault
	}

	return ramenConfig.DrClusterOperator.PackageName
}

func drClusterOperatorNamespaceNameOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.DrClusterOperator.NamespaceName == "" {
		return RamenOperatorNamespace()
	}

	return ramenConfig.DrClusterOperator.NamespaceName
}

func drClusterOperatorCatalogSourceNameOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.DrClusterOperator.CatalogSourceName == "" {
		return drClusterOperatorCatalogSourceNameDefault
	}

	return ramenConfig.DrClusterOperator.CatalogSourceName
}

func drClusterOperatorCatalogSourceNamespaceNameOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.DrClusterOperator.CatalogSourceNamespaceName == "" {
		return RamenOperatorNamespace()
	}

	return ramenConfig.DrClusterOperator.CatalogSourceNamespaceName
}

func drClusterOperatorClusterServiceVersionNameOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.DrClusterOperator.ClusterServiceVersionName == "" {
		return drClusterOperatorClusterServiceVersionNameDefault
	}

	return ramenConfig.DrClusterOperator.ClusterServiceVersionName
}

func cephFSCSIDriverNameOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.VolSync.CephFSCSIDriverName == "" {
		return DefaultCephFSCSIDriverName
	}

	return ramenConfig.VolSync.CephFSCSIDriverName
}

func volSyncDestinationCopyMethodOrDefault(ramenConfig *ramendrv1alpha1.RamenConfig) string {
	if ramenConfig.VolSync.DestinationCopyMethod == "" {
		return DefaultVolSyncCopyMethod
	}

	return ramenConfig.VolSync.DestinationCopyMethod
}

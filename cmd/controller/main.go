// karpenter-provider-scaleway is the unified Karpenter cloudprovider for
// Scaleway-managed Kubernetes (Kapsule or Kosmos). The binary auto-detects
// the cluster type at boot via k8s.GetCluster, then registers the matching
// backend implementation along with the ScalewayNodeClass reconciler.
//
// Required env (Secret) :
//
//	SCW_ACCESS_KEY, SCW_SECRET_KEY      Scaleway IAM credentials.
//
// Required env (ConfigMap) :
//
//	SCW_DEFAULT_PROJECT_ID              Project ID (also used as organization).
//	SCW_CLUSTER_ID                      Scaleway-managed cluster UUID.
//	SCW_DEFAULT_REGION                  e.g. fr-par.
//
// Optional env :
//
//	SCW_FORCE_BACKEND                   "kapsule" | "kosmos" — overrides auto.
//	SCW_KOSMOS_POOL_ID                  Required when the resolved backend
//	                                    is Kosmos: ID of the seed pool.
//	SCW_KOSMOS_API_KEY                  Required for Kosmos: IAM key with
//	                                    KubernetesExternalNodeRegister.
//	K8S_AGENT_URL                       Override the agent binary URL.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	// Side-effect import: registers ScalewayNodeClass with the global scheme.
	_ "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend"
	"github.com/no-inspi/karpenter-scaleway/pkg/cloudprovider"
	nodeclasscontroller "github.com/no-inspi/karpenter-scaleway/pkg/controllers/nodeclass"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
	"github.com/no-inspi/karpenter-scaleway/pkg/version"

	opstatus "github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator"
)

const defaultK8sAgentURL = "https://github.com/scaleway/k8s-agent/releases/download/v0.1.5/k8s-agent_linux_amd64"

// config bundles every value the binary reads from the environment so the
// rest of the wiring stays argument-passing rather than env-grabbing.
type config struct {
	accessKey    string
	secretKey    string
	projectID    string
	region       scw.Region
	clusterID    string
	forceBackend string
	kosmosPoolID string
	kosmosAPIKey string
	k8sAgentURL  string
}

func main() {
	ctx, op := operator.NewOperator()
	logger := log.FromContext(ctx).WithName("scaleway")
	logger.Info("starting karpenter-provider-scaleway", "version", version.Version)

	cfg := mustLoadConfig(logger)

	scwClient, err := scw.NewClient(
		scw.WithDefaultProjectID(cfg.projectID),
		scw.WithAuth(cfg.accessKey, cfg.secretKey),
	)
	if err != nil {
		logger.Error(err, "creating scaleway client")
		os.Exit(1)
	}

	k8sAPI := scaleway.NewK8sClient(scwClient, cfg.region)

	kind, source, err := resolveBackend(ctx, cfg.forceBackend, k8sAPI, cfg.clusterID)
	if err != nil {
		logger.Error(err, "resolving backend")
		os.Exit(1)
	}
	logger.Info("backend resolved",
		"backend", string(kind),
		"source", string(source),
		"clusterID", cfg.clusterID,
	)

	priceProvider := pricing.NewProvider(pricing.NewScalewayAPI(scwClient), &logger)

	bk, err := buildBackend(kind, cfg, scwClient, k8sAPI, op.GetClient(), priceProvider, logger)
	if err != nil {
		logger.Error(err, "building backend")
		os.Exit(1)
	}

	// Run the price-refresh goroutine via the manager so it starts only
	// after leader election: a single replica polls Scaleway every 12h.
	if err := op.Manager.Add(manager.RunnableFunc(func(ctx context.Context) error {
		priceProvider.Run(ctx)
		return nil
	})); err != nil {
		logger.Error(err, "registering pricing refresher")
		os.Exit(1)
	}

	cp := cloudprovider.New(bk, logger.WithName("cloudprovider"))
	clusterState := state.NewCluster(op.Clock, op.GetClient(), cp)

	mkt := scaleway.NewMarketplaceClient(scwClient)
	nodeClassRec := nodeclasscontroller.New(op.GetClient(), mkt, kind, logger.WithName("nodeclass"))
	if err := nodeClassRec.SetupWithManager(op.Manager); err != nil {
		logger.Error(err, "registering nodeclass reconciler")
		os.Exit(1)
	}

	// Operatorpkg status controller exposes the conditions we author as
	// Prometheus metrics — registers a watch but doesn't write conditions.
	nodeClassMetrics := opstatus.NewController[*v1alpha1.ScalewayNodeClass](
		op.GetClient(),
		op.Manager.GetEventRecorderFor("scalewaynodeclass.status"),
	)

	if err := op.Manager.AddHealthzCheck("ping", healthz.Ping); err != nil {
		logger.Error(err, "adding healthz")
		os.Exit(1)
	}
	if err := op.Manager.AddReadyzCheck("backend", func(_ *http.Request) error {
		return bk.Healthcheck(context.Background())
	}); err != nil {
		logger.Error(err, "adding readyz")
		os.Exit(1)
	}

	op.WithControllers(ctx, controllers.NewControllers(
		ctx,
		op.Manager,
		op.Clock,
		op.GetClient(),
		op.EventRecorder,
		// The same cloudprovider is passed as both decorated and undecorated
		// arguments (no overlay decoration).
		cp,
		cp,
		clusterState,
		op.InstanceTypeStore,
	)...).WithControllers(ctx, nodeClassMetrics).Start(ctx)
}

// resolveBackend dispatches to forced or auto resolution. Auto-detection
// happens once at boot — the result is then immutable for the lifetime of
// the process.
func resolveBackend(ctx context.Context, force string, k8sAPI scaleway.K8sAPI, clusterID string) (backend.Kind, backend.Source, error) {
	if force != "" {
		kind, err := backend.ResolveForced(force)
		return kind, backend.SourceForced, err
	}
	kind, err := backend.ResolveAuto(ctx, k8sAPI, clusterID)
	return kind, backend.SourceAuto, err
}

// buildBackend constructs the right Scaleway client and Backend for kind.
func buildBackend(kind backend.Kind, cfg config, scwClient *scw.Client, k8sAPI scaleway.K8sAPI, kc client.Client, prices *pricing.Provider, logger logr.Logger) (backend.Backend, error) {
	switch kind {
	case backend.KindKapsule:
		kapsuleClient := scaleway.NewKapsuleClient(scwClient, cfg.region, cfg.clusterID)
		return backend.New(kind, backend.SharedOptions{
			Kapsule:    kapsuleClient,
			KubeClient: kc,
			ClusterID:  cfg.clusterID,
			Pricing:    prices,
			Log:        logger.WithName("kapsule"),
		})
	case backend.KindKosmos:
		if cfg.kosmosPoolID == "" || cfg.kosmosAPIKey == "" {
			return nil, fmt.Errorf("kosmos backend requires SCW_KOSMOS_POOL_ID and SCW_KOSMOS_API_KEY")
		}
		instClient := scaleway.NewClient(scwClient, cfg.kosmosPoolID, cfg.kosmosAPIKey, cfg.k8sAgentURL)
		return backend.New(kind, backend.SharedOptions{
			Instance:   instClient,
			K8s:        k8sAPI,
			KubeClient: kc,
			ClusterID:  cfg.clusterID,
			Pricing:    prices,
			Log:        logger.WithName("kosmos"),
		})
	default:
		return nil, fmt.Errorf("unknown backend kind %q", kind)
	}
}

// mustLoadConfig reads the SCW_* env vars and aborts on missing required
// values. Surfacing missing config as an error at boot rather than at first
// API call makes Helm-install failures self-explanatory.
func mustLoadConfig(logger logr.Logger) config {
	cfg := config{
		accessKey:    mustEnv(logger, "SCW_ACCESS_KEY"),
		secretKey:    mustEnv(logger, "SCW_SECRET_KEY"),
		projectID:    mustEnv(logger, "SCW_DEFAULT_PROJECT_ID"),
		clusterID:    mustEnv(logger, "SCW_CLUSTER_ID"),
		region:       scw.Region(mustEnv(logger, "SCW_DEFAULT_REGION")),
		forceBackend: os.Getenv("SCW_FORCE_BACKEND"),
		kosmosPoolID: os.Getenv("SCW_KOSMOS_POOL_ID"),
		kosmosAPIKey: os.Getenv("SCW_KOSMOS_API_KEY"),
		k8sAgentURL:  envOr("K8S_AGENT_URL", defaultK8sAgentURL),
	}
	return cfg
}

func mustEnv(logger logr.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		logger.Error(nil, "missing required env var", "name", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

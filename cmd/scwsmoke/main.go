// scwsmoke is a one-shot E2E sanity check for the Scaleway Instance wrapper
// and the k8s-agent cloud-init bootstrap. It creates a single Instance,
// renders the kosmos cloud-init, waits for the node to appear in `kubectl get
// nodes`, then terminates the Instance.
//
// Required env :
//
//	SCW_ACCESS_KEY           admin key used to create/terminate the Instance
//	SCW_SECRET_KEY           admin secret matching SCW_ACCESS_KEY
//	SCW_DEFAULT_PROJECT_ID   project where the Instance is created
//	SCW_KOSMOS_POOL_ID       multicloud pool id (terraform output pool_id)
//	SCW_KOSMOS_API_KEY       IAM key with KubernetesExternalNodeRegister
//	                         (terraform output scw_secret_key)
//
// Optional :
//
//	K8S_AGENT_URL            override the agent binary URL
//	                         (default: v0.1.5 amd64 from VERSIONS.md)
//	SCW_INSTANCE_TYPE        override the commercial type (default: GP1-XS)
//
// Flags :
//
//	-keep-alive              skip the auto-terminate at end (default false).
//	                         Use to inspect the Instance via Scaleway serial
//	                         console when the node fails to join.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/scaleway/scaleway-sdk-go/scw"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
)

const defaultK8sAgentURL = "https://github.com/scaleway/k8s-agent/releases/download/v0.1.5/k8s-agent_linux_amd64"

func main() {
	keepAlive := flag.Bool("keep-alive", false, "do not terminate the Instance at end (debug)")
	wait := flag.Duration("wait", 4*time.Minute, "how long to wait for k8s-agent registration before terminating")
	flag.Parse()

	scwClient, err := scw.NewClient(
		scw.WithDefaultProjectID(mustEnv("SCW_DEFAULT_PROJECT_ID")),
		scw.WithAuth(mustEnv("SCW_ACCESS_KEY"), mustEnv("SCW_SECRET_KEY")),
	)
	must(err)

	agentURL := os.Getenv("K8S_AGENT_URL")
	if agentURL == "" {
		agentURL = defaultK8sAgentURL
	}
	instanceType := os.Getenv("SCW_INSTANCE_TYPE")
	if instanceType == "" {
		instanceType = "GP1-XS"
	}

	c := scaleway.NewClient(
		scwClient,
		mustEnv("SCW_KOSMOS_POOL_ID"),
		mustEnv("SCW_KOSMOS_API_KEY"),
		agentURL,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	name := fmt.Sprintf("smoke-%d", time.Now().Unix())
	logf("creating Instance %s (%s)", name, instanceType)
	inst, err := c.Create(ctx, scaleway.CreateInstanceParams{
		Zone:         scw.ZoneFrPar1,
		InstanceType: instanceType,
		Name:         name,
	})
	must(err)
	logf("created id=%s privateIP=%s", inst.ID, inst.PrivateIP)
	logf("Scaleway console URL : https://console.scaleway.com/instance/servers/%s/%s/overview", inst.Zone, inst.ID)
	logf("→ open Serial Console in UI to watch /var/log/cloud-init-output.log live")

	logf("waiting %s for k8s-agent to register the node — run `kubectl get nodes -w` in another shell", *wait)
	time.Sleep(*wait)

	if *keepAlive {
		logf("keep-alive set — Instance %s left running; terminate manually via console or scw CLI", inst.ID)
		return
	}

	logf("terminating %s", inst.ID)
	must(c.Delete(ctx, inst.Zone, inst.ID))
	logf("done")
}

func logf(format string, args ...any) {
	fmt.Printf("[%s] %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		fmt.Fprintf(os.Stderr, "missing env var %s\n", k)
		os.Exit(1)
	}
	return v
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

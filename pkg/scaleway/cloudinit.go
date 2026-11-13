package scaleway

import (
	"bytes"
	"text/template"
)

type CloudInitParams struct {
	PoolID      string
	PoolRegion  string
	APIKey      string
	K8sAgentURL string
}

const cloudInitTmpl = `#!/bin/bash
set -euo pipefail

# Kosmos node bootstrap via k8s-agent
# Pool: {{ .PoolID }} (region {{ .PoolRegion }})
#
# k8s-agent --kosmos is a one-shot installer: it registers the node via the
# Scaleway API (POST /k8s/v1/regions/<region>/pools/<id>/external-nodes/auth),
# installs kubelet + containerd + CNI, then exits. kubelet keeps running.
#
# TODO(prod): SCW_SECRET_KEY is rendered into cloud-init in cleartext and ends
# up in /var/log/cloud-init*. For prod, fetch it from instance metadata at
# runtime or use Scaleway Secret Manager.

# Karpenter v1.10 expects every managed node to register with the
# karpenter.sh/unregistered:NoExecute taint so the controller can drive the
# registration handshake without scheduling racing with it. Karpenter matches
# key and effect, so NoSchedule is not enough. On Kapsule it is a pool startup
# taint; on Kosmos we inject it ourselves via a kubelet systemd drop-in
# written before k8s-agent installs and starts kubelet.
mkdir -p /etc/systemd/system/kubelet.service.d
cat > /etc/systemd/system/kubelet.service.d/20-karpenter-taints.conf <<'EOF'
[Service]
Environment="KUBELET_EXTRA_ARGS=--register-with-taints=karpenter.sh/unregistered:NoExecute"
EOF

curl --retry 5 --retry-delay 2 --retry-all-errors -fsSLo /usr/local/bin/k8s-agent "{{ .K8sAgentURL }}"
chmod +x /usr/local/bin/k8s-agent

POOL_ID={{ .PoolID }} \
POOL_REGION={{ .PoolRegion }} \
SCW_SECRET_KEY={{ .APIKey }} \
/usr/local/bin/k8s-agent --kosmos
`

func RenderCloudInit(p CloudInitParams) (string, error) {
	t, err := template.New("cloud-init").Parse(cloudInitTmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return "", err
	}
	return buf.String(), nil
}

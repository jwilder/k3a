go build -o k3a ./cmd/k3a && echo "Build successful"
# Create cluster infrastructure with integrated PostgreSQL Flexible Server
./k3a cluster create --subscription 110efc33-11a4-46b9-9986-60716283fbe7 --region canadacentral --cluster k3s-canadacentral-vapa17

# Create control plane using PostgreSQL as datastore (auto-detects PostgreSQL server name)
./k3a pool create --cluster k3s-canadacentral-vapa17 --name k3s-master --instance-count 1 --subscription 110efc33-11a4-46b9-9986-60716283fbe7 --role control-plane
./k3a nsg rule create --cluster  k3s-canadacentral-vapa17 --source CorpNetPublic --name AllowCorpNetPublic --priority 150  --subscription 110efc33-11a4-46b9-9986-60716283fbe7
./k3a kubeconfig --cluster  k3s-canadacentral-vapa17
kubectl get nodes -o name | grep "k3s-agent-" | xargs -I {} kubectl label {} node-role.kubernetes.io/worker=worker --overwrite
# Create worker nodes with Premium SSD 1TB (P30 tier = 5,000 IOPS)
./k3a pool create --cluster k3s-canadacentral-vapa17 --name k3s-agent --instance-count 3 --sku Standard_D16s_v3 --subscription 110efc33-11a4-46b9-9986-60716283fbe7 --role worker

# Wait for worker nodes to be ready and label them
echo "Waiting for worker nodes to be ready..."
sleep 30
kubectl get nodes --no-headers | awk '{print $1}' | grep 'agent' | xargs -r -I {} kubectl label node {} node-role.kubernetes.io/worker=worker --overwrite
echo "All worker nodes labeled successfully"

# Apply CoreDNS patch to ensure it only runs on agent/worker nodes
echo "Applying CoreDNS patch to restrict to worker nodes..."
kubectl patch deployment coredns -n kube-system --patch-file scripts/coredns-patch.json
kubectl rollout restart deployment/coredns -n kube-system
kubectl rollout status deployment/coredns -n kube-system
echo "CoreDNS configured to run only on worker nodes"

# Apply metrics-server patch to ensure it only runs on agent/worker nodesswwa f  6b46b 6
echo "Applying metrics-server patch to restrict to worker nodes..."
kubectl patch deployment metrics-server -n kube-system --patch-file scripts/metrics-server-patch.json
kubectl rollout restart deployment/metrics-server -n kube-system
kubectl rollout status deployment/metrics-server -n kube-system
echo "Metrics-server configured to run only on worker nodes"

# Apply svclb-traefik patch to ensure it doesn't run on hollow nodes (if it exists)
echo "Checking for svclb-traefik daemonset..."
SVCLB_TRAEFIK_DS=$(kubectl get daemonsets -n kube-system -o name | grep svclb-traefik | head -n 1)
if [ -n "$SVCLB_TRAEFIK_DS" ]; then
    echo "Applying svclb-traefik patch to avoid hollow nodes..."
    kubectl patch $SVCLB_TRAEFIK_DS -n kube-system --patch-file scripts/svclb-traefik-patch.json
    echo "Traefik load balancer configured to avoid hollow nodes"
else
    echo "svclb-traefik daemonset not found (will be created when Traefik service needs load balancing)"
    echo "Note: Apply the patch manually later if needed:"
    echo "kubectl patch daemonset svclb-traefik -n kube-system --patch-file scripts/svclb-traefik-patch.json"
fi

# Verify all system pods are properly placed
echo "Verifying system pod placement:"
kubectl get pods -n kube-system -o wide | grep -E "(coredns|metrics-server|svclb-traefik)"
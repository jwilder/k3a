#!/bin/bash

# Script to apply Traefik load balancer patch to avoid hollow nodes
# Run this after creating Traefik services that trigger the svclb-traefik daemonset creation

echo "🔍 Checking for Traefik load balancer daemonsets..."

# Check for svclb-traefik (default Traefik service)
if kubectl get daemonset svclb-traefik -n kube-system >/dev/null 2>&1; then
    echo "✅ Found svclb-traefik daemonset, applying hollow node avoidance patch..."
    kubectl patch daemonset svclb-traefik -n kube-system --patch-file scripts/svclb-traefik-patch.json
    echo "✅ svclb-traefik configured to avoid hollow nodes"
else
    echo "❌ svclb-traefik daemonset not found"
fi

# Check for any other svclb-* daemonsets (custom Traefik services)
echo ""
echo "🔍 Checking for other load balancer daemonsets..."
kubectl get daemonsets -n kube-system | grep "svclb-" | while read line; do
    daemonset_name=$(echo "$line" | awk '{print $1}')
    echo "📋 Found: $daemonset_name"
    echo "   Applying hollow node avoidance patch..."
    kubectl patch daemonset "$daemonset_name" -n kube-system --patch-file scripts/svclb-traefik-patch.json
    echo "   ✅ $daemonset_name configured to avoid hollow nodes"
done

echo ""
echo "🎯 Summary: All Traefik load balancer pods will avoid hollow nodes with kubemark=true label"
echo "📝 Note: This patch only needs to be applied once per daemonset"

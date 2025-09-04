#!/bin/bash
#
# K3s CPU Affinity Update Script - 56 Core Configuration
# Updates existing optimized servers from 48 to 56 cores
#
# Usage: sudo ./update-to-56cores.sh
#

set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

log() {
    echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')] $1${NC}"
}

warn() {
    echo -e "${YELLOW}[$(date +'%Y-%m-%d %H:%M:%S')] WARNING: $1${NC}"
}

error() {
    echo -e "${RED}[$(date +'%Y-%m-%d %H:%M:%S')] ERROR: $1${NC}"
    exit 1
}

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   error "This script must be run as root (use sudo)"
fi

log "Updating K3s CPU affinity from 48 to 56 cores"

# Get hostname for logging
HOSTNAME=$(hostname)
log "Updating server: $HOSTNAME"

# 1. Backup current service file
if [[ -f /etc/systemd/system/k3s.service ]]; then
    cp /etc/systemd/system/k3s.service /etc/systemd/system/k3s.service.56core.backup.$(date +%Y%m%d_%H%M%S)
    log "Service file backed up"
else
    error "K3s service file not found"
fi

# 2. Update CPU affinity in service file
log "Updating CPU affinity to cores 0-55"
sed -i 's/CPUAffinity=0-47/CPUAffinity=0-55/' /etc/systemd/system/k3s.service

# 3. Update IRQ balancing configuration
log "Updating IRQ balancing for 56-core configuration"
cat > /etc/sysconfig/irqbalance << 'EOF'
# IRQ balancing configuration for k3s optimization
# Reserve cores 0-55 for k3s, use cores 56-63 for IRQs and system processes
IRQBALANCE_BANNED_CPUS=00000000000000ffffffffffffffffff
# Use power management
IRQBALANCE_ARGS="--powerthresh=20"
EOF

# 4. Apply changes
log "Reloading systemd configuration"
systemctl daemon-reload

log "Restarting IRQ balance"
systemctl restart irqbalance

log "Restarting K3s service"
systemctl restart k3s

# Wait for service to start
log "Waiting for K3s to start..."
sleep 15

# 5. Verify changes
K3S_PID=$(pgrep -f "/usr/local/bin/k3s server" | head -1 || echo "")
if [[ -n "$K3S_PID" ]]; then
    log "K3s server PID: $K3S_PID"
    
    CPU_AFFINITY=$(taskset -cp $K3S_PID 2>/dev/null | cut -d: -f2 | tr -d ' ' || echo "unknown")
    log "K3s CPU affinity: $CPU_AFFINITY"
    
    if [[ "$CPU_AFFINITY" == "0-55" ]]; then
        log "✅ CPU affinity successfully updated to 56 cores (0-55)"
    else
        warn "CPU affinity is: $CPU_AFFINITY (may need manual verification)"
    fi
    
    # Show process details
    ps -p $K3S_PID -o pid,ppid,psr,pcpu,pmem,nlwp,nice,time,cmd --no-headers 2>/dev/null || warn "Could not get process details"
else
    error "K3s server process not found after restart"
fi

log "56-core update completed successfully!"
log "Configuration:"
echo "  ✅ K3s Cores: 0-55 (56 cores = 87.5%)"
echo "  ✅ System Cores: 56-63 (8 cores = 12.5%)"
echo "  ✅ IRQ Balancing: Updated for new configuration"

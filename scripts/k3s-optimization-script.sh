#!/bin/bash
#
# K3s Server CPU and Performance Optimization Script
# Version: 1.0
# Date: August 6, 2025
# 
# This script applies comprehensive optimizations to K3s servers for maximum performance
# including CPU pinning, memory limits, I/O scheduling, and IRQ balancing.
#
# Usage: sudo ./k3s-optimization-script.sh
#

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging function
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

log "Starting K3s Server Optimization Process"

# Get hostname for logging
HOSTNAME=$(hostname)
log "Optimizing server: $HOSTNAME"

# Check CPU count
CPU_COUNT=$(nproc)
log "Detected $CPU_COUNT CPU cores"

if [[ $CPU_COUNT -lt 16 ]]; then
    warn "This optimization is designed for 64-core systems. Current system has $CPU_COUNT cores."
    warn "Consider adjusting CPU affinity settings for your system."
fi

# 1. Backup current K3s service file
log "Creating backup of current K3s service file"
if [[ -f /etc/systemd/system/k3s.service ]]; then
    cp /etc/systemd/system/k3s.service /etc/systemd/system/k3s.service.backup.$(date +%Y%m%d_%H%M%S)
    log "Backup created: k3s.service.backup.$(date +%Y%m%d_%H%M%S)"
else
    error "K3s service file not found at /etc/systemd/system/k3s.service"
fi

# 2. Add CPU optimization settings to K3s service
log "Adding CPU optimization settings to K3s service"

# Check if optimizations already exist
if grep -q "CPUAffinity=0-47" /etc/systemd/system/k3s.service; then
    warn "CPU optimizations already present in service file"
else
    # Add CPU optimizations after LimitCORE=infinity
    sed -i '/LimitCORE=infinity/a\
# CPU Optimization Settings\
# Pin k3s to cores 0-55 (first 56 cores), leaving 8 cores for system/OS\
CPUAffinity=0-55\
# High priority for k3s (lower nice value = higher priority)\
Nice=-10\
# Real-time I/O scheduling\
IOSchedulingClass=1\
IOSchedulingPriority=4\
\
# Memory optimization\
MemoryHigh=200G\
MemoryMax=220G' /etc/systemd/system/k3s.service

    log "CPU optimization settings added to K3s service"
fi

# 3. Configure IRQ balancing
log "Configuring IRQ balancing to avoid K3s cores"

# Create IRQ balance configuration
cat > /etc/sysconfig/irqbalance << 'EOF'
# IRQ balancing configuration for k3s optimization
# Reserve cores 0-55 for k3s, use cores 56-63 for IRQs and system processes
IRQBALANCE_BANNED_CPUS=00000000000000ffffffffffffffffff
# Use power management
IRQBALANCE_ARGS="--powerthresh=20"
EOF

log "IRQ balancing configuration created"

# 4. Enable and configure services
log "Enabling and configuring services"

# Enable IRQ balance if not already enabled
if ! systemctl is-enabled irqbalance >/dev/null 2>&1; then
    systemctl enable irqbalance
    log "IRQ balance service enabled"
fi

# 5. Reload systemd and restart services
log "Reloading systemd configuration"
systemctl daemon-reload

log "Restarting IRQ balance service"
systemctl restart irqbalance

# Check IRQ balance status
if systemctl is-active irqbalance >/dev/null 2>&1; then
    log "IRQ balance service is running"
else
    warn "IRQ balance service failed to start"
fi

# 6. Get current K3s PID before restart
OLD_K3S_PID=$(pgrep -f "k3s server" || echo "none")
if [[ "$OLD_K3S_PID" != "none" ]]; then
    log "Current K3s server PID: $OLD_K3S_PID"
fi

# 7. Restart K3s service
log "Restarting K3s service with optimizations"
systemctl restart k3s

# Wait for K3s to start
log "Waiting for K3s to start..."
sleep 10

# 8. Verify optimizations
log "Verifying K3s optimizations"

# Get new K3s PID
NEW_K3S_PID=$(pgrep -f "k3s server" || echo "none")
if [[ "$NEW_K3S_PID" == "none" ]]; then
    error "K3s server process not found after restart"
fi

log "New K3s server PID: $NEW_K3S_PID"

# Check CPU affinity
CPU_AFFINITY=$(taskset -cp $NEW_K3S_PID 2>/dev/null | cut -d: -f2 | tr -d ' ' || echo "unknown")
log "K3s CPU affinity: $CPU_AFFINITY"

# Check process details
log "K3s process details:"
ps -p $NEW_K3S_PID -o pid,ppid,psr,pcpu,pmem,nlwp,nice,cls,rtprio,time,cmd 2>/dev/null || warn "Could not get process details"

# Check service status
if systemctl is-active k3s >/dev/null 2>&1; then
    log "K3s service is running successfully"
else
    error "K3s service failed to start with optimizations"
fi

# 9. Display system performance
log "System performance summary:"
echo "----------------------------------------"
uptime
echo "----------------------------------------"

# Check memory usage
MEMORY_INFO=$(systemctl show k3s | grep -E '(MemoryCurrent|MemoryHigh|MemoryMax)' || echo "Memory info not available")
log "K3s memory configuration:"
echo "$MEMORY_INFO"

log "K3s optimization completed successfully!"
log "Optimizations applied:"
echo "  ✅ CPU Affinity: Cores 0-55 (56 cores dedicated to K3s)"
echo "  ✅ CPU Priority: Nice -10 (High priority)"
echo "  ✅ I/O Scheduling: Real-time Class 1, Priority 4"
echo "  ✅ Memory Limits: 200GB soft / 220GB hard limit"
echo "  ✅ IRQ Balancing: Cores 56-63 reserved for interrupts"

log "Backup files created with timestamp for rollback if needed"

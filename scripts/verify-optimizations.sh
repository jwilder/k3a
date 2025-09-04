#!/bin/bash
#
# K3s Optimization Verification Script
# This script verifies that all optimizations are properly applied
#
# Usage: ./verify-optimizations.sh
#

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Status symbols
CHECK="✅"
CROSS="❌"
WARN="⚠️"

# Logging functions
log() {
    echo -e "${GREEN}$1${NC}"
}

warn() {
    echo -e "${YELLOW}$WARN $1${NC}"
}

error() {
    echo -e "${RED}$CROSS $1${NC}"
}

success() {
    echo -e "${GREEN}$CHECK $1${NC}"
}

echo -e "${BLUE}===========================================${NC}"
echo -e "${BLUE}  K3s Optimization Verification Report${NC}"
echo -e "${BLUE}===========================================${NC}"
echo ""

# 1. Check if K3s is running
log "1. Checking K3s Service Status"
if systemctl is-active k3s >/dev/null 2>&1; then
    success "K3s service is running"
    
    # Try multiple methods to find K3s process
    K3S_PID=$(pgrep -f "/usr/local/bin/k3s server" 2>/dev/null || echo "")
    if [[ -z "$K3S_PID" ]]; then
        K3S_PID=$(pgrep -f "k3s server" 2>/dev/null || echo "")
    fi
    if [[ -z "$K3S_PID" ]]; then
        K3S_PID=$(ps aux | grep "/usr/local/bin/k3s" | grep -v grep | awk '{print $2}' | head -1 || echo "")
    fi
    if [[ -z "$K3S_PID" ]]; then
        # Try systemd MainPID as fallback
        K3S_PID=$(systemctl show k3s --property=MainPID --value 2>/dev/null || echo "")
        if [[ "$K3S_PID" == "0" ]]; then
            K3S_PID=""
        fi
    fi
    
    if [[ -n "$K3S_PID" ]]; then
        success "K3s server process found (PID: $K3S_PID)"
    else
        error "K3s server process not found"
        warn "Service is running but process not detected. Continuing with limited checks..."
        K3S_PID=""
    fi
else
    error "K3s service is not running"
    exit 1
fi

echo ""

# 2. Check CPU Affinity
log "2. Checking CPU Affinity Configuration"
if [[ -n "$K3S_PID" ]]; then
    CPU_AFFINITY=$(taskset -cp $K3S_PID 2>/dev/null | cut -d: -f2 | tr -d ' ' || echo "unknown")
    if [[ "$CPU_AFFINITY" == "0-55" ]]; then
        success "CPU affinity correctly set to cores 0-55 (56 cores)"
    elif [[ "$CPU_AFFINITY" == "0-47" ]]; then
        success "CPU affinity set to cores 0-47 (48 cores - previous configuration)"
    elif [[ "$CPU_AFFINITY" =~ ^0-[0-9]+$ ]]; then
        success "CPU affinity set to cores: $CPU_AFFINITY (custom configuration)"
    elif [[ "$CPU_AFFINITY" == "unknown" ]]; then
        warn "Could not determine CPU affinity"
    else
        warn "CPU affinity is set to: $CPU_AFFINITY (expected: 0-55 for 64-core systems)"
    fi
else
    warn "Cannot check CPU affinity - K3s PID not available"
fi

echo ""

# 3. Check Process Priority
log "3. Checking Process Priority and Scheduling"
if [[ -n "$K3S_PID" ]]; then
    PROCESS_INFO=$(sudo ps -p $K3S_PID -o pid,nice,cls,rtprio --no-headers 2>/dev/null || echo "")
    if [[ -n "$PROCESS_INFO" ]]; then
        NICE_VALUE=$(echo $PROCESS_INFO | awk '{print $2}')
        SCHED_CLASS=$(echo $PROCESS_INFO | awk '{print $3}')
        
        if [[ "$NICE_VALUE" == "-10" ]]; then
            success "Process priority correctly set (Nice: $NICE_VALUE)"
        else
            warn "Process priority is $NICE_VALUE (expected: -10)"
        fi
        
        if [[ "$SCHED_CLASS" == "TS" ]]; then
            success "Scheduling class is correct (TS)"
        else
            warn "Scheduling class is $SCHED_CLASS"
        fi
    else
        warn "Could not get process scheduling information for PID $K3S_PID"
    fi
else
    warn "Cannot check process priority - K3s PID not available"
fi

echo ""

# 4. Check Memory Limits
log "4. Checking Memory Configuration"
MEMORY_HIGH=$(systemctl show k3s --property=MemoryHigh --value 2>/dev/null || echo "")
MEMORY_MAX=$(systemctl show k3s --property=MemoryMax --value 2>/dev/null || echo "")
MEMORY_CURRENT=$(systemctl show k3s --property=MemoryCurrent --value 2>/dev/null || echo "")

if [[ "$MEMORY_HIGH" == "214748364800" ]]; then  # 200G in bytes
    success "Memory high limit correctly set (200G)"
elif [[ -n "$MEMORY_HIGH" && "$MEMORY_HIGH" != "infinity" ]]; then
    success "Memory high limit is set to $((MEMORY_HIGH / 1024 / 1024 / 1024))G"
else
    warn "Memory high limit not set or set to infinity"
fi

if [[ "$MEMORY_MAX" == "236223201280" ]]; then  # 220G in bytes
    success "Memory max limit correctly set (220G)"
elif [[ -n "$MEMORY_MAX" && "$MEMORY_MAX" != "infinity" ]]; then
    success "Memory max limit is set to $((MEMORY_MAX / 1024 / 1024 / 1024))G"
else
    warn "Memory max limit not set or set to infinity"
fi

if [[ -n "$MEMORY_CURRENT" ]]; then
    MEMORY_CURRENT_GB=$((MEMORY_CURRENT / 1024 / 1024 / 1024))
    success "Current memory usage: ${MEMORY_CURRENT_GB}G"
fi

echo ""

# 5. Check IRQ Balancing
log "5. Checking IRQ Balancing Configuration"
if systemctl is-active irqbalance >/dev/null 2>&1; then
    success "IRQ balance service is running"
    
    if [[ -f /etc/sysconfig/irqbalance ]]; then
        if grep -q "IRQBALANCE_BANNED_CPUS=00000000000000ffffffffffffffffff" /etc/sysconfig/irqbalance 2>/dev/null; then
            success "IRQ balancing correctly configured for 56-core K3s (cores 0-55)"
        elif grep -q "IRQBALANCE_BANNED_CPUS=000000000000ffffffffffff" /etc/sysconfig/irqbalance 2>/dev/null; then
            success "IRQ balancing configured for 48-core K3s (cores 0-47) - previous config"
        elif grep -q "IRQBALANCE_BANNED_CPUS" /etc/sysconfig/irqbalance 2>/dev/null; then
            BANNED_CPUS=$(grep "IRQBALANCE_BANNED_CPUS" /etc/sysconfig/irqbalance | cut -d= -f2)
            success "IRQ balancing configured with banned CPUs: $BANNED_CPUS"
        else
            warn "IRQ balancing configuration file exists but banned CPUs not configured"
        fi
    else
        warn "IRQ balancing configuration file not found"
    fi
else
    warn "IRQ balance service is not running"
fi

echo ""

# 6. System Performance Summary
log "6. System Performance Summary"
echo "Current system load:"
uptime

if [[ -n "$K3S_PID" ]]; then
    echo ""
    echo "K3s process details:"
    sudo ps -p $K3S_PID -o pid,ppid,psr,pcpu,pmem,nlwp,nice,time,cmd --no-headers 2>/dev/null || warn "Could not get process details for PID $K3S_PID"
else
    echo ""
    warn "K3s PID not available for detailed process information"
    echo "Showing all K3s processes:"
    ps aux | grep "/usr/local/bin/k3s" | grep -v grep | head -3 2>/dev/null || echo "No K3s processes found"
fi

echo ""

# 7. CPU Core Usage
log "7. CPU Core Utilization"
CPU_COUNT=$(nproc)
echo "Total CPU cores: $CPU_COUNT"

if [[ "$CPU_COUNT" -eq 64 ]]; then
    success "System has expected 64 CPU cores"
elif [[ "$CPU_COUNT" -ge 16 ]]; then
    warn "System has $CPU_COUNT cores (optimizations may need adjustment)"
else
    warn "System has only $CPU_COUNT cores (not recommended for these optimizations)"
fi

echo ""

# 8. Service File Verification
log "8. Service File Configuration Check"
if [[ -f /etc/systemd/system/k3s.service ]]; then
    if grep -q "CPUAffinity=0-55" /etc/systemd/system/k3s.service; then
        success "CPU affinity configured in service file (cores 0-55)"
    elif grep -q "CPUAffinity=0-47" /etc/systemd/system/k3s.service; then
        success "CPU affinity configured in service file (cores 0-47 - previous config)"
    else
        warn "CPU affinity not found in service file"
    fi
    
    if grep -q "Nice=-10" /etc/systemd/system/k3s.service; then
        success "Process priority configured in service file"
    else
        warn "Process priority not found in service file"
    fi
    
    if grep -q "IOSchedulingClass=1" /etc/systemd/system/k3s.service; then
        success "I/O scheduling configured in service file"
    else
        warn "I/O scheduling not found in service file"
    fi
else
    error "K3s service file not found"
fi

echo ""
echo -e "${BLUE}===========================================${NC}"
echo -e "${BLUE}  Verification Complete${NC}"
echo -e "${BLUE}===========================================${NC}"

# Summary recommendations
echo ""
log "Recommendations:"
echo "• Monitor system load with: watch -n 1 uptime"
echo "• Check CPU distribution with: htop or top"
echo "• Monitor memory usage with: free -h"
echo "• View IRQ distribution with: cat /proc/interrupts"
echo "• Check K3s logs with: journalctl -fu k3s"
echo ""
log "Alternative K3s Process Detection:"
echo "• Find by binary: pgrep -f '/usr/local/bin/k3s'"
echo "• Find by command: ps aux | grep 'k3s server'"
echo "• Check systemd: systemctl show k3s --property=MainPID"

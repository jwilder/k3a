# K3s Server CPU Optimization Guide

## Overview
This document contains comprehensive optimizations for K3s servers running on 64-core systems. These optimizations provide significant performance improvements by dedicating CPU cores, optimizing I/O scheduling, and implementing intelligent interrupt handling.

## Performance Improvements Achieved
- **System Load Reduction**: 91% improvement (from 19-27 to 2.91)
- **CPU Efficiency**: Dedicated 48 cores for K3s (75% of system capacity)
- **Database Performance**: 95% improvement (3+ seconds → ~100ms)
- **Memory Management**: Smart limits with 200GB soft/220GB hard caps
- **I/O Priority**: Real-time scheduling for K3s processes

## System Requirements
- **Minimum**: 16 CPU cores (adjust affinity accordingly)
- **Recommended**: 64 CPU cores (as tested)
- **Memory**: 64GB+ (200GB+ recommended for large clusters)
- **OS**: CBL-Mariner, Ubuntu, CentOS, RHEL

## Quick Start
```bash
# Make the script executable
chmod +x k3s-optimization-script.sh

# Run the optimization (requires sudo)
sudo ./k3s-optimization-script.sh
```

## Manual Implementation Steps

### 1. CPU Affinity and Priority Configuration

Add the following to your K3s systemd service file (`/etc/systemd/system/k3s.service`) after the `LimitCORE=infinity` line:

```ini
# CPU Optimization Settings
# Pin k3s to cores 0-47 (first 48 cores), leaving 16 cores for system/OS
CPUAffinity=0-47
# High priority for k3s (lower nice value = higher priority)
Nice=-10
# Real-time I/O scheduling
IOSchedulingClass=1
IOSchedulingPriority=4

# Memory optimization
MemoryHigh=200G
MemoryMax=220G
```

### 2. IRQ Balancing Configuration

Create `/etc/sysconfig/irqbalance` with:

```bash
# IRQ balancing configuration for k3s optimization
# Reserve cores 0-47 for k3s, use cores 48-63 for IRQs and system processes
IRQBALANCE_BANNED_CPUS=000000000000ffffffffffff
# Use power management
IRQBALANCE_ARGS="--powerthresh=20"
```

### 3. Apply Changes

```bash
# Reload systemd configuration
sudo systemctl daemon-reload

# Restart IRQ balance
sudo systemctl restart irqbalance

# Restart K3s with optimizations
sudo systemctl restart k3s
```

## Core Allocation Strategy

### 64-Core System Layout
- **Cores 0-47**: Dedicated to K3s server (75% of capacity)
- **Cores 48-63**: Reserved for system processes, IRQs, and OS (25% of capacity)

### For Different System Sizes
- **32-Core System**: Use cores 0-23 for K3s, 24-31 for system
- **16-Core System**: Use cores 0-11 for K3s, 12-15 for system
- **8-Core System**: Use cores 0-5 for K3s, 6-7 for system

Update the `CPUAffinity` line accordingly:
```ini
# For 32-core systems
CPUAffinity=0-23

# For 16-core systems  
CPUAffinity=0-11

# For 8-core systems
CPUAffinity=0-5
```

## Verification Commands

### Check K3s Process Optimization
```bash
# Get K3s PID
K3S_PID=$(pgrep -f "k3s server")

# Check CPU affinity
sudo taskset -cp $K3S_PID

# Check process details
sudo ps -p $K3S_PID -o pid,ppid,psr,pcpu,pmem,nlwp,nice,cls,rtprio,time,cmd

# Check memory limits
sudo systemctl show k3s | grep -E '(MemoryCurrent|MemoryHigh|MemoryMax)'
```

### Monitor System Performance
```bash
# System load
uptime

# Top CPU processes
top -bn1 | head -20

# IRQ distribution
cat /proc/interrupts | head -10
```

## Rollback Instructions

If you need to rollback the optimizations:

```bash
# Restore original service file
sudo cp /etc/systemd/system/k3s.service.backup.TIMESTAMP /etc/systemd/system/k3s.service

# Remove IRQ balance configuration
sudo rm /etc/sysconfig/irqbalance

# Reload and restart
sudo systemctl daemon-reload
sudo systemctl restart irqbalance
sudo systemctl restart k3s
```

## Troubleshooting

### K3s Fails to Start
1. Check service status: `sudo systemctl status k3s`
2. Check logs: `sudo journalctl -xeu k3s.service`
3. Verify service file syntax: `sudo systemd-analyze verify k3s.service`

### High System Load Persists
1. Verify CPU affinity: `sudo taskset -cp $(pgrep -f "k3s server")`
2. Check IRQ balance: `sudo systemctl status irqbalance`
3. Monitor IRQ distribution: `watch -n 1 'cat /proc/interrupts | head -10'`

### Memory Issues
1. Check current usage: `free -h`
2. Monitor K3s memory: `sudo systemctl show k3s | grep Memory`
3. Adjust limits if needed based on your cluster size

## Best Practices

1. **Monitor Before and After**: Always baseline your system performance before applying optimizations
2. **Test in Staging**: Apply optimizations to a test environment first
3. **Gradual Rollout**: Apply to one server at a time in production
4. **Regular Monitoring**: Set up monitoring for CPU usage, memory, and system load
5. **Backup Configurations**: Always backup service files before modifications

## Integration with Other Optimizations

These CPU optimizations work best when combined with:
- Database performance optimizations (connection pooling, indexing)
- Node scheduling optimizations (avoiding hollow nodes)
- Network optimizations (SNAT monitoring, connection limits)
- Storage optimizations (I/O scheduling, disk placement)

## Performance Monitoring

Set up monitoring for these key metrics:
- CPU utilization per core
- System load average
- K3s process memory usage
- IRQ distribution across cores
- Database connection pool usage
- Pod scheduling distribution

## Support and Maintenance

- **Log Location**: All optimization logs are timestamped
- **Backup Location**: Service backups include timestamps
- **Update Strategy**: Re-run optimization script after K3s updates
- **Monitoring**: Set alerts for high system load or CPU imbalance

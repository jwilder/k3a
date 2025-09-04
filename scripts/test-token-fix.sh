log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

log "🔧 Comprehensive Service Account Token Fix"

for port in "${SERVER_PORTS[@]}"; do
    log "📡 Fixing server at port $port..."
    
    ssh -p $port azureuser@$SERVER_HOST "
        # Backup current service file
        sudo cp /etc/systemd/system/k3s.service /etc/systemd/system/k3s.service.backup.comprehensive-fix.\$(date +%Y%m%d-%H%M%S)
        
        # Fix the service file formatting and add proper token configuration
        sudo tee /etc/systemd/system/k3s.service > /dev/null << 'EOFSERVICE'
[Unit]
Description=Lightweight Kubernetes

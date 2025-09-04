#!/bin/bash

# Quick database connection script for vapa18 cluster
# Usage: source ./scripts/connect-vapa18-db.sh

echo "🔐 Connecting to vapa18 database..."

# Set connection details
export PGHOST="k3apg13te9db7sm5tg.postgres.database.azure.com"
export PGDATABASE="postgres" 
export PGUSER="azureuser"
export PGPORT="5432"
export PGSSLMODE="require"

# Get password from Azure Key Vault
echo "Retrieving password from Key Vault..."
export PGPASSWORD=$(az keyvault secret show --vault-name "k3akv13te9db7sm5tg" --name "postgres-admin-password" --query "value" -o tsv 2>/dev/null)

if [ -n "$PGPASSWORD" ]; then
    echo "✅ Password retrieved successfully"
    
    # Test connection
    if psql -c "SELECT 'Connected to vapa18!' as status, current_timestamp;" >/dev/null 2>&1; then
        echo "✅ Database connection successful"
        echo ""
        echo "Connection details:"
        echo "  Host: $PGHOST"
        echo "  Database: $PGDATABASE"
        echo "  User: $PGUSER"
        echo "  Password: ✓ Set"
        echo ""
        echo "🚀 Ready to run database commands!"
        echo ""
        echo "Emergency performance fix:"
        echo "  psql -c \"ANALYZE kine;\""
        echo ""
        echo "Check query performance:"
        echo "  psql -c \"EXPLAIN ANALYZE SELECT ...\""
        
        return 0 2>/dev/null || exit 0
    else
        echo "❌ Connection test failed"
        return 1 2>/dev/null || exit 1
    fi
else
    echo "❌ Failed to retrieve password from Key Vault"
    echo "Make sure you're logged into Azure CLI and have access to k3akv13te9db7sm5tg"
    return 1 2>/dev/null || exit 1
fi

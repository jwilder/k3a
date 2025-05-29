#!/bin/bash
# etcd compatibility test suite for Kubernetes
# Tests the minimum etcd functionality required by kube-apiserver
# Usage: ./etcd-test.sh <endpoint>
# Example: ./etcd-test.sh http://57.154.51.176:3379

set -uo pipefail

ENDPOINT="${1:?Usage: $0 <etcd-endpoint>}"
ETCDCTL="ETCDCTL_API=3 etcdctl --endpoints=$ENDPOINT"
PASS=0
FAIL=0
PREFIX="/k8s-etcd-test-$(date +%s)"

red()   { echo -e "\033[31m$1\033[0m"; }
green() { echo -e "\033[32m$1\033[0m"; }
yellow(){ echo -e "\033[33m$1\033[0m"; }

pass() { PASS=$((PASS+1)); green "  PASS: $1"; }
fail() { FAIL=$((FAIL+1)); red   "  FAIL: $1"; }

cleanup() {
    eval "$ETCDCTL del $PREFIX --prefix" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "============================================"
echo "etcd Kubernetes Compatibility Test Suite"
echo "Endpoint: $ENDPOINT"
echo "============================================"
echo ""

# -----------------------------------------------------------
# Test 1: Basic connectivity
# -----------------------------------------------------------
echo "--- Test 1: Basic Connectivity ---"
if eval "$ETCDCTL endpoint health" >/dev/null 2>&1; then
    pass "Endpoint is healthy"
else
    fail "Endpoint is not healthy"
    echo "Cannot continue without connectivity."
    exit 1
fi

# -----------------------------------------------------------
# Test 2: Put and Get
# -----------------------------------------------------------
echo "--- Test 2: Put and Get ---"
eval "$ETCDCTL put $PREFIX/key1 value1" >/dev/null
GOT=$(eval "$ETCDCTL get $PREFIX/key1 --print-value-only")
if [[ "$GOT" == "value1" ]]; then
    pass "Put/Get works"
else
    fail "Put/Get returned unexpected value: '$GOT'"
fi

# -----------------------------------------------------------
# Test 3: Revision monotonicity
# Kubernetes requires: header.Revision >= key.CreateRevision >= key.ModRevision
# -----------------------------------------------------------
echo "--- Test 3: Revision Monotonicity ---"
eval "$ETCDCTL put $PREFIX/rev-test rev-value" >/dev/null
FIELDS=$(eval "$ETCDCTL get $PREFIX/rev-test -w fields")
HEADER_REV=$(echo "$FIELDS" | grep '"Revision"' | head -1 | awk '{print $NF}')
CREATE_REV=$(echo "$FIELDS" | grep '"CreateRevision"' | awk '{print $NF}')
MOD_REV=$(echo "$FIELDS" | grep '"ModRevision"' | awk '{print $NF}')

echo "  Header Revision: $HEADER_REV"
echo "  CreateRevision:  $CREATE_REV"
echo "  ModRevision:     $MOD_REV"

if [[ "$HEADER_REV" -ge "$CREATE_REV" ]]; then
    pass "Header.Revision ($HEADER_REV) >= CreateRevision ($CREATE_REV)"
else
    fail "Header.Revision ($HEADER_REV) < CreateRevision ($CREATE_REV) — IMPOSSIBLE in real etcd"
fi

if [[ "$HEADER_REV" -ge "$MOD_REV" ]]; then
    pass "Header.Revision ($HEADER_REV) >= ModRevision ($MOD_REV)"
else
    fail "Header.Revision ($HEADER_REV) < ModRevision ($MOD_REV) — IMPOSSIBLE in real etcd"
fi

# -----------------------------------------------------------
# Test 4: Watch delivers events
# Kubernetes informers depend entirely on watch to track state changes
# -----------------------------------------------------------
echo "--- Test 4: Watch Delivers Events ---"
WATCH_OUTPUT=$(mktemp)

# Start watch in background
eval "$ETCDCTL watch $PREFIX/watch-test" > "$WATCH_OUTPUT" 2>&1 &
WATCH_PID=$!
sleep 2

# Write a value
eval "$ETCDCTL put $PREFIX/watch-test watch-event-1" >/dev/null
sleep 3

# Kill the watch
kill "$WATCH_PID" 2>/dev/null || true
wait "$WATCH_PID" 2>/dev/null || true

WATCH_CONTENT=$(cat "$WATCH_OUTPUT")
rm -f "$WATCH_OUTPUT"

if echo "$WATCH_CONTENT" | grep -q "watch-event-1"; then
    pass "Watch received PUT event"
else
    fail "Watch did NOT receive PUT event (got: '$(echo "$WATCH_CONTENT" | tr '\n' ' ')')"
    yellow "  This breaks ALL Kubernetes controllers (scheduler, DaemonSet, Deployment, etc.)"
fi

# -----------------------------------------------------------
# Test 5: Watch with revision (start from specific revision)
# kube-apiserver uses this to resume watches after reconnection
# -----------------------------------------------------------
echo "--- Test 5: Watch from Revision ---"

# Put a key and capture the mod revision
eval "$ETCDCTL put $PREFIX/watch-rev-test rev-value-1" >/dev/null
FIELDS=$(eval "$ETCDCTL get $PREFIX/watch-rev-test -w fields")
PREV_REV=$(echo "$FIELDS" | grep '"ModRevision"' | awk '{print $NF}')

# Put again to create a new event
eval "$ETCDCTL put $PREFIX/watch-rev-test rev-value-2" >/dev/null

# Watch starting from the previous revision
WATCH_OUTPUT=$(mktemp)
eval "$ETCDCTL watch $PREFIX/watch-rev-test --rev=$PREV_REV" > "$WATCH_OUTPUT" 2>&1 &
WATCH_PID=$!
sleep 3
kill "$WATCH_PID" 2>/dev/null || true
wait "$WATCH_PID" 2>/dev/null || true

WATCH_CONTENT=$(cat "$WATCH_OUTPUT")
rm -f "$WATCH_OUTPUT"

if echo "$WATCH_CONTENT" | grep -q "rev-value-2"; then
    pass "Watch from revision received historical event"
else
    fail "Watch from revision did NOT receive historical event"
    yellow "  This breaks kube-apiserver watch resume after reconnection"
fi

# -----------------------------------------------------------
# Test 6: Watch prefix (multiple keys)
# Kubernetes watches entire prefixes like /registry/pods/
# -----------------------------------------------------------
echo "--- Test 6: Watch Prefix ---"
WATCH_OUTPUT=$(mktemp)

eval "$ETCDCTL watch $PREFIX/prefix/ --prefix" > "$WATCH_OUTPUT" 2>&1 &
WATCH_PID=$!
sleep 2

eval "$ETCDCTL put $PREFIX/prefix/key-a prefix-val-a" >/dev/null
eval "$ETCDCTL put $PREFIX/prefix/key-b prefix-val-b" >/dev/null
sleep 3

kill "$WATCH_PID" 2>/dev/null || true
wait "$WATCH_PID" 2>/dev/null || true

WATCH_CONTENT=$(cat "$WATCH_OUTPUT")
rm -f "$WATCH_OUTPUT"

FOUND_A=false
FOUND_B=false
echo "$WATCH_CONTENT" | grep -q "prefix-val-a" && FOUND_A=true
echo "$WATCH_CONTENT" | grep -q "prefix-val-b" && FOUND_B=true

if $FOUND_A && $FOUND_B; then
    pass "Prefix watch received both events"
elif $FOUND_A || $FOUND_B; then
    fail "Prefix watch only received partial events (a=$FOUND_A, b=$FOUND_B)"
else
    fail "Prefix watch received NO events"
    yellow "  This breaks all Kubernetes list-watch informers"
fi

# -----------------------------------------------------------
# Test 7: Watch delivers DELETE events
# Kubernetes uses delete watches for garbage collection, pod eviction, etc.
# -----------------------------------------------------------
echo "--- Test 7: Watch Delivers DELETE Events ---"
eval "$ETCDCTL put $PREFIX/del-test del-value" >/dev/null

WATCH_OUTPUT=$(mktemp)
eval "$ETCDCTL watch $PREFIX/del-test" > "$WATCH_OUTPUT" 2>&1 &
WATCH_PID=$!
sleep 2

eval "$ETCDCTL del $PREFIX/del-test" >/dev/null
sleep 3

kill "$WATCH_PID" 2>/dev/null || true
wait "$WATCH_PID" 2>/dev/null || true

WATCH_CONTENT=$(cat "$WATCH_OUTPUT")
rm -f "$WATCH_OUTPUT"

if echo "$WATCH_CONTENT" | grep -q "DELETE"; then
    pass "Watch received DELETE event"
else
    fail "Watch did NOT receive DELETE event"
    yellow "  This breaks pod deletion, garbage collection, lease expiry"
fi

# -----------------------------------------------------------
# Test 8: Endpoint status sanity
# DB size and raft index should be non-zero after writes
# -----------------------------------------------------------
echo "--- Test 8: Endpoint Status Sanity ---"
STATUS_LINE=$(eval "$ETCDCTL endpoint status" 2>&1)
echo "  $STATUS_LINE"
# Format: endpoint, id, version, dbSize, isLeader, isLearner, raftTerm, raftIndex, raftAppliedIndex, errors
DB_SIZE=$(echo "$STATUS_LINE" | awk -F', ' '{print $4}' | xargs)
RAFT_INDEX=$(echo "$STATUS_LINE" | awk -F', ' '{print $8}' | xargs)

echo "  DB Size: $DB_SIZE"
echo "  Raft Index: $RAFT_INDEX"

if [[ "$DB_SIZE" != "0" && "$DB_SIZE" != "0 B" ]]; then
    pass "DB size is non-zero ($DB_SIZE)"
else
    fail "DB size is 0 despite having written keys"
    yellow "  Suggests this is not a real etcd (possibly a shim/proxy)"
fi

if [[ -n "$RAFT_INDEX" && "$RAFT_INDEX" != "0" ]]; then
    pass "Raft index is non-zero ($RAFT_INDEX)"
else
    fail "Raft index is 0 despite writes"
    yellow "  Suggests this is not backed by a real raft consensus log"
fi

# -----------------------------------------------------------
# Test 9: Compact and watch (compaction boundary)
# kube-apiserver compacts etcd and relies on "compacted" errors for stale watches
# -----------------------------------------------------------
echo "--- Test 9: Compaction ---"
# Get current revision
FIELDS=$(eval "$ETCDCTL get $PREFIX/key1 -w fields")
CURRENT_REV=$(echo "$FIELDS" | grep '"Revision"' | head -1 | awk '{print $NF}')

if [[ -n "$CURRENT_REV" && "$CURRENT_REV" -gt 0 ]]; then
    COMPACT_RESULT=$(eval "$ETCDCTL compact $CURRENT_REV" 2>&1) || true
    if echo "$COMPACT_RESULT" | grep -qi "compacted\|revision"; then
        pass "Compaction completed"
    else
        fail "Compaction returned unexpected result: $COMPACT_RESULT"
    fi
else
    fail "Could not determine current revision for compaction test"
fi

# -----------------------------------------------------------
# Summary
# -----------------------------------------------------------
echo ""
echo "============================================"
echo "RESULTS: $PASS passed, $FAIL failed"
echo "============================================"

if [[ $FAIL -gt 0 ]]; then
    red "VERDICT: etcd is NOT compatible with Kubernetes"
    echo ""
    echo "Critical issues for Kubernetes:"
    echo "  - Watch event delivery is required for ALL controllers"
    echo "  - Revision consistency is required for optimistic concurrency"
    echo "  - Without working watches, informers are blind and no"
    echo "    scheduling, scaling, or reconciliation can occur"
    exit 1
else
    green "VERDICT: etcd appears compatible with Kubernetes"
    exit 0
fi

# etcd Kubernetes Compatibility Test Results

## Test Environment

| | Real etcd | Dovetail etcd |
|---|---|---|
| **Endpoint** | `http://20.171.68.131:2379` | `http://57.154.51.176:3379` |
| **Resource Group** | `etcd-vapa-1` | `etcd-vapa-2` |
| **Reported Version** | 3.5.17 | 3.6.8 (via gRPC), 3.5.0 (via /version) |
| **Azure Region** | canadacentral | canadacentral |
| **Kubernetes Cluster** | kp204 (worked) | vapa-k3a-dovetail1 (broken) |
| **Kubernetes Version** | v1.35.2 | v1.35.2 |
| **OS** | CBL-Mariner 2 | CBL-Mariner 2 |

### Test Script

The test script (`etcd-test.sh`) validates the minimum etcd functionality required by kube-apiserver:

```
./etcd-test.sh <endpoint>
```

It tests: connectivity, put/get, revision monotonicity, watch event delivery (single key, prefix, from-revision, delete), endpoint status sanity (DB size, raft index), and compaction.

---

## Results Summary

| # | Test | Real etcd | Dovetail etcd |
|---|---|---|---|
| 1 | Basic Connectivity | PASS | PASS |
| 2 | Put and Get | PASS | PASS |
| 3a | Revision: Header >= CreateRevision | PASS (15073 >= 15073) | **FAIL** (512 < 1926) |
| 3b | Revision: Header >= ModRevision | PASS (15073 >= 15073) | **FAIL** (512 < 1926) |
| 4 | Watch Delivers Events (single key) | PASS | **FAIL** |
| 5 | Watch from Revision (historical replay) | PASS | **FAIL** |
| 6 | Watch Prefix (multiple keys) | PASS | **FAIL** |
| 7 | Watch Delivers DELETE Events | PASS | **FAIL** |
| 8a | DB Size > 0 | PASS (16 MB) | **FAIL** (0 B) |
| 8b | Raft Index > 0 | PASS (17029) | **FAIL** (0) |
| 9 | Compaction | PASS | PASS |
| | **Total** | **11 passed, 0 failed** | **3 passed, 8 failed** |

---

## Detailed Findings

### Real etcd (20.171.68.131:2379) — All Tests Pass

```
============================================
etcd Kubernetes Compatibility Test Suite
Endpoint: http://20.171.68.131:2379
============================================

--- Test 1: Basic Connectivity ---
  PASS: Endpoint is healthy
--- Test 2: Put and Get ---
  PASS: Put/Get works
--- Test 3: Revision Monotonicity ---
  Header Revision: 15073
  CreateRevision:  15073
  ModRevision:     15073
  PASS: Header.Revision (15073) >= CreateRevision (15073)
  PASS: Header.Revision (15073) >= ModRevision (15073)
--- Test 4: Watch Delivers Events ---
  PASS: Watch received PUT event
--- Test 5: Watch from Revision ---
  PASS: Watch from revision received historical event
--- Test 6: Watch Prefix ---
  PASS: Prefix watch received both events
--- Test 7: Watch Delivers DELETE Events ---
  PASS: Watch received DELETE event
--- Test 8: Endpoint Status Sanity ---
  http://20.171.68.131:2379, 1c70f9bbb41018f, 3.5.17, 16 MB, true, false, 2, 17029, 17029,
  DB Size: 16 MB
  Raft Index: 17029
  PASS: DB size is non-zero (16 MB)
  PASS: Raft index is non-zero (17029)
--- Test 9: Compaction ---
  PASS: Compaction completed

============================================
RESULTS: 11 passed, 0 failed
============================================
VERDICT: etcd appears compatible with Kubernetes
```

Kubernetes cluster `kp204` was deployed against this endpoint and operated normally:
- Control-plane node joined and was `Ready`
- Worker node auto-joined via cloud-init
- Scaled to 2 workers, second worker auto-joined
- All pods `Running` (flannel, kube-proxy, coredns, local-path-provisioner)
- DaemonSets showed correct `DESIRED` counts

---

### Dovetail etcd (57.154.51.176:3379) — 8 Failures

```
============================================
etcd Kubernetes Compatibility Test Suite
Endpoint: http://57.154.51.176:3379
============================================

--- Test 1: Basic Connectivity ---
  PASS: Endpoint is healthy
--- Test 2: Put and Get ---
  PASS: Put/Get works
--- Test 3: Revision Monotonicity ---
  Header Revision: 512
  CreateRevision:  1926
  ModRevision:     1926
  FAIL: Header.Revision (512) < CreateRevision (1926) — IMPOSSIBLE in real etcd
  FAIL: Header.Revision (512) < ModRevision (1926) — IMPOSSIBLE in real etcd
--- Test 4: Watch Delivers Events ---
  FAIL: Watch did NOT receive PUT event (got: ' ')
  This breaks ALL Kubernetes controllers (scheduler, DaemonSet, Deployment, etc.)
--- Test 5: Watch from Revision ---
  FAIL: Watch from revision did NOT receive historical event
  This breaks kube-apiserver watch resume after reconnection
--- Test 6: Watch Prefix ---
  FAIL: Prefix watch received NO events
  This breaks all Kubernetes list-watch informers
--- Test 7: Watch Delivers DELETE Events ---
  FAIL: Watch did NOT receive DELETE event
  This breaks pod deletion, garbage collection, lease expiry
--- Test 8: Endpoint Status Sanity ---
  http://57.154.51.176:3379, 189a06766ea6bcb7, 3.6.8, 0 B, true, false, 1, 0, 0,
  DB Size: 0 B
  Raft Index: 0
  FAIL: DB size is 0 despite having written keys
  Suggests this is not a real etcd (possibly a shim/proxy)
  FAIL: Raft index is 0 despite writes
  Suggests this is not backed by a real raft consensus log
--- Test 9: Compaction ---
  PASS: Compaction completed

============================================
RESULTS: 3 passed, 8 failed
============================================
VERDICT: etcd is NOT compatible with Kubernetes
```

Kubernetes cluster `vapa-k3a-dovetail1` was deployed against this endpoint and exhibited:
- Control-plane node stuck in `NotReady` (flannel CNI never started)
- `kube-proxy` pod stuck in `Terminating` indefinitely
- `flannel` pod stuck in `Init:0/2`
- `coredns` pods stuck in `Pending`
- DaemonSet controller never detected nodes (DESIRED=0 behavior)
- Worker node joined via kubeadm but never appeared in `kubectl get nodes`
- Scheduler never scheduled any pods

---

## Analysis

### Issue 1: Watch Events Are Never Delivered

All four watch tests fail — single key, prefix, from-revision, and delete watches all return zero events. This is the most critical failure.

Kubernetes is built entirely on the **list-watch** pattern. Every controller (scheduler, DaemonSet controller, deployment controller, endpoint controller, etc.) creates a watch stream on etcd via the apiserver. When watch events are not delivered:

- **Scheduler** never sees new pods → pods are never scheduled
- **DaemonSet controller** never sees node events → DESIRED count stays 0
- **Endpoint controller** never sees pod changes → services have no endpoints
- **Node controller** never sees heartbeat updates → nodes marked Unknown
- **Garbage collection** never sees delete events → resources leak

### Issue 2: Revision Counter Inconsistency

The response header `Revision` (512) is less than the key's `CreateRevision` (1926). In a correctly functioning etcd, the header revision is always the store's current revision and must be >= any key's create or modification revision. This means two independent revision counters exist.

Kubernetes uses revisions for:
- **Optimistic concurrency control** (compare-and-swap on resource versions)
- **Watch resumption** (start watching from the last seen revision)
- **Cache consistency validation** in kube-apiserver

### Issue 3: Zero DB Size and Raft Index

- `DB SIZE = 0 B` despite hundreds of keys being stored
- `RAFT INDEX = 0` and `RAFT APPLIED INDEX = 0` despite thousands of writes
- `RAFT TERM = 1` even after data wipes

This strongly indicates the endpoint is **not a standard etcd server**. It is likely a proxy, shim, or alternative backend (e.g., kine, etcd grpc-proxy, or a custom implementation) that emulates the etcd v3 API but does not fully implement the watch and revision semantics.

The `/version` endpoint also returns inconsistent version information: gRPC reports `3.6.8` while `GET /version` returns `etcdserver: 3.5.0`.

---

## Conclusion

The dovetail etcd endpoint at `http://57.154.51.176:3379` passes basic read/write operations but fails all watch-related functionality and revision consistency checks. **It is not compatible with Kubernetes** in its current state. The same behavior was previously observed with the dovetail endpoint at `http://20.106.75.158:3379`.

The real etcd at `http://20.171.68.131:2379` passes all 11 tests and successfully runs Kubernetes clusters including auto-scaling worker pools.

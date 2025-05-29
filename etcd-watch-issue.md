# Etcd Watch Consistency Failure — Dovetail etcd at `http://20.106.75.158:3379`

## Summary
The external etcd instance (`dovetail-member`, etcd 3.6.8) has **broken watch stream delivery** and **inconsistent revision tracking**, making it **incompatible with Kubernetes**. Writes succeed, reads succeed, but watches never deliver events and the revision counter reported in response headers diverges from the actual store revision.

## Symptoms on Kubernetes
- **DaemonSets (kube-proxy, flannel) show `DESIRED=0`** — the DaemonSet controller's node informer never receives update events, so it never learns nodes exist
- **Scheduler never schedules pods** — pod informer never delivers new pod events
- **Pods exist in etcd but don't appear in `kubectl get pods`** — apiserver watch cache is stale because no watch events propagate
- **apiserver Cache Consistency Check failures** — see errors below

## Evidence

### 1. Watch events are never delivered

```
$ (etcdctl watch /test-key &) && sleep 1 && etcdctl put /test-key "hello" && sleep 3
OK          # <-- put succeeded
             # <-- NO watch event was ever delivered (expected: PUT /test-key hello)
```

Both single-key and prefix watches were tested — zero events in all cases. This means all Kubernetes informers (the foundation of the control loop) are completely blind.

### 2. Revision inconsistency — header revision diverges from key revision

```
$ etcdctl get /simple-watch-test -w fields | grep -i rev
"Revision"       : 40047     # header revision
"CreateRevision" : 52249     # key was created at revision 52249
"ModRevision"    : 52249     # key was last modified at revision 52249
```

The header `Revision` (**40047**) is **less than** the key's `CreateRevision` (**52249**). This is impossible in a well-functioning etcd — the header revision must always be >= any key's mod/create revision. This means two different revision counters are being maintained (one for the header response, one for actual key storage).

### 3. `endpoint status` shows anomalous zero values

```
+---------------------------+---------+-----------+-----------+------------+--------------------+
|         ENDPOINT          | DB SIZE | IS LEADER | RAFT TERM | RAFT INDEX | RAFT APPLIED INDEX |
+---------------------------+---------+-----------+-----------+------------+--------------------+
| http://20.106.75.158:3379 |     0 B |      true |         1 |          0 |                  0 |
+---------------------------+---------+-----------+-----------+------------+--------------------+
```

- **DB SIZE = 0 B** despite 371 keys being stored
- **RAFT INDEX = 0, RAFT APPLIED INDEX = 0** despite 52,000+ revisions of writes
- **RAFT TERM = 1** even after a cluster data wipe and fresh bootstrapping

This strongly indicates this is **not a standard etcd server** — it's a proxy, shim, or alternative backend (e.g. kine, etcd grpc-proxy) that emulates the etcd API but doesn't implement the watch/revision semantics correctly.

### 4. apiserver `Cache consistency check error` (smoking gun)

The kube-apiserver logs show repeated errors:

```
E0305 14:23:14 delegator.go:337 "Cache consistency check error"
  err="failed calculating etcd digest: Timeout: Too large resource version: 52349, current: 40074"
  group="rbac.authorization.k8s.io" resource="clusterroles"
```

The apiserver's watch cache thinks the current resourceVersion is **40074** (from the stale header revision), but etcd objects have resourceVersions up to **52349** (the actual store revision). The **~12,000 revision gap** is consistent across all resource types and grows over time. The apiserver is unable to reconcile its cache because watch events bridging this gap are never delivered.

### 5. Member identity

```
$ etcdctl member list -w table
| ID               | NAME            | PEER ADDRS            | CLIENT ADDRS          |
| 1899cc2d120b4ad5 | dovetail-member | http://localhost:2380 | http://localhost:3379 |
```

The member name `dovetail-member` and the non-standard port `3379` suggest this is a **custom etcd deployment** — likely part of the "dovetail" project, possibly a grpc-proxy or kine-backed store.

## Root Cause
The etcd endpoint at `http://20.106.75.158:3379` does **not implement the etcd v3 Watch API correctly**. Specifically:

1. **Watch streams are established but events are never delivered** — either the watch goroutine is not started, or events are being silently dropped
2. **Two divergent revision counters exist** — response headers report a different (lower) revision than the actual key store, suggesting the watch progress notification / revision tracking layer is disconnected from the storage layer
3. **RAFT metadata returns zeros** — the RAFT consensus layer is either not running or not exposed, which is consistent with a proxy/shim architecture

## Impact
This makes the endpoint **unusable as a Kubernetes etcd backend**. Kubernetes requires:
- Working watch streams for all informers (apiserver, controller-manager, scheduler, kubelet)
- Consistent revision ordering (`header.revision >= key.create_revision` always)
- Progress notifications for watch cache consistency checks (k8s 1.30+)

## Recommendation
1. **Check what's actually running on the etcd server** — is it native etcd, etcd grpc-proxy, kine (SQL-backed etcd shim), or something else?
2. If it's a proxy, connect directly to the underlying etcd backend instead
3. If it's kine or similar, check for known watch delivery bugs in that version
4. As a workaround, deploy a standard etcd 3.5.x cluster and use that instead

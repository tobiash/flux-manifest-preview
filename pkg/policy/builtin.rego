package fmp

import rego.v1

enabled(id) if id in input.builtins

network_kind(kind) if kind == "Ingress"
network_kind(kind) if kind == "Gateway"
network_kind(kind) if kind == "HTTPRoute"

stateful_kind(kind) if kind == "StatefulSet"
stateful_kind(kind) if kind == "DaemonSet"

replica_kind(kind) if kind == "Deployment"
replica_kind(kind) if kind == "StatefulSet"
replica_kind(kind) if kind == "ReplicaSet"

classifications contains {
  "id": "image_update",
  "title": "Container image updated",
  "severity": "info",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": sprintf("%s image changed", [change.kind])
} if {
  enabled("image_update")
  some change in input.changes
  change.action == "modified"
  some i
  old_image := change.old.spec.template.spec.containers[i].image
  new_image := change.new.spec.template.spec.containers[i].image
  old_image != new_image
}

classifications contains {
  "id": "secret_change",
  "title": "Secret changed",
  "severity": "warning",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": "Secret data changed"
} if {
  enabled("secret_change")
  some change in input.changes
  change.kind == "Secret"
}

classifications contains {
  "id": "ingress_change",
  "title": "Networking changed",
  "severity": "warning",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": sprintf("%s routing changed", [change.kind])
} if {
  enabled("ingress_change")
  some change in input.changes
  network_kind(change.kind)
}

classifications contains {
  "id": "crd_change",
  "title": "CRD changed",
  "severity": "warning",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": "CustomResourceDefinition changed"
} if {
  enabled("crd_change")
  some change in input.changes
  change.kind == "CustomResourceDefinition"
}

classifications contains {
  "id": "namespace_delete",
  "title": "Namespace removed",
  "severity": "error",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": "Namespace deleted"
} if {
  enabled("namespace_delete")
  some change in input.changes
  change.action == "deleted"
  change.kind == "Namespace"
}

classifications contains {
  "id": "stateful_workload_change",
  "title": "Stateful workload changed",
  "severity": "warning",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": sprintf("%s changed", [change.kind])
} if {
  enabled("stateful_workload_change")
  some change in input.changes
  stateful_kind(change.kind)
}

classifications contains {
  "id": "pvc_change",
  "title": "Persistent volume claim changed",
  "severity": "warning",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": "PersistentVolumeClaim changed"
} if {
  enabled("pvc_change")
  some change in input.changes
  change.kind == "PersistentVolumeClaim"
}

classifications contains {
  "id": "service_type_change",
  "title": "Service type changed",
  "severity": "warning",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": sprintf("Service type changed to %v", [change.new.spec.type])
} if {
  enabled("service_type_change")
  some change in input.changes
  change.action == "modified"
  change.kind == "Service"
  old_spec := object.get(change.old, "spec", {})
  new_spec := object.get(change.new, "spec", {})
  old_type := object.get(old_spec, "type", "ClusterIP")
  new_type := object.get(new_spec, "type", "ClusterIP")
  old_type != new_type
}

classifications contains {
  "id": "replicas_change",
  "title": "Replica count changed",
  "severity": "info",
  "kind": change.kind,
  "namespace": change.namespace,
  "name": change.name,
  "message": sprintf("Replicas changed from %v to %v", [old_replicas, new_replicas])
} if {
  enabled("replicas_change")
  some change in input.changes
  change.action == "modified"
  replica_kind(change.kind)
  old_spec := object.get(change.old, "spec", {})
  new_spec := object.get(change.new, "spec", {})
  old_replicas := object.get(old_spec, "replicas", 1)
  new_replicas := object.get(new_spec, "replicas", 1)
  old_replicas != new_replicas
}
